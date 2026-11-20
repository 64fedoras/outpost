package main

// Ollama-compatible API under /ollama/api/...: chat, generate, tags, show,
// version and ps, for apps that only know Ollama. Point them at
// https://<controller>:8443/ollama. Models are the ones loaded on nodes
// (plus "auto"); pull, push, create and delete are refused because models
// are managed on the Models page. Streaming is newline-delimited JSON as
// Ollama sends it. Auth is an API token like the other APIs, so the app must
// be able to send an Authorization header.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (c *Controller) ollamaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /ollama/api/chat", c.apiCORS(c.authAPI(c.handleOllamaChat)))
	mux.HandleFunc("POST /ollama/api/generate", c.apiCORS(c.authAPI(c.handleOllamaGenerate)))
	mux.HandleFunc("GET /ollama/api/tags", c.apiCORS(c.authAPI(c.handleOllamaTags)))
	mux.HandleFunc("GET /ollama/api/ps", c.apiCORS(c.authAPI(c.handleOllamaTags)))
	mux.HandleFunc("POST /ollama/api/show", c.apiCORS(c.authAPI(c.handleOllamaShow)))
	mux.HandleFunc("GET /ollama/api/version", c.apiCORS(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"version": "0.6.0"}) // an Ollama version clients accept; this is not Outpost's version
	}))
	mux.HandleFunc("GET /ollama/{$}", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "Ollama is running") })
	for _, p := range []string{"pull", "push", "create", "delete", "copy"} {
		method := "POST "
		if p == "delete" {
			method = "DELETE "
		}
		mux.HandleFunc(method+"/ollama/api/"+p, c.apiCORS(func(w http.ResponseWriter, r *http.Request) {
			ollamaError(w, 400, "models are managed in Outpost's web UI (Models page), not through this API")
		}))
	}
	mux.HandleFunc("OPTIONS /ollama/", c.apiCORS(func(w http.ResponseWriter, r *http.Request) {}))
}

func ollamaError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type ollamaOptions struct {
	Temperature *float64 `json:"temperature"`
	TopP        *float64 `json:"top_p"`
	Seed        *int     `json:"seed"`
	NumPredict  int      `json:"num_predict"`
	Stop        []string `json:"stop"`
}

type ollamaMsg struct {
	Role      string   `json:"role"`
	Content   string   `json:"content"`
	Images    []string `json:"images,omitempty"` // base64, no data: prefix
	ToolCalls []struct {
		Function struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls,omitempty"`
	ToolName string `json:"tool_name,omitempty"`
}

type ollamaReq struct {
	Model    string          `json:"model"`
	Messages []ollamaMsg     `json:"messages"`
	Prompt   string          `json:"prompt"`
	System   string          `json:"system"`
	Images   []string        `json:"images"`
	Stream   *bool           `json:"stream"`
	Options  ollamaOptions   `json:"options"`
	Tools    []ToolDef       `json:"tools"`
	Format   json.RawMessage `json:"format"`
}

func (req *ollamaReq) streaming() bool { return req.Stream == nil || *req.Stream } // Ollama streams by default

// ollamaImage turns Ollama's bare base64 into a data URI, guessing the type from the bytes.
func ollamaImage(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) < 4 {
		return "", errors.New("images must be base64")
	}
	mt := http.DetectContentType(raw)
	if !strings.HasPrefix(mt, "image/") {
		return "", errors.New("an image is not PNG, JPEG, GIF or WebP")
	}
	return "data:" + mt + ";base64," + b64, nil
}

func (req *ollamaReq) spec(chat bool) (InferenceSpec, error) {
	o := req.Options
	spec := InferenceSpec{Tools: req.Tools, Params: InferenceParams{Temperature: o.Temperature, MaxTokens: o.NumPredict, Stop: o.Stop}}
	spec.Params.TopP, spec.Params.Seed = o.TopP, o.Seed
	switch f := strings.TrimSpace(string(req.Format)); {
	case f == `"json"`:
		spec.Params.ResponseFormat = json.RawMessage(`{"type":"json_object"}`)
	case strings.HasPrefix(f, "{"):
		spec.Params.ResponseFormat, _ = json.Marshal(map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "answer", "schema": json.RawMessage(f)}})
	}
	if !chat {
		if req.System != "" {
			spec.Messages = append(spec.Messages, ChatMessage{Role: "system", Content: req.System})
		}
		req.Messages = []ollamaMsg{{Role: "user", Content: req.Prompt, Images: req.Images}}
	}
	if len(req.Messages) == 0 {
		return spec, errors.New("messages is required")
	}
	n := 0
	for _, m := range req.Messages {
		cm := ChatMessage{Role: m.Role, Content: m.Content}
		switch m.Role {
		case "system", "user", "assistant":
		case "tool":
			cm.ToolCallID = m.ToolName
		default:
			return spec, fmt.Errorf("unknown role %q", m.Role)
		}
		for _, im := range m.Images {
			u, err := ollamaImage(im)
			if err != nil {
				return spec, err
			}
			cm.Images = append(cm.Images, u)
		}
		for _, tc := range m.ToolCalls {
			var w WireToolCall
			n++
			w.ID, w.Type, w.Function.Name = fmt.Sprintf("call_%d", n), "function", tc.Function.Name
			w.Function.Arguments = string(tc.Function.Arguments)
			cm.ToolCalls = append(cm.ToolCalls, w)
		}
		spec.Messages = append(spec.Messages, cm)
	}
	return spec, nil
}

