package main

// Compare: send one prompt to two nodes and show the answers side by side, so
// you can see what a model on the GPU box does against one on the big-RAM
// node (answer, speed, time). A comparison is a one-off: nothing is saved with
// the chats, it lives in memory for an hour and a restart forgets it. Like
// chat it only takes a free slot, waits for it, and never interrupts tasks.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	maxCompares      = 20
	compareKeep      = time.Hour
	maxComparePrompt = 8000
)

type CompareSide struct {
	Node   string  `json:"node"` // display name
	Model  string  `json:"model"`
	Status string  `json:"status"` // waiting | working | done | failed | stopped
	Text   string  `json:"text,omitempty"`
	Error  string  `json:"error,omitempty"`
	Tokens int     `json:"tokens,omitempty"`
	TPS    float64 `json:"tok_s,omitempty"`
	Secs   float64 `json:"secs,omitempty"` // from starting to answering, waiting for the node excluded
	Think  string  `json:"think,omitempty"`
}

type Compare struct {
	ID      string         `json:"id"`
	Prompt  string         `json:"prompt"`
	Sides   [2]CompareSide `json:"sides"`
	Created time.Time      `json:"created"`
	Done    bool           `json:"done"`

	cancel context.CancelFunc
}

type compareStore struct {
	mu sync.Mutex
	m  map[string]*Compare
}

func (c *Controller) compares() *compareStore {
	c.cmpOnce.Do(func() { c.cmp = &compareStore{m: map[string]*Compare{}} })
	return c.cmp
}

func (c *Controller) compareRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/compare", c.auth(c.handleCompareStart))
	mux.HandleFunc("GET /api/compare/{id}", c.auth(c.handleCompareGet))
	mux.HandleFunc("DELETE /api/compare/{id}", c.auth(c.handleCompareStop))
}

func (c *Controller) handleCompareStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt string    `json:"prompt"`
		Nodes  [2]string `json:"nodes"`
	}
	if err := readJSONBody(w, r, &req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" || len([]rune(req.Prompt)) > maxComparePrompt {
		writeJSON(w, 400, map[string]string{"error": "write a prompt (up to 8000 characters)"})
		return
	}
	if req.Nodes[0] == "" || req.Nodes[1] == "" || req.Nodes[0] == req.Nodes[1] {
		writeJSON(w, 400, map[string]string{"error": "pick two different nodes"})
		return
	}
	cmp := &Compare{ID: newID("cmp"), Prompt: req.Prompt, Created: time.Now()}
	for i, id := range req.Nodes {
		if !c.compareReady(id) {
			writeJSON(w, 400, map[string]string{"error": "that node is offline, paused or has no model ready"})
			return
		}
		cmp.Sides[i] = CompareSide{Node: c.nodeLabel(id), Model: c.loadedModelOf(id), Status: "waiting"}
	}
	s := c.compares()
	s.mu.Lock()
	for id, o := range s.m { // forget old ones
		if time.Since(o.Created) > compareKeep {
			delete(s.m, id)
		}
	}
	if len(s.m) >= maxCompares {
		s.mu.Unlock()
		writeJSON(w, 400, map[string]string{"error": "too many comparisons running; wait a moment"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatWait+chatTimeout)
	cmp.cancel = cancel
	s.m[cmp.ID] = cmp
	s.mu.Unlock()
	var wg sync.WaitGroup
	for i := range cmp.Sides {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.compareSide(ctx, cmp, i, req.Nodes[i])
		}(i)
	}
	go func() {
		wg.Wait()
		cancel()
		s.mu.Lock()
		cmp.Done = true
		s.mu.Unlock()
	}()
	writeJSON(w, 200, map[string]string{"id": cmp.ID})
}

// compareReady reports whether a node is online, not paused and has a model loaded.
func (c *Controller) compareReady(id string) bool {
	for _, n := range c.workerNodes() {
		if n == id {
			return true
		}
	}
	return false
}

func (c *Controller) loadedModelOf(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if hb := c.lastHB[id]; hb != nil {
		return hb.LoadedModel
	}
	return ""
}

func (c *Controller) compareUpdate(cmp *Compare, i int, f func(s *CompareSide)) {
	st := c.compares()
	st.mu.Lock()
	f(&cmp.Sides[i])
	st.mu.Unlock()
}

func (c *Controller) compareSide(ctx context.Context, cmp *Compare, i int, node string) {
	got, err := c.acquireNodeWhere(ctx, node, nil, nil, chatWait)
	if err != nil {
		c.compareUpdate(cmp, i, func(s *CompareSide) { s.Status, s.Error = compareStatus(ctx, err), err.Error() })
		return
	}
	defer c.releaseNode(got)
	c.compareUpdate(cmp, i, func(s *CompareSide) { s.Status, s.Model = "working", c.loadedModelOf(got) })
	ctxTok := c.nodeCtx(got)
	maxOut := chatOutCap(ctxTok, c.nodeSpeed(got))
	start := time.Now()
	out, err := c.runInferenceJob(ctx, got, InferenceSpec{Messages: []ChatMessage{
		{Role: "system", Content: chatBaseSystem},
		{Role: "user", Content: cmp.Prompt},
	}, Params: InferenceParams{MaxTokens: maxOut}}, "Compare", chatTimeout)
	secs := time.Since(start).Seconds()
	c.compareUpdate(cmp, i, func(s *CompareSide) {
		if err != nil {
			s.Status, s.Error = compareStatus(ctx, err), err.Error()
			if s.Status == "stopped" {
				s.Error = "stopped"
			}
			return
		}
		s.Status, s.Text, s.Tokens, s.TPS, s.Secs, s.Think = "done", out.Text, out.OutputTokens, out.TokensPerSec, secs, out.Reasoning
		if out.FinishReason == "length" {
			s.Text += "\n\n…(cut off at the length limit)"
		}
	})
}

func compareStatus(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return "stopped"
	}
	return "failed"
}

func (c *Controller) handleCompareGet(w http.ResponseWriter, r *http.Request) {
	st := c.compares()
	st.mu.Lock()
	cmp := st.m[r.PathValue("id")]
	var snap *Compare
	if cmp != nil {
		cp := *cmp
		snap = &cp
	}
	st.mu.Unlock()
	if snap == nil {
		writeJSON(w, 404, map[string]string{"error": "that comparison is gone (they are kept for an hour)"})
		return
	}
	writeJSON(w, 200, snap)
}

func (c *Controller) handleCompareStop(w http.ResponseWriter, r *http.Request) {
	st := c.compares()
	st.mu.Lock()
	cmp := st.m[r.PathValue("id")]
	st.mu.Unlock()
	if cmp == nil {
		writeJSON(w, 404, map[string]string{"error": "no such comparison"})
		return
	}
	cmp.cancel()
	writeJSON(w, 200, map[string]bool{"ok": true})
}
