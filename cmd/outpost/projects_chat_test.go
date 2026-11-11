package main

import (
	"encoding/json"
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
	pc := p.chatLog()
	deadline := time.Now().Add(3 * time.Second)
	for pc.view().Busy {
		if time.Now().After(deadline) {
			t.Fatal("answer never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	v := pc.view()
	if len(v.Messages) != 2 || v.Messages[0].Text != "hello?" || !strings.Contains(v.Messages[1].Error, "Claude API key") {
		t.Fatalf("messages: %+v", v.Messages)
	}
	projectChats.Delete(pc.path)
	if got := p.chatLog().view(); len(got.Messages) != 2 {
		t.Fatalf("not saved: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "chat.json")); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/api/projects/"+p.ID+"/chat", nil)
	r.SetPathValue("id", p.ID)
	c.handleProjectChatClear(w, r)
	if w.Code != 200 || len(p.chatLog().view().Messages) != 0 {
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
