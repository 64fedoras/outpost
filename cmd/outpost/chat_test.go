package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeInferNode connects a websocket "node" that answers inference jobs with
// streamed output: some reasoning, then the reply "echo: <last user message>".
// It records the specs it was sent.
func fakeInferNode(t *testing.T, c *Controller, id string, specs *[]InferenceSpec, mu *sync.Mutex) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.mu.Lock()
		c.conns[id] = &nodeConn{conn: conn}
		c.mu.Unlock()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(64 << 20)
	go func() {
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var env Envelope
			json.Unmarshal(b, &env)
			if env.Type != "job.dispatch" {
				continue
			}
			var jd JobDispatch
			json.Unmarshal(env.Body, &jd)
			var spec InferenceSpec
			json.Unmarshal(jd.Spec, &spec)
			mu.Lock()
			*specs = append(*specs, spec)
			mu.Unlock()
			last := spec.Messages[len(spec.Messages)-1].Content
			reply := "echo: " + last
			send := func(stream, data string) {
				o, _ := json.Marshal(JobOutput{JobID: jd.JobID, Stream: stream, Data: data, Tokens: 1})
				c.handleNodeMessage(id, &Envelope{Type: "job.output", Body: o})
			}
			send("reasoning", "thinking…")
			send("tokens", reply[:len(reply)/2])
			send("tokens", reply[len(reply)/2:])
			out := InferenceOutput{Text: reply, FinishReason: "stop", PromptTokens: 10, OutputTokens: 3, Model: "m", TokensPerSec: 12}
			if spec.Tools != nil {
				var tc WireToolCall
				tc.ID, tc.Type, tc.Function.Name, tc.Function.Arguments = "call_1", "function", "lookup", `{"q":"x"}`
				out.ToolCalls = []WireToolCall{tc}
			}
			ob, _ := json.Marshal(out)
			res, _ := json.Marshal(JobResult{JobID: jd.JobID, Status: "succeeded", Output: ob})
			c.handleNodeMessage(id, &Envelope{Type: "job.result", Body: res})
		}
	}()
	for i := 0; i < 100; i++ {
		c.mu.Lock()
		ok := c.conns[id] != nil
		c.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fake node did not connect")
}

func chatController(t *testing.T) *Controller {
	c := &Controller{dataDir: t.TempDir(),
		conns: map[string]*nodeConn{}, lastHB: map[string]*Heartbeat{"n": {LoadedModel: "m", InferenceRdy: true}}, lastSeen: map[string]time.Time{},
		jobs: map[string]*JobRec{}, waiters: map[string]chan *JobResult{}, reserved: map[string]int{}, pipelines: map[string]*pipeline{},
		cooldown: map[string]time.Time{}, nodes: map[string]*NodeRec{"n": {ID: "n", Name: "gpu-box", LoadPrefs: &ModelLoad{Name: "m", Ctx: 4096}}},
		chats: map[string]*Chat{}, models: map[string]*ModelRec{}}
	return c
}

