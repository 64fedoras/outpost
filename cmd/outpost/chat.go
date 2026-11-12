package main

// Chat mode (idea 14): talk straight to a model on a node, no planning and no
// files. A conversation is kept on the controller (data/chats/<id>.json);
// nodes keep nothing. Replies stream to the browser through the same live hub
// tasks use (key "chat_<id>"). A chat only takes a free node slot, so it never
// interrupts task work; it waits up to chatWait for one.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type ChatMsg struct {
	ID    string    `json:"id"`
	Role  string    `json:"role"` // user | assistant
	Text  string    `json:"text"`
	Node  string    `json:"node,omitempty"`  // who answered
	Model string    `json:"model,omitempty"` // the model that answered
	TPS   float64   `json:"tok_s,omitempty"`
	Error string    `json:"error,omitempty"`
	Note  string    `json:"note,omitempty"` // e.g. that the web was searched, or why it was not
	At    time.Time `json:"at"`
}

type Chat struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Node     string    `json:"node"` // "auto" or a node id
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Messages []ChatMsg `json:"messages"`
	GroupID  string    `json:"group_id,omitempty"` // sidebar group (groups.go)
	Busy     string    `json:"busy,omitempty"`     // "" | waiting | answering (not saved meaningfully; reset at load)

	dir    string
	cancel context.CancelFunc
	mu     sync.Mutex
}

// Timings, shortened by tests.
var (
	chatWait    = 5 * time.Minute  // for a free node
	chatTimeout = 15 * time.Minute // one answer
)

const (
	maxChatMsgChars = 32000
	maxChats        = 500
)

func (c *Controller) chatsDir() string { return filepath.Join(c.dataDir, "chats") }

func (ch *Chat) saveLocked() {
	b, _ := json.MarshalIndent(ch, "", "  ")
	if err := os.MkdirAll(ch.dir, 0o700); err == nil {
		if err := writeFileAtomic(filepath.Join(ch.dir, ch.ID+".json"), b, 0o600); err != nil {
			log.Printf("chat %s: save failed: %v", ch.ID, err)
		}
	}
}

func (c *Controller) loadChats() {
	c.chatMu.Lock()
	defer c.chatMu.Unlock()
	c.chats = map[string]*Chat{}
	ents, _ := os.ReadDir(c.chatsDir())
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(c.chatsDir(), e.Name()))
		if err != nil {
			continue
		}
		ch := &Chat{}
		if json.Unmarshal(b, ch) != nil || ch.ID+".json" != e.Name() {
			continue
		}
		ch.dir, ch.Busy = c.chatsDir(), ""
		c.chats[ch.ID] = ch
	}
}

func (c *Controller) getChat(id string) *Chat {
	c.chatMu.Lock()
	defer c.chatMu.Unlock()
	return c.chats[id]
}

func (c *Controller) chatRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/chats", c.auth(c.handleListChats))
	mux.HandleFunc("POST /api/chats", c.auth(c.handleNewChat))
	mux.HandleFunc("GET /api/chats/{id}", c.auth(c.handleGetChat))
	mux.HandleFunc("DELETE /api/chats/{id}", c.auth(c.handleDeleteChat))
	mux.HandleFunc("POST /api/chats/{id}/messages", c.auth(c.handleChatSend))
	mux.HandleFunc("POST /api/chats/{id}/stop", c.auth(c.handleChatStop))
	mux.HandleFunc("GET /api/chats/{id}/events", c.authSession(c.handleChatEvents))
}

func (c *Controller) handleListChats(w http.ResponseWriter, r *http.Request) {
	c.chatMu.Lock()
	out := []map[string]any{}
	for _, ch := range c.chats {
		ch.mu.Lock()
		out = append(out, map[string]any{"id": ch.ID, "title": ch.Title, "updated": ch.Updated, "busy": ch.Busy, "messages": len(ch.Messages), "group_id": ch.GroupID})
		ch.mu.Unlock()
	}
	c.chatMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i]["updated"].(time.Time).After(out[j]["updated"].(time.Time)) })
	writeJSON(w, 200, out)
}

