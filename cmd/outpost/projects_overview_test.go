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

// The resolved mark holds only while nothing happens to the task afterwards.
func TestTaskResolvedMark(t *testing.T) {
	c := testController(t)
	ended := time.Now().Add(-time.Hour)
	tk := &Task{ID: "task_r", Title: "r", Status: "done", Created: ended.Add(-time.Minute), Ended: &ended, dir: t.TempDir(),
		Messages: []TaskMessage{{Role: "user", Kind: "text", Text: "do it", Time: ended.Add(-time.Minute)}}}
	c.tasks[tk.ID] = tk
	meta := func(body string) taskSummary {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/tasks/task_r/meta", strings.NewReader(body))
		r.SetPathValue("id", "task_r")
		c.handleTaskMeta(w, r)
		if w.Code != 200 {
			t.Fatalf("meta %s: %d %s", body, w.Code, w.Body)
		}
		var s taskSummary
		json.Unmarshal(w.Body.Bytes(), &s)
		return s
	}
	if s := meta(`{"resolved":true}`); !s.Resolved {
		t.Fatal("not resolved after marking")
	}
	if b, _ := os.ReadFile(filepath.Join(tk.dir, "task.json")); !strings.Contains(string(b), "resolved_at") {
		t.Fatal("resolved mark not saved")
	}
	// A follow-up round (a newer message) brings it back without a write.
	tk.mu.Lock()
	tk.Messages = append(tk.Messages, TaskMessage{Role: "user", Kind: "text", Text: "more", Time: time.Now().Add(time.Second)})
	s := tk.summaryLocked()
	tk.mu.Unlock()
	if s.Resolved {
		t.Fatal("a follow-up did not clear the resolved mark")
	}
	// Marked again, then working: not resolved while it runs.
	meta(`{"resolved":true}`)
	tk.mu.Lock()
	tk.Messages = tk.Messages[:1]
	tk.Status = "running"
	s = tk.summaryLocked()
	tk.Status = "awaiting_answers"
	s2 := tk.summaryLocked()
	tk.Status = "done"
	s3 := tk.summaryLocked()
	tk.mu.Unlock()
	if s.Resolved || s2.Resolved || !s3.Resolved {
		t.Fatalf("running %v, waiting %v, done again %v", s.Resolved, s2.Resolved, s3.Resolved)
	}
	if s := meta(`{"resolved":false}`); s.Resolved {
		t.Fatal("reopen did not clear the mark")
	}
}

func TestProjectOverview(t *testing.T) {
	c := testController(t)
	w := httptest.NewRecorder()
	c.handleCreateProject(w, httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"ov"}`)))
	var created struct{ ID string }
	json.Unmarshal(w.Body.Bytes(), &created)
	p := c.projects[created.ID]
	if p == nil {
		t.Fatalf("create: %s", w.Body)
	}
	os.WriteFile(filepath.Join(p.repoDir(), "main.go"), []byte("package main\n"), 0o644)
	now := time.Now()
	older := now.Add(-time.Hour)
	c.tasks["task_a"] = &Task{ID: "task_a", Title: "first", ProjectID: p.ID, Status: "done", Created: older, Ended: &now, ResultBytes: 99, dir: t.TempDir(),
		Artifacts: []Artifact{{Path: "bin/app", Bytes: 10}},
		Subtasks:  []*SubtaskState{{Status: "done"}},
		Messages: []TaskMessage{{Role: "user", Kind: "text", Text: "build it", Time: older, Round: 1},
			{Role: "assistant", Kind: "shots", Time: now, Shots: []Shot{{Page: "index.html", Desktop: "index-desktop.png"}}},
			{Role: "assistant", Kind: "result", Text: "Built it.", Time: now}}}
	c.tasks["task_b"] = &Task{ID: "task_b", Prompt: "second\nmore", ProjectID: p.ID, Status: "running", Created: now, dir: t.TempDir(),
		Subtasks: []*SubtaskState{{Status: "done"}, {Status: "running"}}}
	c.tasks["task_other"] = &Task{ID: "task_other", Status: "done", Created: now, dir: t.TempDir()}

	w = httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/projects/"+p.ID+"/overview", nil)
	r.SetPathValue("id", p.ID)
	c.handleProjectOverview(w, r)
	if w.Code != 200 {
		t.Fatalf("overview: %d %s", w.Code, w.Body)
	}
	var ov projectOverview
	if err := json.Unmarshal(w.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	if len(ov.Feed) != 2 || ov.Feed[0].ID != "task_a" || ov.Feed[1].ID != "task_b" {
		t.Fatalf("feed: %+v", ov.Feed)
	}
	if ov.Feed[0].Requests[0].Text != "build it" || ov.Feed[0].Result != "Built it." || ov.Feed[1].Requests[0].Text != "second\nmore" {
		t.Fatalf("feed text: %+v", ov.Feed)
	}
	if got := strings.Join(ov.Feed[1].Subtasks, ","); got != "done,running" {
		t.Fatalf("subtasks: %s", got)
	}
	kinds := map[string]string{}
	for _, a := range ov.Artifacts {
		kinds[a.Kind] = a.URL
	}
	if !strings.Contains(kinds["shot"], "/api/tasks/task_a/shot?name=index-desktop.png") || !strings.Contains(kinds["build"], "path=bin%2Fapp") || kinds["result"] != "/api/tasks/task_a/download" {
		t.Fatalf("artifacts: %+v", ov.Artifacts)
	}
	if ov.FilesTotal != 1 || ov.Files[0].Path != "main.go" {
		t.Fatalf("files: %+v", ov.Files)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/api/projects/nope/overview", nil)
	r.SetPathValue("id", "nope")
	c.handleProjectOverview(w, r)
	if w.Code != 404 {
		t.Fatalf("missing project: %d", w.Code)
	}
}