func TestChatConversation(t *testing.T) {
	c := chatController(t)
	var specs []InferenceSpec
	var mu sync.Mutex
	fakeInferNode(t, c, "n", &specs, &mu)
	c.chatRoutes(http.NewServeMux()) // the real routes register without clashing
	m := mux2(c)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	rec := do("POST", "/api/chats", `{"text":"hello there"}`)
	if rec.Code != 200 {
		t.Fatalf("new chat %d %s", rec.Code, rec.Body)
	}
	var created struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &created)
	wait := func() *Chat {
		for i := 0; i < 200; i++ {
			ch := c.getChat(created.ID)
			ch.mu.Lock()
			busy, n := ch.Busy, len(ch.Messages)
			ch.mu.Unlock()
			if busy == "" && n%2 == 0 {
				return ch
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("no answer")
		return nil
	}
	ch := wait()
	if ch.Messages[1].Text != "echo: hello there" || ch.Messages[1].Node != "gpu-box" || ch.Messages[1].TPS != 12 || ch.Title != "hello there" {
		t.Fatalf("answer %+v", ch.Messages)
	}
	if c.reserved["n"] != 0 {
		t.Fatal("node slot not released")
	}
	if rec := do("POST", "/api/chats/"+created.ID+"/messages", `{"text":"and again"}`); rec.Code != 200 {
		t.Fatalf("send %d %s", rec.Code, rec.Body)
	}
	wait()
	mu.Lock()
	last := specs[len(specs)-1]
	mu.Unlock()
	// The second call carries the history: system, user, assistant, user.
	if len(last.Messages) != 4 || last.Messages[2].Content != "echo: hello there" || last.Messages[0].Role != "system" {
		t.Fatalf("history %+v", last.Messages)
	}
	// Saved and loaded again.
	c2 := &Controller{dataDir: c.dataDir}
	c2.loadChats()
	if got := c2.getChat(created.ID); got == nil || len(got.Messages) != 4 {
		t.Fatal("chat not saved")
	}
	if rec := do("POST", "/api/chats", `{"text":"  "}`); rec.Code != 400 {
		t.Fatal("empty message accepted")
	}
	if rec := do("DELETE", "/api/chats/"+created.ID, ""); rec.Code != 200 || c.getChat(created.ID) != nil {
		t.Fatal("delete")
	}
}

// mux2 serves the chat and /v1 handlers without the sign-in check.
func mux2(c *Controller) *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("POST /api/chats", c.handleNewChat)
	m.HandleFunc("POST /api/chats/{id}/messages", c.handleChatSend)
	m.HandleFunc("DELETE /api/chats/{id}", c.handleDeleteChat)
	m.HandleFunc("GET /v1/models", c.handleV1Models)
	m.HandleFunc("POST /v1/chat/completions", c.handleV1Chat)
	return m
}

func TestChatNoNode(t *testing.T) {
	old := chatWait
	chatWait = 50 * time.Millisecond
	defer func() { chatWait = old }()
	c := chatController(t)
	c.lastHB = map[string]*Heartbeat{}
	msg := c.answerChat(context.Background(), "x", "auto", chatHistory([]ChatMsg{{Role: "user", Text: "hi"}}), func() {})
	if !strings.Contains(msg.Error, "no node has a model ready") {
		t.Fatalf("error %q", msg.Error)
	}
}

func TestFitHistory(t *testing.T) {
	h := []ChatMessage{{Role: "system", Content: "s"}}
	for i := 0; i < 10; i++ {
		h = append(h, ChatMessage{Role: "user", Content: strings.Repeat("u", 100)}, ChatMessage{Role: "assistant", Content: strings.Repeat("a", 100)})
	}
	h = append(h, ChatMessage{Role: "user", Content: "last"})
	got := fitHistory(h, 500)
	if got[0].Role != "system" || got[len(got)-1].Content != "last" || got[1].Role != "user" || len(got) >= len(h) {
		t.Fatalf("fit: %d messages, first %q", len(got), got[1].Role)
	}
	if len(fitHistory(h, 1<<20)) != len(h) {
		t.Fatal("everything fits")
	}
}

