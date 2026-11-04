package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskMetaRenameAndPin(t *testing.T) {
	tk := &Task{ID: "t1", Status: "done", Prompt: "write a dice roller"}
	c := rerunController(t, tk)

	if w := post(c, c.handleTaskMeta, "t1", `{"title":"  Dice\n roller  "}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if tk.Title != "Dice roller" || tk.Pinned {
		t.Fatalf("title not cleaned or pin changed: %q %v", tk.Title, tk.Pinned)
	}
	w := post(c, c.handleTaskMeta, "t1", `{"pinned":true}`)
	var s taskSummary
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil || !s.Pinned || s.Title != "Dice roller" {
		t.Fatalf("pin: %d %s", w.Code, w.Body)
	}
	b, err := os.ReadFile(filepath.Join(tk.dir, "task.json"))
	if err != nil || !strings.Contains(string(b), `"pinned": true`) || !strings.Contains(string(b), `"title": "Dice roller"`) {
		t.Fatalf("not saved: %v %s", err, b)
	}
	if w := post(c, c.handleTaskMeta, "t1", `{"title":"   "}`); w.Code != 400 {
		t.Fatalf("empty title should be refused, got %d", w.Code)
	}
	if w := post(c, c.handleTaskMeta, "t1", `{"title":"`+strings.Repeat("é", 300)+`"}`); w.Code != 200 || len([]rune(tk.Title)) != maxTaskTitle {
		t.Fatalf("long title not cut: %d %d", w.Code, len([]rune(tk.Title)))
	}
	if w := post(c, c.handleTaskMeta, "nope", `{"pinned":true}`); w.Code != 404 {
		t.Fatalf("unknown task: %d", w.Code)
	}
}