func (c *Controller) handleGetChat(w http.ResponseWriter, r *http.Request) {
	ch := c.getChat(r.PathValue("id"))
	if ch == nil {
		writeJSON(w, 404, map[string]string{"error": "no such chat"})
		return
	}
	ch.mu.Lock()
	b, _ := json.Marshal(ch)
	ch.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

type chatReq struct {
	Text string `json:"text"`
	Node string `json:"node"` // "auto" or a node id; empty keeps the chat's choice
}

func (req *chatReq) check() error {
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		return errors.New("type a message first")
	}
	if len(req.Text) > maxChatMsgChars {
		return fmt.Errorf("messages are limited to %d characters", maxChatMsgChars)
	}
	return nil
}

func (c *Controller) handleNewChat(w http.ResponseWriter, r *http.Request) {
	var req chatReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	if err := req.check(); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	now := time.Now()
	ch := &Chat{ID: newID("chat"), Title: chatTitle(req.Text), Node: firstNonEmpty(req.Node, "auto"), Created: now, Updated: now, dir: c.chatsDir()}
	c.chatMu.Lock()
	if len(c.chats) >= maxChats {
		c.chatMu.Unlock()
		writeJSON(w, 400, map[string]string{"error": "too many chats; delete some first"})
		return
	}
	c.chats[ch.ID] = ch
	c.chatMu.Unlock()
	c.startChatReply(ch, req.Text)
	writeJSON(w, 200, map[string]string{"id": ch.ID})
}

func (c *Controller) handleChatSend(w http.ResponseWriter, r *http.Request) {
	ch := c.getChat(r.PathValue("id"))
	if ch == nil {
		writeJSON(w, 404, map[string]string{"error": "no such chat"})
		return
	}
	var req chatReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	if err := req.check(); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	ch.mu.Lock()
	busy := ch.Busy != ""
	if req.Node != "" {
		ch.Node = req.Node
	}
	ch.mu.Unlock()
	if busy {
		writeJSON(w, 409, map[string]string{"error": "wait for the answer (or stop it) first"})
		return
	}
	c.startChatReply(ch, req.Text)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleChatStop(w http.ResponseWriter, r *http.Request) {
	ch := c.getChat(r.PathValue("id"))
	if ch == nil {
		writeJSON(w, 404, map[string]string{"error": "no such chat"})
		return
	}
	ch.mu.Lock()
	if ch.cancel != nil {
		ch.cancel()
	}
	ch.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleDeleteChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c.chatMu.Lock()
	ch := c.chats[id]
	delete(c.chats, id)
	c.chatMu.Unlock()
	if ch == nil {
		writeJSON(w, 404, map[string]string{"error": "no such chat"})
		return
	}
	ch.mu.Lock()
	if ch.cancel != nil {
		ch.cancel()
	}
	ch.mu.Unlock()
	os.Remove(filepath.Join(c.chatsDir(), id+".json"))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleChatEvents(w http.ResponseWriter, r *http.Request) {
	if c.getChat(r.PathValue("id")) == nil {
		writeJSON(w, 404, map[string]string{"error": "no such chat"})
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, 500, map[string]string{"error": "streaming not supported"})
		return
	}
	serveLive(w, r, fl, "chat_"+r.PathValue("id"))
}

func chatTitle(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	if len([]rune(t)) > 60 {
		t = string([]rune(t)[:60]) + "…"
	}
	return t
}

// startChatReply adds the user's message and answers it in the background.
func (c *Controller) startChatReply(ch *Chat, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), chatWait+chatTimeout)
	ch.mu.Lock()
	ch.Messages = append(ch.Messages, ChatMsg{ID: newID("m"), Role: "user", Text: text, At: time.Now()})
	ch.Updated, ch.Busy, ch.cancel = time.Now(), "waiting", cancel
	pref := ch.Node
	hist := chatHistory(ch.Messages)
	ch.saveLocked()
	ch.mu.Unlock()
	taskLive.changed("chat_" + ch.ID)
	go func() {
		defer cancel()
		msg := c.answerChat(ctx, ch.ID, pref, hist, func() {
			ch.mu.Lock()
			ch.Busy = "answering"
			ch.mu.Unlock()
			taskLive.changed("chat_" + ch.ID)
		})
		ch.mu.Lock()
		msg.ID, msg.At = newID("m"), time.Now()
		ch.Messages = append(ch.Messages, msg)
		ch.Updated, ch.Busy, ch.cancel = time.Now(), "", nil
		ch.saveLocked()
		ch.mu.Unlock()
		taskLive.changed("chat_" + ch.ID)
	}()
}

