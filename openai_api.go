package main

// OpenAI-compatible API (idea 15): GET /v1/models and POST /v1/chat/completions
// on the controller, so editors and tools (Continue, aider, Open WebUI) can use
// the cluster. Requests need an API token (Settings → API tokens); a token made
// as "chat only" works here and nowhere else. "model" is a model loaded on a
// node, or "auto" for the strongest free node. Like chat, a request only takes
// a free node slot and waits up to apiWait for one, so it never interrupts
// task work.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

var apiWait = 2 * time.Minute

func (c *Controller) openAIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/models", c.authAPI(c.handleV1Models))
	mux.HandleFunc("POST /v1/chat/completions", c.authAPI(c.handleV1Chat))
}

// oaiError writes an error in the OpenAI shape.
func oaiError(w http.ResponseWriter, status int, typ, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": typ, "code": nil}})
}

// authAPI accepts a signed-in browser session or any API token, including chat-only ones.
func (c *Controller) authAPI(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok := false
		c.mu.Lock()
		if ck, err := r.Cookie("outpost_session"); err == nil {
			if exp, found := c.sessions[ck.Value]; found && time.Now().Before(exp) {
				ok = true
			}
		}
		if !ok {
			if i := c.bearerAPIToken(r); i >= 0 {
				ok = true
				if time.Since(c.apiTokens[i].LastUsed) > time.Minute {
					c.apiTokens[i].LastUsed = time.Now()
					c.saveAPITokensLocked()
				}
			}
		}
		c.mu.Unlock()
		if !ok {
			oaiError(w, 401, "invalid_request_error", "missing or unknown API token (create one under Settings → API tokens)")
			return
		}
		h(w, r)
	}
}

// v1ModelIDs lists the models loaded and ready on online nodes, plus "auto".
func (c *Controller) v1ModelIDs() []string {
	seen := map[string]bool{}
	for _, id := range c.workerNodes() {
		c.mu.Lock()
		if hb := c.lastHB[id]; hb != nil && hb.LoadedModel != "" {
			seen[hb.LoadedModel] = true
		}
		c.mu.Unlock()
	}
	out := []string{"auto"}
	var names []string
	for m := range seen {
		names = append(names, m)
	}
	sort.Strings(names)
	return append(out, names...)
}

func (c *Controller) handleV1Models(w http.ResponseWriter, r *http.Request) {
	var data []map[string]any
	now := time.Now().Unix()
	for _, id := range c.v1ModelIDs() {
		data = append(data, map[string]any{"id": id, "object": "model", "created": now, "owned_by": "outpost"})
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}

// v1Message accepts content as a string or as a list of parts (text only;
// image parts become data URIs for vision models).
type v1Message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []WireToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type v1Request struct {
	Model       string      `json:"model"`
	Messages    []v1Message `json:"messages"`
	Stream      bool        `json:"stream"`
	MaxTokens   int         `json:"max_tokens"`
	MaxCompl    int         `json:"max_completion_tokens"`
	Temperature *float64    `json:"temperature"`
	Stop        any         `json:"stop"`
	Tools       []ToolDef   `json:"tools"`
}

func (m v1Message) toChat() (ChatMessage, error) {
	out := ChatMessage{Role: m.Role, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID}
	switch m.Role {
	case "system", "developer":
		out.Role = "system"
	case "user", "assistant", "tool":
	default:
		return out, fmt.Errorf("unknown role %q", m.Role)
	}
	if len(m.Content) == 0 || string(m.Content) == "null" {
		return out, nil
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		out.Content = s
		return out, nil
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(m.Content, &parts); err != nil {
		return out, errors.New("content must be a string or a list of parts")
	}
	var sb strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "text":
			sb.WriteString(p.Text)
		case "image_url":
			if !strings.HasPrefix(p.ImageURL.URL, "data:image/") {
				return out, errors.New("images must be sent as data: URIs")
			}
			out.Images = append(out.Images, p.ImageURL.URL)
		}
	}
	out.Content = sb.String()
	return out, nil
}

func (req *v1Request) spec() (InferenceSpec, error) {
	if len(req.Messages) == 0 {
		return InferenceSpec{}, errors.New("messages is required")
	}
	spec := InferenceSpec{Tools: req.Tools, Params: InferenceParams{Temperature: req.Temperature, MaxTokens: req.MaxTokens}}
	if req.MaxCompl > 0 {
		spec.Params.MaxTokens = req.MaxCompl
	}
	switch st := req.Stop.(type) {
	case string:
		spec.Params.Stop = []string{st}
	case []any:
		for _, x := range st {
			if s, ok := x.(string); ok {
				spec.Params.Stop = append(spec.Params.Stop, s)
			}
		}
	}
	for _, m := range req.Messages {
		cm, err := m.toChat()
		if err != nil {
			return spec, err
		}
		spec.Messages = append(spec.Messages, cm)
	}
	return spec, nil
}

