package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestLiveHubBufferAndSnapshot(t *testing.T) {
	h := newLiveHub()
	lh := h.begin("t1", "s1", "label", "node", false)
	h.mu.Lock()
	lh.h.jobs["job1"] = [2]string{"t1", lh.callID}
	h.mu.Unlock()
	big := strings.Repeat("x", 40<<10)
	h.jobOutput(JobOutput{JobID: "job1", Data: big, Tokens: 3})
	h.jobOutput(JobOutput{JobID: "job1", Data: big + "END", Tokens: 3})
	snap, ch, cancel := h.subscribe("t1")
	defer cancel()
	if len(snap) != 1 || snap[0].Sub != "s1" || snap[0].Tok != 6 {
		t.Fatalf("snapshot: %+v", snap)
	}
	if len(snap[0].Text) != liveBufBytes || !strings.HasSuffix(snap[0].Text, "END") {
		t.Fatalf("buffer should keep the last %d bytes, got %d", liveBufBytes, len(snap[0].Text))
	}
	h.jobOutput(JobOutput{JobID: "job1", Data: "more", Tokens: 1})
	if ev := <-ch; ev.T != "delta" || ev.Text != "more" || ev.Tok != 7 {
		t.Fatalf("delta: %+v", ev)
	}
	lh.End()
	if ev := <-ch; ev.T != "end" {
		t.Fatalf("end: %+v", ev)
	}
	h.jobOutput(JobOutput{JobID: "job1", Data: "late"}) // job unbound after End: ignored
	h.changed("t1")
	if ev := <-ch; ev.T != "changed" {
		t.Fatalf("changed: %+v", ev)
	}
}

func TestLiveHubSlowSubscriberDropped(t *testing.T) {
	h := newLiveHub()
	lh := h.begin("t1", "", "l", "n", true)
	_, ch, cancel := h.subscribe("t1")
	defer cancel()
	for i := 0; i < 600; i++ {
		lh.Write("abcd")
	}
	n := 0
	for range ch { // must close once the buffer overflowed
		n++
	}
	if n == 0 || n > 512 {
		t.Fatalf("drained %d events", n)
	}
	lh.End()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.tasks) != 0 {
		t.Fatal("hub should forget a task with no calls and no subscribers")
	}
}

// sseServer fakes llama-server's streamed chat completion.
func fakeLlama(chunks []string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, c := range chunks {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": c}}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
			time.Sleep(70 * time.Millisecond)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":4}}\n\ndata: [DONE]\n\n")
	}))
}

// TestNodeStreamsInference runs a node inference job against a fake streaming
// llama-server and checks job.output messages arrive over the WebSocket before
// the end, with token counts.
func TestNodeStreamsInference(t *testing.T) {
	llama := fakeLlama([]string{"he", "llo ", "wor", "ld"})
	defer llama.Close()
	var port int
	fmt.Sscanf(llama.Listener.Addr().(*net.TCPAddr).String()[strings.LastIndex(llama.Listener.Addr().String(), ":")+1:], "%d", &port)

	var mu sync.Mutex
	var outs []JobOutput
	got := make(chan struct{})
	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		go func() {
			time.Sleep(50 * time.Millisecond)
			close(got)
		}()
		for {
			_, b, err := c.Read(context.Background())
			if err != nil {
				return
			}
			var env Envelope
			if json.Unmarshal(b, &env) == nil && env.Type == "job.output" {
				var o JobOutput
				_ = json.Unmarshal(env.Body, &o)
				mu.Lock()
				outs = append(outs, o)
				mu.Unlock()
			}
		}
	}))
	defer ws.Close()
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(ws.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	n := &Node{conn: conn}
	n.llama = newLlamaMgr(n)
	n.llama.cmd, n.llama.ready, n.llama.port, n.llama.model = &exec.Cmd{}, true, port, "m"
	<-got

	spec, _ := json.Marshal(InferenceSpec{Messages: []ChatMessage{{Role: "user", Content: "hi"}}, Stream: true})
	var res JobResult
	n.runInference(context.Background(), JobDispatch{JobID: "j1", Kind: "inference", Spec: spec}, &res)
	if res.Status != "succeeded" {
		t.Fatalf("status %s: %+v", res.Status, res.Error)
	}
	var out InferenceOutput
	_ = json.Unmarshal(res.Output, &out)
	if out.Text != "hello world" {
		t.Fatalf("final text %q", out.Text)
	}
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	var text strings.Builder
	toks := 0
	for _, o := range outs {
		text.WriteString(o.Data)
		toks += o.Tokens
	}
	if text.String() != "hello world" || toks != 4 {
		t.Fatalf("streamed %q with %d tokens over %d messages", text.String(), toks, len(outs))
	}
	if len(outs) < 2 {
		t.Fatalf("expected the text in several messages, got %d", len(outs))
	}
}