// chatHistory turns the stored messages into the model's conversation.
// Failed answers are left out.
func chatHistory(msgs []ChatMsg) []ChatMessage {
	out := []ChatMessage{{Role: "system", Content: "You are a helpful, concise assistant. Format code in fenced blocks."}}
	for _, m := range msgs {
		if m.Error != "" || m.Text == "" {
			continue
		}
		out = append(out, ChatMessage{Role: m.Role, Content: m.Text})
	}
	return out
}

// fitHistory keeps the system message and as many of the latest messages as
// fit about budget characters (the newest one always).
func fitHistory(msgs []ChatMessage, budget int) []ChatMessage {
	if len(msgs) <= 2 {
		return msgs
	}
	used := len(msgs[0].Content)
	start := len(msgs)
	for i := len(msgs) - 1; i >= 1; i-- {
		used += len(msgs[i].Content) + 16
		if used > budget && i < len(msgs)-1 {
			break
		}
		start = i
	}
	// Start with a user turn, as chat templates expect.
	for start < len(msgs)-1 && msgs[start].Role != "user" {
		start++
	}
	return append([]ChatMessage{msgs[0]}, msgs[start:]...)
}

// answerChat waits for a free node, runs the call and returns the assistant message.
func (c *Controller) answerChat(ctx context.Context, chatID, pref string, hist []ChatMessage, started func()) ChatMsg {
	node, err := c.acquireModelNode(ctx, pref, "", chatWait)
	if err != nil {
		return ChatMsg{Role: "assistant", Error: err.Error()}
	}
	defer c.releaseNode(node)
	started()
	brief, note := c.chatSearchBrief(ctx, chatID, hist)
	hist = withSearchBrief(hist, brief)
	ctxTok := c.nodeCtx(node)
	maxOut := min(4096, ctxTok/2)
	spec := InferenceSpec{Messages: fitHistory(hist, (ctxTok-maxOut)*3), Params: InferenceParams{MaxTokens: maxOut}}
	name := c.nodeLabel(node)
	live := taskLive.begin("chat_"+chatID, "", "Chat", name, false)
	out, err := c.runInferenceJobLive(ctx, node, spec, "Chat", chatTimeout, live)
	live.End()
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return ChatMsg{Role: "assistant", Node: name, Error: "stopped"}
	case err != nil:
		return ChatMsg{Role: "assistant", Node: name, Error: err.Error(), Note: note}
	}
	text := out.Text
	if out.FinishReason == "length" {
		text += "\n\n…(cut off at the length limit)"
	}
	return ChatMsg{Role: "assistant", Text: text, Node: name, Model: out.Model, TPS: out.TokensPerSec, Note: note}
}

// acquireModelNode reserves a free slot on a ready node: pref "auto" (or "")
// takes the strongest free node, a node id waits for that node, and model
// (when set) only accepts nodes with that model loaded. It gives up after wait.
func (c *Controller) acquireModelNode(ctx context.Context, pref, model string, wait time.Duration) (string, error) {
	deadline := time.Now().Add(wait)
	for {
		ready := c.workerNodes()
		var cands []string
		c.mu.Lock()
		for _, id := range ready {
			if pref != "" && pref != "auto" && id != pref {
				continue
			}
			if model != "" {
				if hb := c.lastHB[id]; hb == nil || hb.LoadedModel != model {
					continue
				}
			}
			cands = append(cands, id)
		}
		c.mu.Unlock()
		for _, id := range cands {
			if c.tryReserve(id) {
				return id, nil
			}
		}
		if time.Now().After(deadline) {
			switch {
			case len(cands) == 0 && model != "":
				return "", fmt.Errorf("no ready node has %s loaded", model)
			case len(cands) == 0 && pref != "" && pref != "auto":
				return "", errors.New("that node is offline, paused or has no model ready")
			case len(cands) == 0:
				return "", errors.New("no node has a model ready; load one on the Nodes page")
			}
			return "", fmt.Errorf("every node was busy for %s; try again later", wait.Round(time.Second))
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
