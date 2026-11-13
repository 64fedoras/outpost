package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func toolCallOf(id, name, args string) WireToolCall {
	var w WireToolCall
	w.ID, w.Type, w.Function.Name, w.Function.Arguments = id, "function", name, args
	return w
}

// scriptedInfer answers each request from the list in order and records the specs.
func scriptedInfer(outs []*InferenceOutput, errs []error, seen *[]InferenceSpec) inferFn {
	i := 0
	return func(ctx context.Context, spec InferenceSpec) (*InferenceOutput, error) {
		*seen = append(*seen, spec)
		k := i
		i++
		if k < len(errs) && errs[k] != nil {
			return nil, errs[k]
		}
		return outs[k], nil
	}
}

func hist(q string) []ChatMessage {
	return []ChatMessage{{Role: "system", Content: "sys"}, {Role: "user", Content: q}}
}

func TestChatToolLoop(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><p>Go 1.26 is out.</p></body></html>")
	}))
	defer page.Close()
	useSearchStubs(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, ddgPage) }))
	c := &Controller{}
	var seen []InferenceSpec
	infer := scriptedInfer([]*InferenceOutput{
		{ToolCalls: []WireToolCall{toolCallOf("1", "web_search", `{"query":"go release"}`)}},
		{ToolCalls: []WireToolCall{toolCallOf("2", "read_page", `{"url":"`+page.URL+`"}`)}},
		{Text: "Go 1.26, see https://go.dev"},
	}, nil, &seen)
	out, st, err := c.chatWithTools(context.Background(), infer, hist("what is the latest Go?"), 8192, 2048)
	if err != nil || out.Text != "Go 1.26, see https://go.dev" {
		t.Fatalf("%v %+v", err, out)
	}
	if st.searches != 1 || st.engine != "DuckDuckGo" {
		t.Errorf("stats %+v", st)
	}
	// The second call carries the assistant's tool call and the search results.
	m := seen[1].Messages
	if len(seen[1].Tools) != 2 || m[len(m)-1].Role != "tool" || !strings.Contains(m[len(m)-1].Content, "go.dev/doc/devel/release") {
		t.Errorf("second request: %+v", m)
	}
	if !strings.Contains(m[0].Content, "web_search") {
		t.Error("system message does not mention the tools")
	}
	// read_page refuses the local stub page, so the third request carries an error as its tool result.
	last := seen[2].Messages[len(seen[2].Messages)-1]
	if last.Role != "tool" || !strings.HasPrefix(last.Content, "Error:") {
		t.Errorf("third request should end with the tool result: %+v", last)
	}
	if n := st.note(); !strings.Contains(n, "DuckDuckGo") {
		t.Errorf("note %q", n)
	}
}

func TestChatToolReadPageBlocksInternal(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "secret") }))
	defer page.Close()
	c := &Controller{}
	c.settings.WebAllowPrivate = true // must not matter for model-driven fetches
	st := &webStats{}
	got := c.runChatTool(context.Background(), toolCallOf("1", "read_page", `{"url":"`+page.URL+`"}`), 1000, st)
	if strings.Contains(got, "secret") || !strings.HasPrefix(got, "Error:") || st.pages != 0 {
		t.Fatalf("internal page was read: %q", got)
	}
	if got := c.runChatTool(context.Background(), toolCallOf("1", "read_page", `{"url":"http://169.254.169.254/latest"}`), 1000, st); !strings.HasPrefix(got, "Error:") {
		t.Fatalf("metadata address: %q", got)
	}
	if got := c.runChatTool(context.Background(), toolCallOf("1", "nope", `{}`), 1000, st); !strings.Contains(got, "unknown tool") {
		t.Fatal(got)
	}
	if got := c.runChatTool(context.Background(), toolCallOf("1", "web_search", `{bad`), 1000, st); !strings.Contains(got, "not valid JSON") {
		t.Fatal(got)
	}
}

func TestChatToolRoundLimit(t *testing.T) {
	useSearchStubs(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, ddgPage) }))
	c := &Controller{}
	var outs []*InferenceOutput
	for i := 0; i < maxChatToolRounds; i++ {
		outs = append(outs, &InferenceOutput{ToolCalls: []WireToolCall{toolCallOf(fmt.Sprint(i), "web_search", `{"query":"x"}`)}})
	}
	outs = append(outs, &InferenceOutput{Text: "done"})
	var seen []InferenceSpec
	out, _, err := c.chatWithTools(context.Background(), scriptedInfer(outs, nil, &seen), hist("q"), 8192, 2048)
	if err != nil || out.Text != "done" {
		t.Fatalf("%v %+v", err, out)
	}
	if last := seen[len(seen)-1]; len(last.Tools) != 0 {
		t.Error("the final request must not offer tools")
	}
}

func TestChatToolsUnsupportedFallsBackToSearchOnce(t *testing.T) {
	useSearchStubs(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, ddgPage) }))
	c := &Controller{}
	var seen []InferenceSpec
	infer := scriptedInfer([]*InferenceOutput{nil, {Text: "answer"}},
		[]error{errors.New("llama-server 500: tools param requires --jinja flag")}, &seen)
	out, note, err := c.answerWithWeb(context.Background(), infer, "big-7b.gguf", hist("latest Go version?"), 8192, 2048)
	if err != nil || out.Text != "answer" {
		t.Fatalf("%v %+v", err, out)
	}
	if len(seen) != 2 || len(seen[1].Tools) != 0 {
		t.Fatalf("want a plain second request: %+v", seen)
	}
	u := seen[1].Messages[len(seen[1].Messages)-1].Content
	if !strings.Contains(u, "latest Go version?") || !strings.Contains(u, "https://go.dev/doc/devel/release") {
		t.Errorf("results not put in front of the question: %q", u)
	}
	if !strings.Contains(note, "once") {
		t.Errorf("note %q", note)
	}
}

func TestSmallModelSkipsTools(t *testing.T) {
	useSearchStubs(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, ddgPage) }))
	c := &Controller{}
	var seen []InferenceSpec
	out, _, err := c.answerWithWeb(context.Background(), scriptedInfer([]*InferenceOutput{{Text: "ok"}}, nil, &seen), "qwen2.5-3b.gguf", hist("q"), 8192, 2048)
	if err != nil || out.Text != "ok" || len(seen) != 1 || len(seen[0].Tools) != 0 {
		t.Fatalf("%v %+v %+v", err, out, seen)
	}
}

func TestSearchFailureStillAnswers(t *testing.T) {
	useSearchStubs(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }))
	c := &Controller{dataDir: t.TempDir()}
	var seen []InferenceSpec
	out, note, err := c.answerWithWeb(context.Background(), scriptedInfer([]*InferenceOutput{{Text: "from memory"}}, nil, &seen), "qwen2.5-3b.gguf", hist("q"), 8192, 2048)
	if err != nil || out.Text != "from memory" || !strings.HasPrefix(note, "Web search failed") {
		t.Fatalf("%v %+v %q", err, out, note)
	}
}

func TestChatToolsOffByDefault(t *testing.T) {
	c := &Controller{}
	if c.chatToolsOn() {
		t.Fatal("must be off by default")
	}
	c.settings.ChatWebSearch, c.settings.ChatSearchWith = true, "local"
	if !c.chatToolsOn() {
		t.Fatal("not on")
	}
	if b, n := c.chatSearchBrief(context.Background(), "x", nil); b != "" || n != "" {
		t.Fatal("the cloud brief must not run in local mode")
	}
}