func (c *Controller) handleOllamaChat(w http.ResponseWriter, r *http.Request) {
	c.ollamaRun(w, r, true)
}
func (c *Controller) handleOllamaGenerate(w http.ResponseWriter, r *http.Request) {
	c.ollamaRun(w, r, false)
}

func (c *Controller) ollamaRun(w http.ResponseWriter, r *http.Request, chat bool) {
	var req ollamaReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil {
		ollamaError(w, 400, "bad JSON: "+err.Error())
		return
	}
	if !chat && req.Prompt == "" && len(req.Images) == 0 { // an empty generate only loads the model in Ollama
		writeJSON(w, 200, map[string]any{"model": req.Model, "created_at": time.Now().UTC(), "response": "", "done": true, "done_reason": "load"})
		return
	}
	spec, err := req.spec(chat)
	if err != nil {
		ollamaError(w, 400, err.Error())
		return
	}
	run, code, err := c.apiAcquire(r.Context(), req.Model)
	if err != nil {
		if r.Context().Err() == nil {
			ollamaError(w, code, err.Error())
		}
		return
	}
	defer run.release()
	name := firstNonEmpty(req.Model, run.model)
	start := time.Now()
	piece := func(text string, done bool) map[string]any {
		m := map[string]any{"model": name, "created_at": time.Now().UTC(), "done": done}
		if chat {
			m["message"] = map[string]any{"role": "assistant", "content": text}
		} else {
			m["response"] = text
		}
		return m
	}
	final := func(out *InferenceOutput, text string) map[string]any {
		m := piece(text, true)
		reason := "stop"
		if out.FinishReason == "length" {
			reason = "length"
		}
		m["done_reason"] = reason
		m["total_duration"], m["eval_count"], m["prompt_eval_count"] = time.Since(start).Nanoseconds(), out.OutputTokens, out.PromptTokens
		if chat && len(out.ToolCalls) > 0 {
			var calls []map[string]any
			for _, tc := range out.ToolCalls {
				calls = append(calls, map[string]any{"function": map[string]any{"name": tc.Function.Name, "arguments": antToolInput(tc.Function.Arguments)}})
			}
			m["message"] = map[string]any{"role": "assistant", "content": text, "tool_calls": calls}
		}
		return m
	}
	if !req.streaming() {
		out, err := c.apiInfer(r.Context(), run, spec, "API: ollama", nil)
		if err != nil {
			if r.Context().Err() == nil {
				ollamaError(w, 500, err.Error())
			}
			return
		}
		writeJSON(w, 200, final(out, out.Text))
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		ollamaError(w, 500, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	sent := false
	out, err := c.apiInfer(r.Context(), run, spec, "API: ollama", func(text string, reason bool) {
		if reason {
			return
		}
		sent = true
		enc.Encode(piece(text, false))
		fl.Flush()
	})
	if err != nil {
		if r.Context().Err() == nil {
			enc.Encode(map[string]string{"error": err.Error()})
			fl.Flush()
		}
		return
	}
	rest := ""
	if !sent {
		rest = out.Text
	}
	enc.Encode(final(out, rest))
	fl.Flush()
}

func (c *Controller) handleOllamaTags(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	now := time.Now().UTC()
	for _, id := range c.v1ModelIDs() {
		models = append(models, map[string]any{"name": id, "model": id, "modified_at": now, "size": 0, "digest": "",
			"details": map[string]any{"format": "gguf", "family": "", "parameter_size": "", "quantization_level": ""}})
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

func (c *Controller) handleOllamaShow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
		Name  string `json:"name"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
	m := c.apiModelAlias(firstNonEmpty(req.Model, req.Name))
	for _, id := range c.v1ModelIDs() {
		if id == m || (m == "" && id == "auto") {
			writeJSON(w, 200, map[string]any{"modelfile": "", "parameters": "", "template": "",
				"details":      map[string]any{"format": "gguf"},
				"model_info":   map[string]any{},
				"capabilities": []string{"completion", "tools"}})
			return
		}
	}
	ollamaError(w, 404, "model '"+m+"' not found: no ready node has it loaded")
}
