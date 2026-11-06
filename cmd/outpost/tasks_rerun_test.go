package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func samplePlan() *Plan {
	return &Plan{Title: "Dice", Note: "keep me", Subtasks: []PlanSubtask{
		{ID: "s1", Title: "Core", Files: []string{"dice.py"}, Instructions: "roll"},
		{ID: "s2", Title: "CLI", Files: []string{"cli.py"}, DependsOn: []string{"s1"}, Instructions: "args"},
	}}
}

func TestCleanEditedPlan(t *testing.T) {
	cur := samplePlan()
	in := samplePlan()
	in.Note = "changed"
	in.Subtasks[1].Title = "  Command line "
	p, err := cleanEditedPlan(cur, in)
	if err != nil || p.Note != "keep me" || p.Subtasks[1].Title != "Command line" {
		t.Fatalf("got %+v %v", p, err)
	}
	bad := map[string]func(*Plan){
		"title":  func(p *Plan) { p.Title = " " },
		"empty":  func(p *Plan) { p.Subtasks[0].Instructions = "" },
		"dep":    func(p *Plan) { p.Subtasks[1].DependsOn = []string{"nope"} },
		"cycle":  func(p *Plan) { p.Subtasks[0].DependsOn = []string{"s2"} },
		"path":   func(p *Plan) { p.Subtasks[0].Files = []string{"../x"} },
		"nofile": func(p *Plan) { p.Subtasks[0].Files = nil },
		"dupe":   func(p *Plan) { p.Subtasks[1].Files = []string{"dice.py"} },
	}
	for name, f := range bad {
		in := samplePlan()
		f(in)
		if _, err := cleanEditedPlan(cur, in); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func rerunController(t *testing.T, tk *Task) *Controller {
	c := &Controller{dataDir: t.TempDir()}
	tk.dir = filepath.Join(c.tasksDir(), tk.ID)
	os.MkdirAll(tk.dir, 0o755)
	c.tasks = map[string]*Task{tk.ID: tk}
	c.nodes = map[string]*NodeRec{"n1": {}}
	return c
}

func post(c *Controller, h func(w http.ResponseWriter, r *http.Request), id, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestEditPlanBeforeApproval(t *testing.T) {
	tk := &Task{ID: "t1", Status: "awaiting_approval", Round: 1, Plan: samplePlan(), Subtasks: []*SubtaskState{{ID: "s1"}, {ID: "s2"}}}
	tk.Messages = []TaskMessage{{ID: "m1", Kind: "plan", Plan: samplePlan(), Round: 1}}
	c := rerunController(t, tk)
	body := `{"plan":{"title":"Dice 2","subtasks":[{"id":"s1","title":"Core","files":["dice.py"],"instructions":"roll"}]}}`
	if w := post(c, c.handleEditPlan, "t1", body); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if tk.Plan.Title != "Dice 2" || len(tk.Subtasks) != 1 || tk.Status != "awaiting_approval" || tk.Approved != nil {
		t.Fatalf("plan not replaced: %+v", tk)
	}
	if !tk.Messages[0].Superseded || len(tk.Messages) != 2 || !tk.Messages[1].Edited {
		t.Fatalf("old plan should be superseded and the new one marked edited: %+v", tk.Messages)
	}
}

func TestEditAndRerunRefusedWhileRunning(t *testing.T) {
	tk := &Task{ID: "t1", Status: "running", Round: 1, Plan: samplePlan()}
	c := rerunController(t, tk)
	if w := post(c, c.handleEditPlan, "t1", `{"plan":{"title":"x"}}`); w.Code != 409 {
		t.Errorf("edit: %d", w.Code)
	}
	if w := post(c, c.handleRerunPlan, "t1", `{}`); w.Code != 409 {
		t.Errorf("rerun: %d", w.Code)
	}
}

func TestRerunWholePutsFilesBack(t *testing.T) {
	tk := &Task{ID: "t1", Status: "done", Round: 1, Plan: samplePlan(), Planner: "node:n1", Approved: &[]time.Time{time.Now()}[0],
		Subtasks: []*SubtaskState{{ID: "s1", Status: "done"}, {ID: "s2", Status: "failed"}}}
	c := rerunController(t, tk)
	os.MkdirAll(tk.repoDir(), 0o755)
	os.WriteFile(filepath.Join(tk.repoDir(), "dice.py"), []byte("old"), 0o644)
	if err := tk.writeFile("dice.py", "new"); err != nil {
		t.Fatal(err)
	}
	w := post(c, c.handleRerunPlan, "t1", `{}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(tk.repoDir(), "dice.py")); string(b) != "old" {
		t.Errorf("file not restored: %q", b)
	}
	tk.mu.Lock()
	defer tk.mu.Unlock()
	if tk.Round != 2 || tk.Status != "running" || tk.Subtasks[1].Status != "pending" {
		t.Errorf("round/status/subtasks wrong: %d %s %+v", tk.Round, tk.Status, tk.Subtasks[1])
	}
	if tk.cancel != nil {
		tk.cancel()
	}
}
