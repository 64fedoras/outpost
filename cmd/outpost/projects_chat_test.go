package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSplitTaskBlocks(t *testing.T) {
	text := "Sure, two tasks.\n\n[start task]\nAdd --top N to the CLI.\nTest it.\n[/start task]\n\n[START TASK] Write a README [/start task]\n\n[start task]  [/start task]\nDone."
	reply, asks := splitTaskBlocks(text)
	if len(asks) != 2 || asks[0] != "Add --top N to the CLI.\nTest it." || asks[1] != "Write a README" {
		t.Fatalf("asks: %q", asks)
	}
	if strings.Contains(reply, "start task") || !strings.HasPrefix(reply, "Sure, two tasks.") || !strings.HasSuffix(reply, "Done.") {
		t.Fatalf("reply: %q", reply)
	}
	many := strings.Repeat("[start task]x[/start task]", 5)
	if _, asks := splitTaskBlocks(many); len(asks) != maxChatTasks {
		t.Fatalf("not capped: %d", len(asks))
	}
}

func TestProjectChatTranscript(t *testing.T) {
	msgs := []ProjectChatMsg{
		{Role: "user", Text: strings.Repeat("old ", 100)},
		{Role: "assistant", Text: "ok", Tasks: []string{"task_1"}},
		{Role: "assistant", Error: "stopped"},
		{Role: "user", Text: "what next?"},
	}
	full := projectChatTranscript(msgs, 10000)
	if !strings.Contains(full, "Owner: old") || !strings.Contains(full, "You: ok\n(started task task_1)") || !strings.HasSuffix(full, "Owner: what next?") {
		t.Fatalf("transcript: %s", full)
	}
	short := projectChatTranscript(msgs, 60)
	if strings.Contains(short, "old") || !strings.HasSuffix(short, "Owner: what next?") {
		t.Fatalf("budget not kept: %s", short)
	}
}