func TestOpenAIAPI(t *testing.T) {
	c := chatController(t)
	var specs []InferenceSpec
	var mu sync.Mutex
	fakeInferNode(t, c, "n", &specs, &mu)
	m := mux2(c)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if !strings.Contains(rec.Body.String(), `"id":"auto"`) || !strings.Contains(rec.Body.String(), `"id":"m"`) {
		t.Fatalf("models %s", rec.Body)
	}
	// Non-streaming, with content parts and a tool.
	body := `{"model":"m","messages":[{"role":"system","content":"be brief"},{"role":"user","content":[{"type":"text","text":"hi "},{"type":"text","text":"there"}]}],
		"max_tokens":50,"stop":"END","tools":[{"type":"function","function":{"name":"lookup","description":"d","parameters":{}}}]}`
	rec = httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	var resp struct {
		Object  string
		Choices []struct {
			Message struct {
				Content   string
				ToolCalls []WireToolCall `json:"tool_calls"`
			}
			FinishReason string `json:"finish_reason"`
		}
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		}
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if rec.Code != 200 || resp.Object != "chat.completion" || resp.Choices[0].Message.Content != "echo: hi there" || resp.Choices[0].FinishReason != "tool_calls" || resp.Usage.TotalTokens != 13 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	sp := specs[0]
	mu.Unlock()
	if sp.Params.MaxTokens != 50 || len(sp.Params.Stop) != 1 || len(sp.Tools) != 1 || sp.Messages[0].Role != "system" {
		t.Fatalf("spec %+v", sp)
	}
	// Streaming: role chunk, reasoning, content pieces, finish, [DONE].
	srv := httptest.NewServer(m)
	defer srv.Close()
	res, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"yo"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var content, reasoning, finish string
	done := false
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		line := strings.TrimPrefix(sc.Text(), "data: ")
		if line == "[DONE]" {
			done = true
			break
		}
		var ck struct {
			Choices []struct {
				Delta struct {
					Content   string
					Reasoning string `json:"reasoning_content"`
				}
				FinishReason *string `json:"finish_reason"`
			}
		}
		if json.Unmarshal([]byte(line), &ck) != nil || len(ck.Choices) == 0 {
			continue
		}
		content += ck.Choices[0].Delta.Content
		reasoning += ck.Choices[0].Delta.Reasoning
		if ck.Choices[0].FinishReason != nil {
			finish = *ck.Choices[0].FinishReason
		}
	}
	if !done || content != "echo: yo" || reasoning != "thinking…" || finish != "stop" {
		t.Fatalf("stream: content %q reasoning %q finish %q done %v", content, reasoning, finish, done)
	}
	// An unknown model is a 404 in the OpenAI shape.
	old := apiWait
	apiWait = 50 * time.Millisecond
	defer func() { apiWait = old }()
	rec = httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"nope","messages":[{"role":"user","content":"x"}]}`)))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("unknown model %d %s", rec.Code, rec.Body)
	}
}

func TestChatOnlyToken(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), sessions: map[string]time.Time{}}
	c.apiTokens = []apiToken{{ID: "a", Hash: hashAPIToken("full"), Scope: ""}, {ID: "b", Hash: hashAPIToken("chatonly"), Scope: "chat"}}
	hit := func(wrap func(http.HandlerFunc) http.HandlerFunc, tok string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		wrap(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })(rec, req)
		return rec.Code
	}
	if hit(c.auth, "full") != 204 || hit(c.auth, "chatonly") != 401 {
		t.Fatal("a chat-only token must not reach the normal API")
	}
	if hit(c.authAPI, "full") != 204 || hit(c.authAPI, "chatonly") != 204 || hit(c.authAPI, "wrong") != 401 {
		t.Fatal("/v1 auth")
	}
}

func TestV1SpecPassesAgentSettings(t *testing.T) {
	var req v1Request
	body := `{"model":"auto","messages":[{"role":"user","content":"x"}],"top_p":0.5,"seed":7,"tool_choice":"required","response_format":{"type":"json_object"},"tools":[{"type":"function","function":{"name":"f"}}],"stream_options":{"include_usage":true}}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	spec, err := req.spec()
	if err != nil {
		t.Fatal(err)
	}
	p := spec.Params
	if p.TopP == nil || *p.TopP != 0.5 || p.Seed == nil || *p.Seed != 7 || string(p.ToolChoice) != `"required"` || !strings.Contains(string(p.ResponseFormat), "json_object") || !req.StreamOptions.IncludeUsage {
		t.Fatalf("params not passed through: %+v", p)
	}
}
