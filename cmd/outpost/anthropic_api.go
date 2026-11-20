package main

// Anthropic Messages API: POST /v1/messages (also under /anthropic/v1/...),
// so tools that only speak Claude's API, Claude Code among them, can run on
// the cluster's local models. Requests are translated to the same inference
// job the OpenAI API uses, and the answer back: text, tool_use and
// tool_result blocks, base64 images, streaming as Anthropic's SSE events.
// Thinking text from reasoning models is left out of the reply (it would need
// a signature the client may check). Any claude-* model name means "auto".
// Auth: an API token (full or chat only) as Authorization: Bearer or x-api-key.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (c *Controller) anthropicRoutes(mux *http.ServeMux) {
	for _, pre := range []string{"", "/anthropic"} {
		mux.HandleFunc("POST "+pre+"/v1/messages", c.apiCORS(c.authAPI(c.handleAnthropicMessages)))
		mux.HandleFunc("POST "+pre+"/v1/messages/count_tokens", c.apiCORS(c.authAPI(c.handleAnthropicCount)))
	}
	mux.HandleFunc("GET /anthropic/v1/models", c.apiCORS(c.authAPI(c.handleAnthropicModels)))
	mux.HandleFunc("OPTIONS /anthropic/", c.apiCORS(func(w http.ResponseWriter, r *http.Request) {}))
}