// A message is kept, the answer reports a planner problem instead of failing
// silently, and the log survives a restart (a fresh cache).
func TestProjectChatSendAndPersist(t *testing.T) {
	c := testController(t)
	c.settings.DefaultPlanner = "claude" // no key set: the answer says so
	w := httptest.NewRecorder()
	c.handleCreateProject(w, httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"chat"}`)))
	var created struct{ ID string }
	json.Unmarshal(w.Body.Bytes(), &created)
	p := c.projects[created.ID]
	send := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/projects/"+p.ID+"/chat", strings.NewReader(body))
		r.SetPathValue("id", p.ID)
		c.handleProjectChat(w, r)
		return w
	}
	if w := send(`{"text":"  "}`); w.Code != 400 {
		t.Fatalf("empty message: %d", w.Code)
	}
	if w := send(`{"text":"hello?"}`); w.Code != 200 {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	ch := c.projectConvChat(p, false) // made by the first message; the old chat.json is not used
	if ch == nil || ch.ProjectID != p.ID || p.ConvChatID != ch.ID {
		t.Fatalf("no conversation chat: %+v", ch)
	}
	deadline := time.Now().Add(3 * time.Second)
	for ch.projectView().Busy {
		if time.Now().After(deadline) {
			t.Fatal("answer never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	v := ch.projectView()
	if len(v.Messages) != 2 || v.Messages[0].Text != "hello?" || !strings.Contains(v.Messages[1].Error, "Claude API key") {
		t.Fatalf("messages: %+v", v.Messages)
	}
	// The log survives a restart: a fresh controller reads the chat and the project's link to it.
	c2 := &Controller{dataDir: c.dataDir, projects: map[string]*Project{}}
	c2.loadProjects()
	c2.loadChats()
	if p2 := c2.projects[p.ID]; p2 == nil || p2.ConvChatID != ch.ID || len(c2.projectChatViewFor(p2).Messages) != 2 {
		t.Fatalf("not saved: %+v", c2.projects[p.ID])
	}
	if _, err := os.Stat(filepath.Join(c.chatsDir(), ch.ID+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "chat.json")); err == nil {
		t.Fatal("chat.json must not be written any more")
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/api/projects/"+p.ID+"/chat", nil)
	r.SetPathValue("id", p.ID)
	c.handleProjectChatClear(w, r)
	if w.Code != 200 || len(c.projectChatViewFor(p).Messages) != 0 {
		t.Fatalf("clear: %d", w.Code)
	}
}

func TestProjectChatSystemPrompt(t *testing.T) {
	c := testController(t)
	p := &Project{ID: "proj_s", Name: "dice", Instructions: "Use pytest.", TestCommand: "pytest -q", slot: make(chan struct{}, 1)}
	p.dir = filepath.Join(c.projectsDir(), p.ID)
	os.MkdirAll(p.repoDir(), 0o755)
	os.WriteFile(filepath.Join(p.repoDir(), "roll.py"), []byte("x"), 0o644)
	c.projects[p.ID] = p
	c.tasks["task_s"] = &Task{ID: "task_s", Title: "Add roll", ProjectID: p.ID, Status: "awaiting_answers", Created: time.Now(), dir: t.TempDir(),
		Messages: []TaskMessage{{Role: "user", Kind: "text", Text: "add a roll function"}}}
	s := c.projectChatSystem(p)
	for _, want := range []string{`"dice"`, "[start task]", "Use pytest.", "pytest -q", "Add roll (task_s) [awaiting_answers, waiting on the owner]: add a roll function", "roll.py"} {
		if !strings.Contains(s, want) {
			t.Errorf("system prompt lacks %q:\n%s", want, s)
		}
	}
}

func TestProjectReadTools(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, "src"), 0o755)
	os.WriteFile(filepath.Join(repo, "src", "app.go"), []byte("package app\n\nfunc Total(a, b int) int {\n\treturn a + b\n}\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "secret.env"), []byte("KEY=x\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".outpostignore"), []byte("*.env\n"), 0o644)
	pr := newProjectReads(repo)
	call := func(name, args string) string { return pr.run(toolCall{ID: "1", Name: name, Args: args}, 5000) }
	if out := call("list_files", `{}`); !strings.Contains(out, "src/app.go") || strings.Contains(out, "secret.env") {
		t.Fatalf("list: %s", out)
	}
	if out := call("read_file", `{"path":"./src/app.go","start_line":3,"end_line":4}`); !strings.Contains(out, "lines 3-4 of") || !strings.Contains(out, "func Total") {
		t.Fatalf("read: %s", out)
	}
	if out := call("read_file", `{"path":"secret.env"}`); !strings.Contains(out, "outpostignore") {
		t.Fatalf("ignored file was read: %s", out)
	}
	if out := call("read_file", `{"path":"../../etc/passwd"}`); !strings.HasPrefix(out, "error") {
		t.Fatalf("escape: %s", out)
	}
	if out := call("search", `{"pattern":"KEY|Total"}`); !strings.Contains(out, "src/app.go:3:") || strings.Contains(out, "secret") {
		t.Fatalf("search: %s", out)
	}
	if out := call("outline", `{"path":"src"}`); !strings.Contains(out, "func Total") {
		t.Fatalf("outline: %s", out)
	}
	if out := call("write_file", `{"path":"x","content":"y"}`); !strings.Contains(out, "no such tool") {
		t.Fatalf("write must not exist: %s", out)
	}
	want := []string{"Listed the project", "Read src/app.go 3-4", "Read etc/passwd", `Searched "KEY|Total"`, "Outline of src"}
	if strings.Join(pr.Notes, ",") != strings.Join(want, ",") {
		t.Fatalf("notes %q", pr.Notes)
	}
	// Fallback for models without tools: files the message names, by path or base name.
	files, names := namedFiles(repo, "why does app.go add? also see secret.env", 10000)
	if len(names) != 1 || names[0] != "src/app.go" || !strings.Contains(files, "func Total") {
		t.Fatalf("named %v %s", names, files)
	}
}

func TestProjectToolLoop(t *testing.T) {
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "a.py"), []byte("def roll():\n    return 4\n"), 0o644)
	var reqs []llmRequest
	step := func(ctx context.Context, req llmRequest) (*llmResponse, error) {
		reqs = append(reqs, req)
		if len(reqs) == 1 {
			return &llmResponse{Calls: []toolCall{{ID: "c1", Name: "read_file", Args: `{"path":"a.py"}`}}}, nil
		}
		return &llmResponse{Text: "roll() returns 4 (a.py:2)"}, nil
	}
	pr := newProjectReads(repo)
	var seen []string
	resp, err := projectToolLoop(context.Background(), step, "sys", "Owner: what does roll do?", 500, 4000, 100000, pr, func(n []string) { seen = n })
	if err != nil || resp.Text != "roll() returns 4 (a.py:2)" || len(seen) != 1 {
		t.Fatalf("resp %+v err %v seen %v", resp, err, seen)
	}
	last := reqs[1].Msgs[len(reqs[1].Msgs)-1]
	if last.Role != "tool" || last.CallID != "c1" || !strings.Contains(last.Text, "return 4") {
		t.Fatalf("tool result %+v", last)
	}
	// A model that keeps calling tools is made to answer.
	reqs = nil
	loop := func(ctx context.Context, req llmRequest) (*llmResponse, error) {
		reqs = append(reqs, req)
		return &llmResponse{Text: "partial", Calls: []toolCall{{ID: "x", Name: "list_files", Args: `{}`}}}, nil
	}
	resp, err = projectToolLoop(context.Background(), loop, "sys", "u", 500, 4000, 100000, newProjectReads(repo), nil)
	if err != nil || resp.Text != "partial" || len(reqs) != maxProjectToolRounds+1 {
		t.Fatalf("rounds %d resp %+v err %v", len(reqs), resp, err)
	}
	// A server without tool support: the caller falls back.
	_, err = projectToolLoop(context.Background(), func(ctx context.Context, req llmRequest) (*llmResponse, error) {
		return nil, errors.New("tools param requires --jinja flag")
	}, "s", "u", 500, 4000, 100000, newProjectReads(repo), nil)
	if !errors.Is(err, errAgentUnsupported) {
		t.Fatalf("unsupported: %v", err)
	}
}
