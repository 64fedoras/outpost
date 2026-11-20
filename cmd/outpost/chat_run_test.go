package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The model calls run_code, sees the output and a returned file, and answers;
// the run is kept with its file, and later turns see a summary of it.
func TestChatRunCodeLoop(t *testing.T) {
	c := chatController(t)
	ch := &Chat{ID: "chat_r", RunCode: true, dir: c.chatsDir(), Messages: []ChatMsg{{ID: "m1", Role: "user", Text: "sum 1..100"}}}
	run := c.newCodeRunner(ch, nil)
	if run == nil {
		t.Fatal("run code on, but no runner")
	}
	var gotCode string
	run.stub = func(lang, code string) (*ExecOutput, map[string][]byte, error) {
		gotCode = code
		return &ExecOutput{ExitCode: 0, StdoutTail: "5050\n"}, map[string][]byte{"chart.svg": []byte("<svg/>")}, nil
	}
	tool, ok := run.tool()
	if !ok || !strings.Contains(tool.Description, "python") {
		t.Fatalf("tool = %+v ok=%v", tool, ok)
	}
	var seen []InferenceSpec
	infer := scriptedInfer([]*InferenceOutput{
		{ToolCalls: []WireToolCall{toolCallOf("c1", "run_code", `{"language":"python","code":"print(sum(range(101)))"}`)}},
		{Text: "The sum is 5050."},
	}, nil, &seen)
	out, _, ran, err := c.answerWithRun(context.Background(), infer, "qwen3-8b", hist("sum 1..100"), 8192, 1024, run)
	if err != nil || !ran || out.Text != "The sum is 5050." {
		t.Fatalf("out=%+v ran=%v err=%v", out, ran, err)
	}
	if gotCode != "print(sum(range(101)))" {
		t.Fatalf("code = %q", gotCode)
	}
	if len(seen) != 2 || !strings.Contains(seen[0].Messages[0].Content, "run_code") {
		t.Fatalf("first request lacks the run_code system note: %+v", seen)
	}
	last := seen[1].Messages[len(seen[1].Messages)-1]
	if last.Role != "tool" || !strings.Contains(last.Content, "5050") || !strings.Contains(last.Content, "out/chart.svg") {
		t.Fatalf("tool result = %+v", last)
	}
	runs := run.done()
	if len(runs) != 1 || runs[0].Node != "stub" || len(runs[0].Files) != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	if b, err := os.ReadFile(filepath.Join(ch.runsDir(), runs[0].Files[0].File)); err != nil || string(b) != "<svg/>" {
		t.Fatalf("file %v %q", err, b)
	}
	h := chatHistory([]ChatMsg{{Role: "user", Text: "q"}, {Role: "assistant", Text: "a", Runs: runs}})
	if !strings.Contains(h[2].Content, "python on stub: exit 0, output: 5050") || !strings.Contains(h[2].Content, "wrote out/chart.svg") {
		t.Fatalf("history = %q", h[2].Content)
	}

	// The file is served with a sandbox policy; a path outside the folder is not.
	c.chats[ch.ID] = ch
	m := http.NewServeMux()
	m.HandleFunc("GET /api/chats/{id}/run-file", c.handleChatRunFile)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/api/chats/chat_r/run-file?file="+runs[0].Files[0].File, nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(rec.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("serve: %d %v", rec.Code, rec.Header())
	}
	rec = httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/api/chats/chat_r/run-file?file=../chat_r.json", nil))
	if rec.Code != 404 {
		t.Fatalf("traversal: %d", rec.Code)
	}
}

func TestChatRunCodeLimits(t *testing.T) {
	c := chatController(t)
	// Off: no runner. On with no exec node: the answer goes ahead with a note.
	if c.newCodeRunner(&Chat{ID: "x", dir: c.chatsDir()}, nil) != nil {
		t.Fatal("runner for a chat with run code off")
	}
	run := c.newCodeRunner(&Chat{ID: "y", RunCode: true, dir: c.chatsDir()}, nil)
	_, note, ran, err := c.answerWithRun(context.Background(), nil, "qwen3-8b", hist("q"), 8192, 1024, run)
	if ran || err != nil || !strings.Contains(note, "no online node") {
		t.Fatalf("no exec node: ran=%v note=%q err=%v", ran, note, err)
	}
	run.stub = func(lang, code string) (*ExecOutput, map[string][]byte, error) {
		return &ExecOutput{ExitCode: 1, StderrTail: "boom"}, nil, nil
	}
	if _, note, ran, _ := c.answerWithRun(context.Background(), nil, "llama-3.2-1b", hist("q"), 8192, 1024, run); ran || !strings.Contains(note, "too small") {
		t.Fatalf("small model: ran=%v note=%q", ran, note)
	}
	if s := run.run(context.Background(), `{"language":"cobol","code":"x"}`); !strings.Contains(s, "must be one of") {
		t.Fatal(s)
	}
	if s := run.run(context.Background(), `{"language":"bash","code":"exit 1"}`); !strings.Contains(s, "No node") {
		t.Fatal(s) // sh is not offered by the stub
	}
	for i := 0; i < maxRunsPerTurn; i++ {
		if s := run.run(context.Background(), `{"language":"python3","code":"raise SystemExit(1)"}`); i == 0 && !strings.Contains(s, "Exit code 1") {
			t.Fatal(s)
		}
	}
	if s := run.run(context.Background(), `{"language":"python","code":"1"}`); !strings.Contains(s, "at most") {
		t.Fatal(s)
	}
}

// The chat's documents go along as files/ inputs.
func TestChatRunCodeDocs(t *testing.T) {
	c := chatController(t)
	ch := &Chat{ID: "chat_d", RunCode: true, dir: c.chatsDir()}
	ch.mu.Lock()
	ch.Messages = []ChatMsg{{ID: "m1", Role: "user", Text: "x", Docs: ch.saveDocs("m1", []chatDoc{{DocInfo{Name: "sales data.csv", Kind: "csv"}, "a,b\n1,2\n"}, {DocInfo{Name: "spec.pdf", Kind: "pdf"}, "[page 1]\nhello"}})}}
	ch.mu.Unlock()
	run := c.newCodeRunner(ch, nil)
	if strings.Join(run.names, " ") != "files/sales_data.csv files/spec.pdf.txt" || len(run.inputs) != 2 {
		t.Fatalf("names=%v inputs=%d", run.names, len(run.inputs))
	}
	run.stub = func(string, string) (*ExecOutput, map[string][]byte, error) { return &ExecOutput{}, nil, nil }
	if tool, _ := run.tool(); !strings.Contains(tool.Description, "files/sales_data.csv") {
		t.Fatal(tool.Description)
	}
}