func TestClaudeStreamToLive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			t.Error("stream not requested")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":120,"cache_read_input_tokens":20}}}

data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}}

data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}

data: {"type":"message_delta","usage":{"output_tokens":9}}

data: {"type":"message_stop"}

`)
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_ANTHROPIC_URL", srv.URL)
	h := newLiveHub()
	lh := h.begin("t", "", "plan", "Claude", true)
	_, ch, cancel := h.subscribe("t")
	defer cancel()
	r, err := callClaude(context.Background(), "k", "claude-sonnet-5-5", llmRequest{System: "s", User: "u", MaxTokens: 100, Live: lh})
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "Hello" || r.InTokens != 120 || r.OutTokens != 9 || r.CacheRead != 20 || r.CostUSD <= 0 {
		t.Fatalf("response %+v", r)
	}
	var seen string
	for len(ch) > 0 {
		if ev := <-ch; ev.T == "delta" {
			seen += ev.Text
		}
	}
	if seen != "Hello" {
		t.Fatalf("live saw %q", seen)
	}
}

func TestClaudeNonStreamingReplyStillWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"content":[{"type":"text","text":"plain"}],"usage":{"input_tokens":1,"output_tokens":2}}`)
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_ANTHROPIC_URL", srv.URL)
	h := newLiveHub()
	r, err := callClaude(context.Background(), "k", "claude-sonnet-5-5", llmRequest{System: "s", User: "u", MaxTokens: 10, Live: h.begin("t", "", "l", "c", true)})
	if err != nil || r.Text != "plain" {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestOpenRouterStreamToLive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `: OPENROUTER PROCESSING

data: {"choices":[{"delta":{"content":"ab"}}]}

data: {"choices":[{"delta":{"content":"cd"},"finish_reason":"stop"}]}

data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"cost":0.001}}

data: [DONE]

`)
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_OPENROUTER_URL", srv.URL)
	h := newLiveHub()
	lh := h.begin("t", "", "plan", "OpenRouter", true)
	_, ch, cancel := h.subscribe("t")
	defer cancel()
	r, err := callOpenRouter(context.Background(), "k", "vendor/m", llmRequest{System: "s", User: "u", MaxTokens: 100, Live: lh})
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "abcd" || r.OutTokens != 2 || r.CostUSD != 0.001 {
		t.Fatalf("response %+v", r)
	}
	ev := <-ch
	if ev.T == "reset" { // each attempt starts by clearing the text
		ev = <-ch
	}
	if ev.T != "delta" || ev.Text != "ab" {
		t.Fatalf("first live event %+v", ev)
	}
}

func TestTaskEventsEndpoint(t *testing.T) {
	c := &Controller{tasks: map[string]*Task{"tk": {ID: "tk"}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tasks/{id}/events", c.handleTaskEvents)
	ms := httptest.NewServer(mux)
	defer ms.Close()

	lh := taskLive.begin("tk", "s1", "write", "node01", false)
	resp, err := http.Get(ms.URL + "/api/tasks/tk/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	next := func() liveEvent {
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var ev liveEvent
				_ = json.Unmarshal([]byte(d), &ev)
				return ev
			}
		}
		t.Fatal("stream ended")
		return liveEvent{}
	}
	if ev := next(); ev.T != "start" || ev.Sub != "s1" {
		t.Fatalf("snapshot %+v", ev)
	}
	if ev := next(); ev.T != "changed" {
		t.Fatalf("initial %+v", ev)
	}
	lh.Write("xyz")
	if ev := next(); ev.T != "delta" || ev.Text != "xyz" {
		t.Fatalf("delta %+v", ev)
	}
	lh.End()
	if ev := next(); ev.T != "end" {
		t.Fatalf("end %+v", ev)
	}
	r2, _ := http.Get(ms.URL + "/api/tasks/nope/events")
	if r2.StatusCode != 404 {
		t.Fatalf("missing task: %d", r2.StatusCode)
	}
}