func antError(w http.ResponseWriter, status int, typ, msg string) {
	writeJSON(w, status, map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": msg}})
}

type antBlock struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	Source *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	} `json:"source,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // tool_result: string or blocks
	IsError   bool            `json:"is_error,omitempty"`
}

type antMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type antRequest struct {
	Model         string          `json:"model"`
	MaxTokens     int             `json:"max_tokens"`
	System        json.RawMessage `json:"system"`
	Messages      []antMessage    `json:"messages"`
	Stream        bool            `json:"stream"`
	Temperature   *float64        `json:"temperature"`
	TopP          *float64        `json:"top_p"`
	StopSequences []string        `json:"stop_sequences"`
	Tools         []struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"input_schema"`
		Type        string         `json:"type"`
	} `json:"tools"`
	ToolChoice *struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"tool_choice"`
}

// antBlocks reads content that is a string or a list of blocks.
func antBlocks(raw json.RawMessage) ([]antBlock, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []antBlock{{Type: "text", Text: s}}, nil
	}
	var bs []antBlock
	if err := json.Unmarshal(raw, &bs); err != nil {
		return nil, errors.New("content must be a string or a list of content blocks")
	}
	return bs, nil
}

func antText(bs []antBlock) string {
	var parts []string
	for _, b := range bs {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// spec translates the request into an OpenAI-shaped inference job.
func (req *antRequest) spec() (InferenceSpec, error) {
	if len(req.Messages) == 0 {
		return InferenceSpec{}, errors.New("messages: at least one message is required")
	}
	spec := InferenceSpec{Params: InferenceParams{Temperature: req.Temperature, MaxTokens: req.MaxTokens, Stop: req.StopSequences}}
	spec.Params.TopP = req.TopP
	sys, err := antBlocks(req.System)
	if err != nil {
		return spec, fmt.Errorf("system: %v", err)
	}
	if t := antText(sys); t != "" {
		spec.Messages = append(spec.Messages, ChatMessage{Role: "system", Content: t})
	}
	for _, t := range req.Tools {
		if t.Type != "" && t.Type != "custom" { // server tools (web_search_…, bash_…) have no local equivalent
			continue
		}
		var d ToolDef
		d.Type, d.Function.Name, d.Function.Description, d.Function.Parameters = "function", t.Name, t.Description, t.InputSchema
		if d.Function.Parameters == nil {
			d.Function.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		spec.Tools = append(spec.Tools, d)
	}
	if req.ToolChoice != nil && len(spec.Tools) > 0 {
		var tc any
		switch req.ToolChoice.Type {
		case "any":
			tc = "required"
		case "none":
			tc = "none"
		case "tool":
			tc = map[string]any{"type": "function", "function": map[string]string{"name": req.ToolChoice.Name}}
		}
		if tc != nil {
			spec.Params.ToolChoice, _ = json.Marshal(tc)
		}
	}
	for i, m := range req.Messages {
		bs, err := antBlocks(m.Content)
		if err != nil {
			return spec, fmt.Errorf("messages.%d: %v", i, err)
		}
		switch m.Role {
		case "user":
			// tool_result blocks become tool turns, the rest one user turn after them.
			user := ChatMessage{Role: "user"}
			var texts []string
			for _, b := range bs {
				switch b.Type {
				case "tool_result":
					inner, err := antBlocks(b.Content)
					if err != nil {
						return spec, fmt.Errorf("messages.%d: tool_result: %v", i, err)
					}
					out := antText(inner)
					if b.IsError {
						out = "Error: " + out
					}
					spec.Messages = append(spec.Messages, ChatMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: out})
				case "text":
					texts = append(texts, b.Text)
				case "image":
					if b.Source == nil || b.Source.Type != "base64" || !strings.HasPrefix(b.Source.MediaType, "image/") {
						return spec, fmt.Errorf("messages.%d: images must be base64 sources", i)
					}
					user.Images = append(user.Images, "data:"+b.Source.MediaType+";base64,"+b.Source.Data)
				}
			}
			user.Content = strings.Join(texts, "\n")
			if user.Content != "" || len(user.Images) > 0 {
				spec.Messages = append(spec.Messages, user)
			}
		case "assistant":
			a := ChatMessage{Role: "assistant"}
			var texts []string
			for _, b := range bs {
				switch b.Type {
				case "text":
					texts = append(texts, b.Text)
				case "tool_use":
					var tc WireToolCall
					tc.ID, tc.Type, tc.Function.Name = b.ID, "function", b.Name
					tc.Function.Arguments = string(b.Input)
					if tc.Function.Arguments == "" {
						tc.Function.Arguments = "{}"
					}
					a.ToolCalls = append(a.ToolCalls, tc)
				}
			}
			a.Content = strings.Join(texts, "\n")
			spec.Messages = append(spec.Messages, a)
		default:
			return spec, fmt.Errorf("messages.%d: role must be user or assistant", i)
		}
	}
	return spec, nil
}

func antStop(out *InferenceOutput) string {
	switch {
	case len(out.ToolCalls) > 0:
		return "tool_use"
	case out.FinishReason == "length":
		return "max_tokens"
	}
	return "end_turn"
}

// antToolInput turns a tool call's argument string into the input object.
func antToolInput(args string) json.RawMessage {
	var v map[string]any
	if json.Unmarshal([]byte(args), &v) != nil || v == nil {
		return json.RawMessage("{}")
	}
	b, _ := json.Marshal(v)
	return b
}

func (c *Controller) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	var req antRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil {
		antError(w, 400, "invalid_request_error", "bad JSON: "+err.Error())
		return
	}
	spec, err := req.spec()
	if err != nil {
		antError(w, 400, "invalid_request_error", err.Error())
		return
	}
	run, code, err := c.apiAcquire(r.Context(), req.Model)
	if err != nil {
		if r.Context().Err() == nil {
			typ, status := "overloaded_error", 529 // every node busy: clients retry on 529
			if code == 404 {
				typ, status = "not_found_error", 404
			}
			antError(w, status, typ, err.Error())
		}
		return
	}
	defer run.release()
	id := "msg_" + randHex(12)
	usage := func(out *InferenceOutput) map[string]int {
		return map[string]int{"input_tokens": out.PromptTokens, "output_tokens": out.OutputTokens}
	}
	if !req.Stream {
		out, err := c.apiInfer(r.Context(), run, spec, "API: messages", nil)
		if err != nil {
			if r.Context().Err() == nil {
				antError(w, 500, "api_error", err.Error())
			}
			return
		}
		content := []map[string]any{}
		if out.Text != "" {
			content = append(content, map[string]any{"type": "text", "text": out.Text})
		}
		for _, tc := range out.ToolCalls {
			content = append(content, map[string]any{"type": "tool_use", "id": antToolID(tc.ID), "name": tc.Function.Name, "input": antToolInput(tc.Function.Arguments)})
		}
		writeJSON(w, 200, map[string]any{"id": id, "type": "message", "role": "assistant", "model": run.model, "content": content,
			"stop_reason": antStop(out), "stop_sequence": nil, "usage": usage(out)})
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		antError(w, 500, "api_error", "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	event := func(name string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
		fl.Flush()
	}
	event("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": id, "type": "message", "role": "assistant", "model": run.model,
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 0, "output_tokens": 0}}})
	event("ping", map[string]string{"type": "ping"})
	idx, textOpen := 0, false
	out, err := c.apiInfer(r.Context(), run, spec, "API: messages", func(text string, reason bool) {
		if reason {
			return
		}
		if !textOpen {
			event("content_block_start", map[string]any{"type": "content_block_start", "index": idx, "content_block": map[string]string{"type": "text", "text": ""}})
			textOpen = true
		}
		event("content_block_delta", map[string]any{"type": "content_block_delta", "index": idx, "delta": map[string]string{"type": "text_delta", "text": text}})
	})
	if err != nil {
		if r.Context().Err() == nil {
			event("error", map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": err.Error()}})
		}
		return
	}
	if !textOpen && out.Text != "" { // nothing streamed (a node that answers in one piece)
		event("content_block_start", map[string]any{"type": "content_block_start", "index": idx, "content_block": map[string]string{"type": "text", "text": ""}})
		event("content_block_delta", map[string]any{"type": "content_block_delta", "index": idx, "delta": map[string]string{"type": "text_delta", "text": out.Text}})
		textOpen = true
	}
	if textOpen {
		event("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
		idx++
	}
	for _, tc := range out.ToolCalls {
		event("content_block_start", map[string]any{"type": "content_block_start", "index": idx, "content_block": map[string]any{"type": "tool_use", "id": antToolID(tc.ID), "name": tc.Function.Name, "input": map[string]any{}}})
		event("content_block_delta", map[string]any{"type": "content_block_delta", "index": idx, "delta": map[string]string{"type": "input_json_delta", "partial_json": string(antToolInput(tc.Function.Arguments))}})
		event("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
		idx++
	}
	event("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": antStop(out), "stop_sequence": nil}, "usage": map[string]int{"output_tokens": out.OutputTokens}})
	event("message_stop", map[string]string{"type": "message_stop"})
}

// antToolID gives tool calls the toolu_ prefix clients expect, keeping llama.cpp's id inside.
func antToolID(id string) string {
	if id == "" {
		return "toolu_" + randHex(12)
	}
	if strings.HasPrefix(id, "toolu_") {
		return id
	}
	return "toolu_" + id
}

// handleAnthropicCount estimates input tokens (about 4 characters each); Claude
// Code uses it to decide when to compact a conversation.
func (c *Controller) handleAnthropicCount(w http.ResponseWriter, r *http.Request) {
	var req antRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil {
		antError(w, 400, "invalid_request_error", "bad JSON: "+err.Error())
		return
	}
	spec, err := req.spec()
	if err != nil {
		antError(w, 400, "invalid_request_error", err.Error())
		return
	}
	n := 0
	for _, m := range spec.Messages {
		n += len(m.Content) + 8
		for _, tc := range m.ToolCalls {
			n += len(tc.Function.Arguments) + len(tc.Function.Name)
		}
	}
	for _, t := range spec.Tools {
		b, _ := json.Marshal(t)
		n += len(b)
	}
	writeJSON(w, 200, map[string]int{"input_tokens": n/4 + 1})
}

func (c *Controller) handleAnthropicModels(w http.ResponseWriter, r *http.Request) {
	var data []map[string]any
	now := time.Now().UTC().Format(time.RFC3339)
	for _, id := range c.v1ModelIDs() {
		data = append(data, map[string]any{"type": "model", "id": id, "display_name": id, "created_at": now})
	}
	first, last := "", ""
	if len(data) > 0 {
		first, last = data[0]["id"].(string), data[len(data)-1]["id"].(string)
	}
	writeJSON(w, 200, map[string]any{"data": data, "has_more": false, "first_id": first, "last_id": last})
}
