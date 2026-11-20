package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func compatMux(t *testing.T) (*Controller, *http.ServeMux, *[]InferenceSpec, *sync.Mutex) {
	old := apiWait
	apiWait = 100 * time.Millisecond
	t.Cleanup(func() { apiWait = old })
	c := chatController(t)
	var specs []InferenceSpec
	var mu sync.Mutex
	fakeInferNode(t, c, "n", &specs, &mu)
	c.sessions = map[string]time.Time{}
	c.apiTokens = []apiToken{{ID: "a", Hash: hashAPIToken("tok"), Scope: "chat"}}
	m := http.NewServeMux()
	c.anthropicRoutes(m)
	c.ollamaRoutes(m)
	return c, m, &specs, &mu
}

func send(m *http.ServeMux, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	m.ServeHTTP(rec, req)
	return rec
}

func TestAnthropicMessages(t *testing.T) {
	_, m, specs, mu := compatMux(t)
	key := map[string]string{"x-api-key": "tok", "anthropic-version": "2023-06-01"}
	if rec := send(m, "POST", "/v1/messages", `{"model":"claude-x","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`, nil); rec.Code != 401 {
		t.Fatalf("no key: %d", rec.Code)
	}
	body := `{"model":"claude-sonnet-4-5","max_tokens":50,"system":[{"type":"text","text":"Be brief."}],
	  "messages":[{"role":"user","content":"look it up"},
	    {"role":"assistant","content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"q":"a"}}]},
	    {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"found it"},{"type":"text","text":"and now?"}]}]}`
	rec := send(m, "POST", "/anthropic/v1/messages", body, key)
	if rec.Code != 200 {
		t.Fatalf("messages %d %s", rec.Code, rec.Body)
	}
	var res struct {
		StopReason string `json:"stop_reason"`
		Content    []map[string]any
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	if len(res.Content) != 1 || res.Content[0]["text"] != "echo: and now?" || res.StopReason != "end_turn" {
		t.Fatalf("reply %s", rec.Body)
	}
	mu.Lock()
	got := (*specs)[0].Messages
	mu.Unlock()
	// system, user, assistant (with tool call), tool, user
	if len(got) != 5 || got[0].Role != "system" || got[2].ToolCalls[0].Function.Arguments != `{"q":"a"}` || got[3].Role != "tool" || got[3].ToolCallID != "toolu_1" || got[3].Content != "found it" {
		t.Fatalf("translated %+v", got)
	}
	// With tools the fake node calls one: a tool_use block, streamed.
	rec = send(m, "POST", "/v1/messages", `{"model":"auto","max_tokens":50,"stream":true,"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"go"}]}`, map[string]string{"Authorization": "Bearer tok"})
	var events []string
	var toolStart, text string
	raw := rec.Body.String()
	sc := bufio.NewScanner(strings.NewReader(raw))
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "event: ") {
			events = append(events, l[7:])
		}
		if strings.HasPrefix(l, "data: ") && strings.Contains(l, `"tool_use"`) && strings.Contains(l, "content_block_start") {
			toolStart = l
		}
		if strings.Contains(l, "text_delta") {
			var ev struct{ Delta struct{ Text string } }
			json.Unmarshal([]byte(l[6:]), &ev)
			text += ev.Delta.Text
		}
	}
	joined := strings.Join(events, ",")
	if !strings.HasPrefix(joined, "message_start,ping,content_block_start") || !strings.HasSuffix(joined, "message_delta,message_stop") || !strings.Contains(toolStart, `"toolu_call_1"`) || text != "echo: go" {
		t.Fatalf("events %s tool %s text %q", joined, toolStart, text)
	}
	if !strings.Contains(raw, `"stop_reason":"tool_use"`) {
		t.Fatal("stop reason tool_use")
	}
	if rec := send(m, "POST", "/v1/messages/count_tokens", `{"model":"x","messages":[{"role":"user","content":"hello there"}]}`, key); rec.Code != 200 || !strings.Contains(rec.Body.String(), "input_tokens") {
		t.Fatalf("count %d %s", rec.Code, rec.Body)
	}
	if rec := send(m, "POST", "/v1/messages", `{"model":"x","messages":[{"role":"robot","content":"x"}]}`, key); rec.Code != 400 || !strings.Contains(rec.Body.String(), `"invalid_request_error"`) {
		t.Fatalf("bad role %d %s", rec.Code, rec.Body)
	}
}

func TestOllamaAPI(t *testing.T) {
	_, m, specs, mu := compatMux(t)
	tok := map[string]string{"Authorization": "Bearer tok"}
	rec := send(m, "GET", "/ollama/api/tags", "", tok)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"m"`) {
		t.Fatalf("tags %d %s", rec.Code, rec.Body)
	}
	// Streams by default, one JSON object per line, the last with done=true.
	rec = send(m, "POST", "/ollama/api/chat", `{"model":"m:latest","messages":[{"role":"user","content":"hi","images":["iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="]}]}`, tok)
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	var text string
	for i, l := range lines {
		var p struct {
			Done    bool
			Message struct{ Content string }
		}
		json.Unmarshal([]byte(l), &p)
		text += p.Message.Content
		if p.Done != (i == len(lines)-1) {
			t.Fatalf("done flags: %s", rec.Body)
		}
	}
	if text != "echo: hi" {
		t.Fatalf("text %q", text)
	}
	mu.Lock()
	img := (*specs)[0].Messages[0].Images
	mu.Unlock()
	if len(img) != 1 || !strings.HasPrefix(img[0], "data:image/png;base64,") {
		t.Fatalf("image %v", img)
	}
	rec = send(m, "POST", "/ollama/api/generate", `{"model":"auto","prompt":"say","system":"short","stream":false}`, tok)
	var g struct {
		Response string
		Done     bool
	}
	json.Unmarshal(rec.Body.Bytes(), &g)
	if !g.Done || g.Response != "echo: say" {
		t.Fatalf("generate %s", rec.Body)
	}
	if rec := send(m, "POST", "/ollama/api/pull", `{"model":"x"}`, tok); rec.Code != 400 {
		t.Fatal("pull must be refused")
	}
	if rec := send(m, "POST", "/ollama/api/chat", `{"model":"nope","messages":[{"role":"user","content":"x"}]}`, tok); rec.Code != 404 && rec.Code != 503 {
		t.Fatalf("unknown model %d", rec.Code)
	}
}

func TestAPICORS(t *testing.T) {
	c, m, _, _ := compatMux(t)
	pre := func(origin string) *httptest.ResponseRecorder {
		return send(m, "OPTIONS", "/anthropic/v1/messages", "", map[string]string{"Origin": origin, "Access-Control-Request-Method": "POST"})
	}
	if rec := pre("http://localhost:3000"); rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("origin not listed must get no CORS headers")
	}
	c.settings.APICORSOrigins = normCORSOrigins([]string{"http://localhost:3000/", "javascript:x", "https://a.example/path"})
	if len(c.settings.APICORSOrigins) != 1 {
		t.Fatalf("normalised %v", c.settings.APICORSOrigins)
	}
	if rec := pre("http://localhost:3000"); rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Fatal("listed origin")
	}
}
