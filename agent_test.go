package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// agentTask makes a task with a project folder holding the given files.
func agentTask(t *testing.T, files map[string]string) *Task {
	t.Helper()
	dir := t.TempDir()
	tk := &Task{ID: "t1", dir: dir, Round: 1, Planner: "claude", ClaudeModel: "claude-sonnet-5-5", CapUSD: 10, Title: "demo",
		Plan: &Plan{Title: "demo", Summary: "a demo", TestCommand: "go test ./..."}}
	for p, c := range files {
		full := filepath.Join(tk.repoDir(), filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return tk
}

func scripted(steps ...*llmResponse) (func(context.Context, llmRequest) (*llmResponse, error), *[]llmRequest) {
	var seen []llmRequest
	var mu sync.Mutex
	i := 0
	return func(ctx context.Context, req llmRequest) (*llmResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, req)
		if i >= len(steps) {
			return &llmResponse{Text: "done"}, nil
		}
		i++
		return steps[i-1], nil
	}, &seen
}

func call(id, name, args string) toolCall { return toolCall{ID: id, Name: name, Args: args} }

func TestAgentToolsReadSearchWrite(t *testing.T) {
	tk := agentTask(t, map[string]string{
		"main.go":       "package main\n\nfunc add(a, b int) int { return a - b }\n",
		"sub/util.go":   "package sub\n// TODO fix\n",
		"sub/binary.go": "x\x00y",
	})
	c := &Controller{}
	step, seen := scripted(
		&llmResponse{Calls: []toolCall{call("a", "list_files", `{}`), call("b", "read_file", `{"path":"main.go"}`)}},
		&llmResponse{Calls: []toolCall{
			call("c", "search", `{"pattern":"TODO|return a"}`),
			call("d", "read_file", `{"path":"main.go","start_line":3,"end_line":3}`),
			call("e", "write_file", `{"path":"main.go","content":"package main\n\nfunc add(a, b int) int { return a + b }"}`),
			call("f", "read_file", `{"path":"../etc/passwd"}`),
			call("g", "nope", `{}`),
			call("h", "run", `{"command":"go test ./..."}`),
			call("i", "read_file", `not json`),
		}},
		&llmResponse{Text: "add subtracted instead of adding; fixed."},
	)
	a := c.newAgent(tk, "", "Fix round 1", "sys", "go", "", []agentTool{toolListFiles, toolReadFile, toolSearch, toolWriteFile, toolRun, toolAskUser})
	a.step = step
	if err := a.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.Calls != 9 || a.Stopped != "" || !strings.Contains(a.Summary, "fixed") {
		t.Fatalf("calls=%d stopped=%q summary=%q", a.Calls, a.Stopped, a.Summary)
	}
	if got, _ := os.ReadFile(filepath.Join(tk.repoDir(), "main.go")); !strings.Contains(string(got), "a + b") || !strings.HasSuffix(string(got), "\n") {
		t.Fatalf("file not written: %q", got)
	}
	if len(a.Written) != 1 || a.Written[0] != "main.go" || len(tk.Touched) != 1 {
		t.Fatalf("written %v touched %v", a.Written, tk.Touched)
	}
	// The undo snapshot of the round exists, like any other write.
	if _, err := os.Stat(filepath.Join(tk.roundDir(1), "orig", "main.go")); err != nil {
		t.Fatalf("no snapshot: %v", err)
	}
	want := map[string]string{
		"a": "main.go (", "b": "return a - b", "c": "sub/util.go:2: // TODO fix", "d": "(lines 3-3 of 4)",
		"e": "wrote main.go", "f": "error:", "g": "error: no such tool", "h": "no node has exec", "i": "not a JSON object",
	}
	last := (*seen)[2].Msgs
	for _, m := range last {
		if m.Role == "tool" {
			if w, ok := want[m.CallID]; ok && !strings.Contains(m.Text, w) {
				t.Errorf("result %s = %q, want it to contain %q", m.CallID, m.Text, w)
			}
		}
	}
	if len(*seen) != 3 || len((*seen)[0].Tools) != 6 {
		t.Fatalf("requests %d", len(*seen))
	}
}

func TestAgentLimits(t *testing.T) {
	tk := agentTask(t, map[string]string{"a.txt": "x"})
	c := &Controller{}
	n := 0
	a := c.newAgent(tk, "", "Fix", "sys", "go", "", []agentTool{toolReadFile})
	a.step = func(ctx context.Context, req llmRequest) (*llmResponse, error) {
		if len(req.Tools) == 0 { // the wrap-up request
			return &llmResponse{Text: "ran out; found nothing"}, nil
		}
		n++
		return &llmResponse{Calls: []toolCall{call(fmt.Sprint("c", n), "read_file", `{"path":"a.txt"}`)}}, nil
	}
	if err := a.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.Calls != maxAgentCalls || n != maxAgentCalls || !strings.Contains(a.Stopped, "tool calls") || a.Summary != "ran out; found nothing" {
		t.Fatalf("calls=%d n=%d stopped=%q summary=%q", a.Calls, n, a.Stopped, a.Summary)
	}

	// The time limit counts active time.
	a = c.newAgent(tk, "", "Fix", "sys", "go", "", []agentTool{toolReadFile})
	a.active = maxAgentTime
	a.step = func(ctx context.Context, req llmRequest) (*llmResponse, error) {
		return &llmResponse{Text: "wrap"}, nil
	}
	if err := a.loop(context.Background()); err != nil || a.Calls != 0 || !strings.Contains(a.Stopped, "20 minutes") {
		t.Fatalf("err=%v stopped=%q", err, a.Stopped)
	}
}

func TestAgentToolsUnsupported(t *testing.T) {
	tk := agentTask(t, nil)
	c := &Controller{}
	a := c.newAgent(tk, "", "Fix", "sys", "go", "", []agentTool{toolReadFile})
	a.step = func(ctx context.Context, req llmRequest) (*llmResponse, error) {
		return nil, fmt.Errorf("llama-server 500 Internal Server Error: tools param requires --jinja flag")
	}
	if err := a.loop(context.Background()); err != errAgentUnsupported {
		t.Fatalf("got %v", err)
	}
	for _, s := range []string{"OpenRouter 404: No endpoints found that support tool use", "Claude API 400: tools are not supported"} {
		if !toolsUnsupported(fmt.Errorf("%s", s)) {
			t.Errorf("should be unsupported: %s", s)
		}
	}
	if toolsUnsupported(fmt.Errorf("Claude API 401: invalid x-api-key")) || toolsUnsupported(nil) {
		t.Error("an auth error is not an unsupported-tools error")
	}
}

func TestAgentAskUser(t *testing.T) {
	tk := agentTask(t, nil)
	tk.Subtasks = []*SubtaskState{{ID: "t1", Status: "running"}}
	c := &Controller{}
	step, seen := scripted(
		&llmResponse{Calls: []toolCall{call("q", "ask_user", `{"question":"Which database?","options":["SQLite","Postgres"]}`)}},
		&llmResponse{Text: "using SQLite"},
	)
	a := c.newAgent(tk, "", "Investigate", "sys", "go", "t1", []agentTool{toolAskUser})
	a.step = step
	done := make(chan error, 1)
	go func() { done <- a.loop(context.Background()) }()

	var id string
	deadline := time.Now().Add(5 * time.Second)
	for id == "" && time.Now().Before(deadline) {
		tk.mu.Lock()
		for _, m := range tk.Messages {
			if m.Kind == "agent_question" && m.Pending {
				id = m.ID
			}
		}
		waiting := tk.Subtasks[0].Waiting
		tk.mu.Unlock()
		if id != "" && !waiting {
			t.Fatal("subtask should be marked waiting")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if id == "" {
		t.Fatal("no question was posted")
	}
	if !tk.hasPendingAsk() || !tk.summaryLocked().NeedsYou {
		t.Fatal("a pending question should need the user")
	}
	select {
	case <-done:
		t.Fatal("the agent must wait for the answer")
	case <-time.After(50 * time.Millisecond):
	}
	if err := tk.deliverAnswer("nope", "x"); err == nil {
		t.Fatal("unknown question accepted")
	}
	if err := tk.deliverAnswer(id, "SQLite"); err != nil {
		t.Fatal(err)
	}
	if err := tk.deliverAnswer(id, "again"); err == nil {
		t.Fatal("a question can be answered once")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if a.Summary != "using SQLite" || tk.hasPendingAsk() || tk.Subtasks[0].Waiting {
		t.Fatalf("summary=%q pending=%v waiting=%v", a.Summary, tk.hasPendingAsk(), tk.Subtasks[0].Waiting)
	}
	res := (*seen)[1].Msgs[2]
	if res.Role != "tool" || !strings.Contains(res.Text, "SQLite") {
		t.Fatalf("answer not passed back: %+v", res)
	}

	// Cancelling while waiting ends the agent.
	ctx, cancel := context.WithCancel(context.Background())
	step2, _ := scripted(&llmResponse{Calls: []toolCall{call("q", "ask_user", `{"question":"x?"}`)}})
	b := c.newAgent(tk, "", "Investigate", "sys", "go", "", []agentTool{toolAskUser})
	b.step = step2
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if err := b.loop(ctx); err != errCancelled {
		t.Fatalf("got %v", err)
	}
}

func TestCompactMsgs(t *testing.T) {
	big := strings.Repeat("x", 5000)
	msgs := []agentMsg{{Role: "user", Text: "task"}}
	for i := 0; i < 5; i++ {
		msgs = append(msgs, agentMsg{Role: "assistant", Calls: []toolCall{call(fmt.Sprint(i), "read_file", `{}`)}}, agentMsg{Role: "tool", CallID: fmt.Sprint(i), Text: big})
	}
	compactMsgs(msgs, 12000)
	if msgs[2].Text == big || msgs[len(msgs)-1].Text != big {
		t.Fatalf("oldest output should be dropped and the newest kept: %q", msgs[2].Text)
	}
	if msgs[0].Text != "task" {
		t.Fatal("the task prompt must stay")
	}
}

func TestPlanInvestigateSubtask(t *testing.T) {
	p := &Plan{Subtasks: []PlanSubtask{
		{ID: "i1", Title: "find it", Kind: " Investigate ", Instructions: "dig"},
		{ID: "t1", Title: "fix", Files: []string{"a.go"}, DependsOn: []string{"i1"}, Kind: "weird"},
	}}
	if err := validatePlan(p); err != nil {
		t.Fatal(err)
	}
	if p.Subtasks[0].Kind != "investigate" || p.Subtasks[1].Kind != "" {
		t.Fatalf("kinds %q %q", p.Subtasks[0].Kind, p.Subtasks[1].Kind)
	}
	bad := &Plan{Subtasks: []PlanSubtask{{ID: "t1", Title: "x"}}}
	if validatePlan(bad) == nil {
		t.Fatal("a write subtask without files must be refused")
	}
}

func TestDependencyReports(t *testing.T) {
	tk := agentTask(t, nil)
	tk.Subtasks = []*SubtaskState{{ID: "i1", Report: "the bug is in parse()"}, {ID: "t2"}}
	c := &Controller{}
	got := c.dependencyReports(tk, PlanSubtask{DependsOn: []string{"i1", "t2"}})
	if !strings.Contains(got, "the bug is in parse()") || strings.Contains(got, "t2") {
		t.Fatalf("got %q", got)
	}
	if c.dependencyReports(tk, PlanSubtask{DependsOn: []string{"t2"}}) != "" {
		t.Fatal("no report, no text")
	}
}

// ---------- backends ----------

func TestClaudeToolUse(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		w.Write([]byte(`{"content":[{"type":"text","text":"looking"},{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"a.go"}},{"type":"tool_use","id":"toolu_2","name":"search","input":{"pattern":"x"}}],"usage":{"input_tokens":100,"output_tokens":20}}`))
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_ANTHROPIC_URL", srv.URL)
	resp, err := callClaude(context.Background(), "k", "claude-sonnet-5-5", llmRequest{
		System: "s", MaxTokens: 100, Tools: []agentTool{toolReadFile, toolSearch},
		Msgs: []agentMsg{
			{Role: "user", Text: "go"},
			{Role: "assistant", Text: "ok", Calls: []toolCall{call("toolu_0", "list_files", `{}`), call("toolu_9", "search", `{"pattern":"y"}`)}},
			{Role: "tool", CallID: "toolu_0", Text: "a.go"},
			{Role: "tool", CallID: "toolu_9", Text: "no matches"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Calls) != 2 || resp.Calls[0].ID != "toolu_1" || resp.Calls[0].Name != "read_file" || !strings.Contains(resp.Calls[0].Args, "a.go") || resp.Text != "looking" {
		t.Fatalf("parsed %+v", resp)
	}
	b := bodies[0]
	tools := b["tools"].([]any)
	if len(tools) != 2 || tools[0].(map[string]any)["input_schema"] == nil {
		t.Fatalf("tools %v", tools)
	}
	msgs := b["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("want user, assistant, one user turn with both results; got %d messages", len(msgs))
	}
	results := msgs[2].(map[string]any)["content"].([]any)
	if len(results) != 2 || results[0].(map[string]any)["type"] != "tool_result" || results[1].(map[string]any)["tool_use_id"] != "toolu_9" {
		t.Fatalf("results %v", results)
	}
	if results[1].(map[string]any)["cache_control"] == nil {
		t.Error("the last block should be cached")
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	if len(asst) != 3 || asst[1].(map[string]any)["type"] != "tool_use" || asst[1].(map[string]any)["input"] == nil {
		t.Fatalf("assistant blocks %v", asst)
	}
}

func TestOpenRouterToolUse(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"model":"v/m","choices":[{"message":{"content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.001}}`))
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_OPENROUTER_URL", srv.URL)
	resp, err := callOpenRouter(context.Background(), "k", "v/m", llmRequest{System: "s", MaxTokens: 100, Tools: []agentTool{toolReadFile},
		Msgs: []agentMsg{{Role: "user", Text: "go"}, {Role: "assistant", Calls: []toolCall{call("c0", "list_files", `{}`)}}, {Role: "tool", CallID: "c0", Text: "a.go"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Calls) != 1 || resp.Calls[0].ID != "c1" || resp.Calls[0].Args != `{"path":"a.go"}` {
		t.Fatalf("calls %+v", resp.Calls)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 4 || msgs[2].(map[string]any)["tool_calls"] == nil || msgs[3].(map[string]any)["tool_call_id"] != "c0" {
		t.Fatalf("messages %v", msgs)
	}
	if body["tools"].([]any)[0].(map[string]any)["type"] != "function" {
		t.Fatalf("tools %v", body["tools"])
	}
}

// fakeLlamaServer streams tool calls the way llama-server does: the name and
// id first, the arguments in pieces.
func fakeLlamaServer(t *testing.T, got *map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(got)
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(s string) { fmt.Fprintf(w, "data: %s\n\n", s) }
		send(`{"choices":[{"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`)
		send(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`)
		send(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`)
		send(`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"search","arguments":"{\"pattern\":\"x\"}"}}]}}]}`)
		send(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":50,"completion_tokens":12}}`)
		send(`[DONE]`)
	}))
}

func TestLlamaChatToolCalls(t *testing.T) {
	var got map[string]any
	srv := fakeLlamaServer(t, &got)
	defer srv.Close()
	var port int
	fmt.Sscanf(srv.URL, "http://127.0.0.1:%d", &port)
	l := newLlamaMgr(nil)
	l.cmd, l.ready, l.port, l.model = &exec.Cmd{}, true, port, "m"

	spec := InferenceSpec{
		Messages: []ChatMessage{{Role: "system", Content: "s"}, {Role: "user", Content: "go"},
			{Role: "assistant", ToolCalls: wireCalls([]toolCall{call("call_0", "list_files", `{}`)})}, {Role: "tool", ToolCallID: "call_0", Content: "a.go"}},
		Tools: toolDefs([]agentTool{toolReadFile, toolSearch}),
	}
	out, e := l.chat(context.Background(), spec, func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	calls := fromWireCalls(out.ToolCalls)
	if len(calls) != 2 || calls[0].ID != "call_a" || calls[0].Name != "read_file" || calls[0].Args != `{"path":"a.go"}` || calls[1].Name != "search" {
		t.Fatalf("calls %+v", calls)
	}
	if out.FinishReason != "tool_calls" || out.OutputTokens != 12 || out.Text != "" {
		t.Fatalf("out %+v", out)
	}
	if tools, _ := got["tools"].([]any); len(tools) != 2 {
		t.Fatalf("tools not forwarded: %v", got["tools"])
	}
	msgs := got["messages"].([]any)
	if msgs[2].(map[string]any)["tool_calls"] == nil || msgs[3].(map[string]any)["tool_call_id"] != "call_0" {
		t.Fatalf("messages %v", msgs)
	}

	// A plain request carries no tools and returns no calls.
	got = nil
	if _, e := l.chat(context.Background(), InferenceSpec{Messages: []ChatMessage{{Role: "user", Content: "hi"}}}, func(string) {}); e != nil {
		t.Fatal(e)
	}
	if _, has := got["tools"]; has {
		t.Fatal("tools sent on a plain request")
	}
}

func TestJinjaFlag(t *testing.T) {
	if hasJinjaArg([]string{"-ngl", "99"}) || !hasJinjaArg([]string{"--jinja"}) || !hasJinjaArg([]string{"--no-jinja"}) {
		t.Fatal("hasJinjaArg")
	}
}

// ---------- the fixer end to end against a stub Claude ----------

func TestAgentFixWithClaude(t *testing.T) {
	var mu sync.Mutex
	var reqs []map[string]any
	replies := []string{
		`{"content":[{"type":"tool_use","id":"u1","name":"read_file","input":{"path":"calc.go"}}],"usage":{"input_tokens":1000,"output_tokens":50}}`,
		`{"content":[{"type":"tool_use","id":"u2","name":"write_file","input":{"path":"calc.go","content":"package calc\n\nfunc Add(a, b int) int { return a + b }\n"}}],"usage":{"input_tokens":1200,"output_tokens":60}}`,
		`{"content":[{"type":"text","text":"Add subtracted; now it adds."}],"usage":{"input_tokens":1300,"output_tokens":20}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		defer mu.Unlock()
		reqs = append(reqs, b)
		i := len(reqs) - 1
		if i >= len(replies) {
			i = len(replies) - 1
		}
		w.Write([]byte(replies[i]))
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_ANTHROPIC_URL", srv.URL)

	c := &Controller{dataDir: t.TempDir()}
	enc, err := c.encrypt("sk-test")
	if err != nil {
		t.Fatal(err)
	}
	c.settings = defaultSettings()
	c.settings.AnthropicKeyEnc = enc
	tk := agentTask(t, map[string]string{"calc.go": "package calc\n\nfunc Add(a, b int) int { return a - b }\n"})
	changed, err := c.agentFix(context.Background(), tk, tk.Plan, "go test ./...", "--- FAIL: TestAdd\n    got -1 want 5", []string{"round 1 changed calc.go"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != "calc.go" {
		t.Fatalf("changed %v", changed)
	}
	if got, _ := os.ReadFile(filepath.Join(tk.repoDir(), "calc.go")); !strings.Contains(string(got), "a + b") {
		t.Fatalf("not fixed: %s", got)
	}
	if len(reqs) != 3 || reqs[0]["tools"] == nil {
		t.Fatalf("requests %d", len(reqs))
	}
	first := reqs[0]["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	for _, want := range []string{"go test ./...", "got -1 want 5", "round 1 changed calc.go", "calc.go ("} {
		if !strings.Contains(first, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if tk.ClaudeCalls != 3 || tk.CostUSD <= 0 {
		t.Fatalf("cost not counted: calls=%d cost=%v", tk.ClaudeCalls, tk.CostUSD)
	}

	// The same stub that rejects tools makes agentFix report it, so the caller can fall back.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":{"message":"tools are not supported by this model"}}`))
	}))
	defer bad.Close()
	t.Setenv("OUTPOST_ANTHROPIC_URL", bad.URL)
	if _, err := c.agentFix(context.Background(), tk, tk.Plan, "go test", "fail", nil, 0); err != errAgentUnsupported {
		t.Fatalf("got %v", err)
	}
}

func TestAgentFixFileBlocksFallback(t *testing.T) {
	// A model that ignores the tools and answers with file blocks still fixes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"content":[{"type":"text","text":"<<<FILE calc.go>>>\npackage calc\n<<<END>>>"}],"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_ANTHROPIC_URL", srv.URL)
	c := &Controller{dataDir: t.TempDir()}
	enc, _ := c.encrypt("sk-test")
	c.settings = defaultSettings()
	c.settings.AnthropicKeyEnc = enc
	tk := agentTask(t, map[string]string{"calc.go": "old"})
	changed, err := c.agentFix(context.Background(), tk, tk.Plan, "go test", "fail", nil, 0)
	if err != nil || len(changed) != 1 {
		t.Fatalf("changed %v err %v", changed, err)
	}
	if got, _ := os.ReadFile(filepath.Join(tk.repoDir(), "calc.go")); !strings.HasPrefix(string(got), "package calc") {
		t.Fatalf("got %q", got)
	}
}

// Cloud planners stream (the live view), so tool calls must survive that path too.
func TestStreamedToolCalls(t *testing.T) {
	claude := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":40}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me look."}}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_a","name":"read_file","input":{}}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\": \"a."}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"go\"}"}}`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_b","name":"list_files","input":{}}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":33}}`,
		`data: {"type":"message_stop"}`, ``,
	}, "\n\n")
	r, err := readClaudeStream(strings.NewReader(claude), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "Let me look." || len(r.Calls) != 2 || r.Calls[0].ID != "toolu_a" || r.Calls[0].Args != `{"path": "a.go"}` || r.Calls[1].Name != "list_files" || r.Calls[1].Args != "{}" {
		t.Fatalf("claude stream: %+v", r)
	}
	if r.InTokens != 100 || r.OutTokens != 33 || r.CacheRead != 40 {
		t.Fatalf("usage %+v", r)
	}

	or := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","content":"","tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"search","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pattern\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":4}}`,
		`data: [DONE]`, ``,
	}, "\n\n")
	var out struct {
		Choices []struct {
			Message struct {
				ToolCalls []WireToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(readOpenRouterStream(strings.NewReader(or), nil), &out); err != nil {
		t.Fatal(err)
	}
	calls := fromWireCalls(out.Choices[0].Message.ToolCalls)
	if len(calls) != 1 || calls[0].ID != "c1" || calls[0].Name != "search" || calls[0].Args != `{"pattern":"x"}` {
		t.Fatalf("openrouter stream: %+v", calls)
	}
}
