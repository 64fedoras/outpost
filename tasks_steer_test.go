package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A note sent while the planner is thinking stops that pass and plans again
// with the note. The stopped pass must not publish a plan of its own.
func TestSteerWhileNodePlanning(t *testing.T) {
	r := newBonRig(t, map[string]string{"n1": "big-32b"})
	c := r.c
	c.settings = Settings{}
	tk := r.task
	tk.Planner, tk.Status, tk.Round, tk.Plan = "node:n1", "planning", 1, nil
	tk.Subtasks = nil
	tk.Messages = []TaskMessage{{ID: "m1", Round: 1, Role: "user", Kind: "text", Text: "make a dice roller"}}
	c.tasks = map[string]*Task{tk.ID: tk}

	const planJSON = `{"title":"Dice","summary":"s","language":"python","subtasks":[{"id":"s1","title":"dice","files":["dice.py"],"instructions":"write roll()","difficulty":"easy"}]}`
	var mu sync.Mutex
	var prompts []string
	first := make(chan struct{})
	c.inferStub = func(ctx context.Context, node string, spec InferenceSpec) (*InferenceOutput, error) {
		mu.Lock()
		prompts = append(prompts, spec.Messages[len(spec.Messages)-1].Content)
		n := len(prompts)
		mu.Unlock()
		if n == 1 {
			close(first)
			<-ctx.Done() // the slow first pass
			// answer anyway, as a call that finished just as it was cancelled
			return &InferenceOutput{Text: strings.Replace(planJSON, "Dice", "Stale", 1)}, nil
		}
		return &InferenceOutput{Text: planJSON}, nil
	}
	c.startTask(tk, c.planTask)
	<-first

	req := httptest.NewRequest("POST", "/api/tasks/"+tk.ID+"/messages", strings.NewReader(`{"text":"use only the standard library"}`))
	req.SetPathValue("id", tk.ID)
	w := httptest.NewRecorder()
	c.handleTaskMessage(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk.mu.Lock()
		st := tk.Status
		tk.mu.Unlock()
		if st == "awaiting_approval" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // let the stopped pass finish, if it were going to publish

	tk.mu.Lock()
	defer tk.mu.Unlock()
	if tk.Status != "awaiting_approval" || tk.Plan == nil {
		t.Fatalf("status %q, plan %v", tk.Status, tk.Plan)
	}
	if tk.Plan.Title != "Dice" {
		t.Errorf("plan title %q: the stopped pass published its plan", tk.Plan.Title)
	}
	plans := 0
	for _, m := range tk.Messages {
		if m.Kind == "plan" {
			plans++
		}
	}
	if plans != 1 {
		t.Errorf("%d plan messages, want 1", plans)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(prompts) < 2 || !strings.Contains(prompts[len(prompts)-1], "use only the standard library") {
		t.Errorf("the second pass did not see the note: %v", prompts)
	}
}
