package main

// Embeddings: turn text into vectors on a node (node_embed.go). Used by
// POST /v1/embeddings and, later, knowledge bases. The embedding model is a
// library model flagged as embedding (controller_models.go); it runs on the
// CPU of any online node that has llama.cpp, in a server that starts on demand.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	embedBatchTexts = 32    // texts per job
	embedBatchChars = 60000 // characters per job
	embedMaxTexts   = 512   // one call
	embedMaxChars   = 32000 // one text (about 8k tokens; a text over the model's window is refused by it)
	embedJobTimeout = 12 * time.Minute
)

// EmbedResult is what Embed returns.
type EmbedResult struct {
	Model   string
	Vectors [][]float32
	Tokens  int
}

// embedPrefix is the text a model family wants in front of a document or a query.
// role is "document", "query" or "" (none: the caller's text goes as is).
func embedPrefix(model, role string) string {
	l := strings.ToLower(model)
	switch {
	case strings.Contains(l, "nomic-embed"):
		switch role {
		case "document":
			return "search_document: "
		case "query":
			return "search_query: "
		}
	case strings.Contains(l, "qwen3-embedding"):
		if role == "query" {
			return "Instruct: Given a question, retrieve passages that answer it\nQuery: "
		}
	}
	return ""
}

// embedModel finds the library model to embed with: the one asked for, or the
// first ready embedding model (catalog order, so the default comes first).
func (c *Controller) embedModel(want string) (*ModelRec, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if want != "" && want != "auto" {
		m := c.models[want]
		if m == nil || !isEmbeddingName(m.Name) {
			return nil, fmt.Errorf("%q is not an embedding model in the library", want)
		}
		if m.Status != "ready" {
			return nil, fmt.Errorf("embedding model %s is not ready (%s)", want, m.Status)
		}
		return m, nil
	}
	for _, e := range catalogNow() {
		if m := c.models[e.ID]; e.Embedding && m != nil && m.Status == "ready" {
			return m, nil
		}
	}
	var names []string
	for _, m := range c.models {
		if m.Status == "ready" && isEmbeddingName(m.Name) {
			names = append(names, m.Name)
		}
	}
	if len(names) > 0 {
		sort.Strings(names)
		return c.models[names[0]], nil
	}
	return nil, errors.New("no embedding model in the library: add Nomic Embed Text on the Models page first")
}

// embedNode picks the node to embed on: one that has the model already, then the one with the most cores.
func (c *Controller) embedNode(m *ModelRec) (string, error) {
	type cand struct {
		id    string
		has   bool
		cores int
	}
	var cands []cand
	now := time.Now()
	c.mu.Lock()
	for id := range c.conns {
		n := c.nodes[id]
		hb := c.lastHB[id]
		if n == nil || n.Hello == nil || n.Hello.Runtime == nil || (hb != nil && hb.Paused) || !n.Hours.open(now) || c.pipelines[id] != nil {
			continue
		}
		if until, ok := c.cooldown[id]; ok && now.Before(until) {
			continue
		}
		has := false
		for _, mi := range n.Hello.Models {
			if mi.Name == m.Name {
				has = true
			}
		}
		cands = append(cands, cand{id, has, n.Hello.CPU.Threads})
	}
	c.mu.Unlock()
	if len(cands) == 0 {
		return "", errors.New("no online node has llama.cpp installed, so nothing can compute embeddings")
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].has != cands[j].has {
			return cands[i].has
		}
		if cands[i].cores != cands[j].cores {
			return cands[i].cores > cands[j].cores
		}
		return cands[i].id < cands[j].id
	})
	return cands[0].id, nil
}

// embedNodeLocks makes one embedding batch run on a node at a time.
var embedNodeLocks sync.Map // node id -> *sync.Mutex

// Embed returns one vector per text, L2-normalised, from the named embedding
// model ("" or "auto": the default one). role is "document", "query" or "".
func (c *Controller) Embed(ctx context.Context, want, role string, texts []string) (*EmbedResult, error) {
	if len(texts) == 0 {
		return nil, errors.New("nothing to embed")
	}
	if len(texts) > embedMaxTexts {
		return nil, fmt.Errorf("at most %d texts per call", embedMaxTexts)
	}
	m, err := c.embedModel(want)
	if err != nil {
		return nil, err
	}
	id, err := c.embedNode(m)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	nc := c.conns[id]
	c.mu.Unlock()
	if nc == nil {
		return nil, errors.New("node went offline")
	}
	lk, _ := embedNodeLocks.LoadOrStore(id, &sync.Mutex{})
	mu := lk.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if err := c.ensureModelOnNode(ctx, id, nc, m); err != nil {
		return nil, err
	}
	pre := embedPrefix(m.Name, role)
	res := &EmbedResult{Model: m.Name}
	for i := 0; i < len(texts); {
		j, chars := i, 0
		var batch []string
		for j < len(texts) && len(batch) < embedBatchTexts && (len(batch) == 0 || chars+len(texts[j]) <= embedBatchChars) {
			t := texts[j]
			if len(t) > embedMaxChars {
				return nil, fmt.Errorf("text %d is too long (%d characters, at most %d)", j, len(t), embedMaxChars)
			}
			batch = append(batch, pre+t)
			chars += len(t)
			j++
		}
		out, err := c.runEmbedJob(ctx, id, m.Name, batch)
		if err != nil {
			return nil, err
		}
		res.Vectors = append(res.Vectors, out.Vectors...)
		res.Tokens += out.Tokens
		i = j
	}
	for _, v := range res.Vectors {
		normalize(v)
	}
	return res, nil
}

func (c *Controller) runEmbedJob(ctx context.Context, nodeID, model string, texts []string) (*EmbedOutput, error) {
	spec, _ := json.Marshal(EmbedSpec{Model: model, Texts: texts})
	job := &JobDispatch{Kind: "embed", TimeoutS: int(embedJobTimeout.Seconds()), Spec: spec}
	ch, err := c.dispatchJob(nodeID, job, "embed "+model)
	if err != nil {
		return nil, err
	}
	select {
	case res := <-ch:
		if res.Status != "succeeded" {
			if res.Error != nil {
				return nil, res.Error
			}
			return nil, errors.New("embedding " + res.Status)
		}
		var out EmbedOutput
		if err := json.Unmarshal(res.Output, &out); err != nil {
			return nil, err
		}
		if len(out.Vectors) != len(texts) {
			return nil, fmt.Errorf("the node returned %d vectors for %d texts", len(out.Vectors), len(texts))
		}
		return &out, nil
	case <-ctx.Done():
		c.cancelJob(job.JobID)
		return nil, ctx.Err()
	}
}

// normalize scales v to length 1 (a zero vector is left alone).
func normalize(v []float32) {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return
	}
	k := float32(1 / math.Sqrt(s))
	for i := range v {
		v[i] *= k
	}
}
