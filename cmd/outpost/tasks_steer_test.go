package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A note sent while the planner is answering is not lost and does not throw the
// answer away: once the answer is in, the planner gets it back with the note in
// the same conversation, and only the revised plan is published.
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
	first, noted := make(chan struct{}), make(chan struct{})
	c.inferStub = func(ctx context.Context, node string, spec InferenceSpec) (*InferenceOutput, error) {
		mu.Lock()
		prompts = append(prompts, spec.Messages[len(spec.Messages)-1].Content)
		n := len(prompts)
		mu.Unlock()
		if n == 1 {
			close(first)
			<-noted // still answering when the note arrives
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return &InferenceOutput{Text: strings.Replace(planJSON, "Dice", "Draft", 1)}, nil
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
	close(noted)

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
		t.Errorf("plan title %q: the draft was published instead of the revision", tk.Plan.Title)
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
	if len(prompts) != 2 {
		t.Fatalf("%d planner calls, want 2 (the answer, then the revision)", len(prompts))
	}
	last := prompts[1]
	if !strings.Contains(last, "use only the standard library") || !strings.Contains(last, `"title":"Draft"`) {
		t.Errorf("the revision did not get the note and the earlier answer:\n%s", last)
	}
	if len(tk.Queued) != 0 {
		t.Errorf("notes left over: %v", tk.Queued)
	}
}

// "Send now" cancels the running planner call, drops its answer, and plans again
// with the note; the cancelled pass must not publish over the new one.
func TestSendNowInterruptsPlanning(t *testing.T) {
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
			<-ctx.Done() // a long answer, cut short by Send now
			return nil, ctx.Err()
		}
		return &InferenceOutput{Text: planJSON}, nil
	}
	c.startTask(tk, c.planTask)
	<-first

	req := httptest.NewRequest("POST", "/api/tasks/"+tk.ID+"/messages", strings.NewReader(`{"text":"drop me"}`))
	req.SetPathValue("id", tk.ID)
	c.handleTaskMessage(httptest.NewRecorder(), req)
	req = httptest.NewRequest("POST", "/api/tasks/"+tk.ID+"/messages", strings.NewReader(`{"text":"use only the standard library"}`))
	req.SetPathValue("id", tk.ID)
	c.handleTaskMessage(httptest.NewRecorder(), req)
	tk.mu.Lock()
	if len(tk.Queued) != 2 {
		t.Fatalf("%d queued notes, want 2", len(tk.Queued))
	}
	drop := tk.Queued[0].ID
	tk.mu.Unlock()

	req = httptest.NewRequest("DELETE", "/api/tasks/"+tk.ID+"/queued/"+drop, nil)
	req.SetPathValue("id", tk.ID)
	req.SetPathValue("note", drop)
	if w := httptest.NewRecorder(); func() int { c.handleRemoveQueued(w, req); return w.Code }() != 200 {
		t.Fatal("remove failed")
	}

	req = httptest.NewRequest("POST", "/api/tasks/"+tk.ID+"/send-now", nil)
	req.SetPathValue("id", tk.ID)
	w := httptest.NewRecorder()
	c.handleSendNow(w, req)
	if w.Code != 200 {
		t.Fatalf("send-now: %d %s", w.Code, w.Body.String())
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
	time.Sleep(100 * time.Millisecond)

	tk.mu.Lock()
	defer tk.mu.Unlock()
	if tk.Status != "awaiting_approval" || tk.Plan == nil {
		t.Fatalf("status %q, plan %v", tk.Status, tk.Plan)
	}
	if len(tk.Queued) != 0 {
		t.Errorf("queue not empty: %v", tk.Queued)
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
	if len(prompts) != 2 {
		t.Fatalf("%d planner calls, want 2", len(prompts))
	}
	if !strings.Contains(prompts[1], "use only the standard library") || strings.Contains(prompts[1], "drop me") {
		t.Errorf("new pass prompt wrong:\n%s", prompts[1])
	}
}