func (c *Controller) handleV1Chat(w http.ResponseWriter, r *http.Request) {
	var req v1Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil {
		oaiError(w, 400, "invalid_request_error", "bad JSON: "+err.Error())
		return
	}
	spec, err := req.spec()
	if err != nil {
		oaiError(w, 400, "invalid_request_error", err.Error())
		return
	}
	model := strings.TrimSpace(req.Model)
	if model == "outpost" {
		model = "auto"
	}
	want := model
	if want == "auto" || want == "" {
		want = ""
	}
	node, err := c.acquireModelNode(r.Context(), "auto", want, apiWait)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		code := 503
		if strings.Contains(err.Error(), "no ready node has") {
			code = 404
		}
		oaiError(w, code, "server_error", err.Error())
		return
	}
	defer c.releaseNode(node)
	ctxTok := c.nodeCtx(node)
	if spec.Params.MaxTokens <= 0 || spec.Params.MaxTokens > ctxTok {
		spec.Params.MaxTokens = min(4096, ctxTok/2)
	}
	id := "chatcmpl-" + randHex(12)
	created := time.Now().Unix()
	if !req.Stream {
		out, err := c.runInferenceJob(r.Context(), node, spec, "API: chat", chatTimeout)
		if err != nil {
			if r.Context().Err() == nil {
				oaiError(w, 502, "server_error", err.Error())
			}
			return
		}
		msg := map[string]any{"role": "assistant", "content": out.Text}
		finish := firstNonEmpty(out.FinishReason, "stop")
		if len(out.ToolCalls) > 0 {
			msg["tool_calls"] = out.ToolCalls
			finish = "tool_calls"
		}
		writeJSON(w, 200, map[string]any{"id": id, "object": "chat.completion", "created": created, "model": out.Model,
			"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
			"usage":   map[string]int{"prompt_tokens": out.PromptTokens, "completion_tokens": out.OutputTokens, "total_tokens": out.PromptTokens + out.OutputTokens}})
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		oaiError(w, 500, "server_error", "streaming not supported")
		return
	}
	// The node's streamed text arrives through the live hub under a key of its own.
	key := "api_" + id
	live := taskLive.begin(key, "", "API", c.nodeLabel(node), false)
	_, ch, cancel := taskLive.subscribe(key)
	defer cancel()
	type result struct {
		out *InferenceOutput
		err error
	}
	done := make(chan result, 1)
	ctx, stop := context.WithCancel(r.Context())
	defer stop()
	go func() {
		out, err := c.runInferenceJobLive(ctx, node, spec, "API: chat", chatTimeout, live)
		live.End()
		done <- result{out, err}
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	modelName := firstNonEmpty(model, "auto")
	c.mu.Lock()
	if hb := c.lastHB[node]; hb != nil && hb.LoadedModel != "" {
		modelName = hb.LoadedModel
	}
	c.mu.Unlock()
	chunk := func(delta map[string]any, finish any) {
		b, _ := json.Marshal(map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": modelName,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	chunk(map[string]any{"role": "assistant", "content": ""}, nil)
	fl.Flush()
	forward := func(ev liveEvent) {
		if ev.T != "delta" || ev.Text == "" {
			return
		}
		if ev.Reason {
			chunk(map[string]any{"reasoning_content": ev.Text}, nil)
		} else {
			chunk(map[string]any{"content": ev.Text}, nil)
		}
	}
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				ch = nil
				continue
			}
			forward(ev)
			fl.Flush()
		case res := <-done:
			// Text still queued in the hub went out before the call ended.
			for n := len(ch); ch != nil && n > 0; n-- {
				forward(<-ch)
			}
			if res.err != nil {
				if r.Context().Err() == nil {
					b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": res.err.Error(), "type": "server_error"}})
					fmt.Fprintf(w, "data: %s\n\n", b)
					fmt.Fprint(w, "data: [DONE]\n\n")
					fl.Flush()
				}
				return
			}
			finish := firstNonEmpty(res.out.FinishReason, "stop")
			if len(res.out.ToolCalls) > 0 {
				calls := make([]map[string]any, len(res.out.ToolCalls))
				for i, tc := range res.out.ToolCalls {
					calls[i] = map[string]any{"index": i, "id": tc.ID, "type": "function", "function": tc.Function}
				}
				chunk(map[string]any{"tool_calls": calls}, nil)
				finish = "tool_calls"
			}
			chunk(map[string]any{}, finish)
			fmt.Fprint(w, "data: [DONE]\n\n")
			fl.Flush()
			return
		case <-r.Context().Done():
			return
		}
	}
}
