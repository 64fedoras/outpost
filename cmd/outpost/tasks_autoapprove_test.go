package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// runChatPlan plans a task the project conversation started and returns it
// once the planner has finished (approved, or waiting for the owner).
func runChatPlan(t *testing.T, planJSON string, earlier []TaskMessage) *Task {
	t.Helper()
	r := newBonRig(t, map[string]string{"n1": "big-32b"})
	c := r.c
	c.settings = Settings{}
	tk := r.task
	tk.Planner, tk.Status, tk.Round, tk.Plan, tk.FromChat = "node:n1", "planning", 1, nil, true
	tk.Subtasks = nil
	tk.Messages = append([]TaskMessage{{ID: "m1", Round: 1, Role: "user", Kind: "text", Text: "make a dice roller"}}, earlier...)
	c.tasks = map[string]*Task{tk.ID: tk}
	c.inferStub = func(ctx context.Context, node string, spec InferenceSpec) (*InferenceOutput, error) {
		if strings.Contains(spec.Messages[0].Content+spec.Messages[len(spec.Messages)-1].Content, "dice.py") {
			return &InferenceOutput{Text: "```python\ndef roll():\n    return 4\n```"}, nil
		}
		return &InferenceOutput{Text: planJSON}, nil
	}
	c.startTask(tk, c.planTask)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk.mu.Lock()
		ok := tk.Approved != nil || tk.Status == "awaiting_approval"
		tk.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { stopTaskAndWait(tk) })
	return tk
}

func msgsContain(tk *Task, s string) bool {
	tk.mu.Lock()
	defer tk.mu.Unlock()
	for _, m := range tk.Messages {
		if strings.Contains(m.Text, s) {
			return true
		}
	}
	return false
}

const dicePlan = `{"title":"Dice","summary":"s","language":"python","assumptions":[{"topic":"Style","choice":"CLI","options":["CLI","GUI"]}],"subtasks":[{"id":"s1","title":"dice","files":["dice.py"],"instructions":"write roll()","difficulty":"easy"}]}`

// A task the project conversation started begins on its own, even with
// assumptions on the plan (they are defaults the owner can still change by
// stopping it).
func TestChatTaskPlanStartsOnItsOwn(t *testing.T) {
	tk := runChatPlan(t, dicePlan, nil)
	tk.mu.Lock()
	approved := tk.Approved
	tk.mu.Unlock()
	if approved == nil || !msgsContain(tk, "Started on its own") {
		t.Fatalf("plan from the project conversation was not started on its own")
	}
}

// A plan that removes files keeps the approval step.
func TestChatTaskPlanWithDeleteWaits(t *testing.T) {
	const p = `{"title":"Dice","summary":"s","language":"python","delete":["old.py"],"subtasks":[{"id":"s1","title":"dice","files":["dice.py"],"instructions":"write roll()","difficulty":"easy"}]}`
	tk := runChatPlan(t, p, nil)
	tk.mu.Lock()
	approved, st := tk.Approved, tk.Status
	tk.mu.Unlock()
	if approved != nil || st != "awaiting_approval" || !msgsContain(tk, "deletes or renames") {
		t.Fatalf("a deleting plan should wait (status %s, approved %v)", st, approved)
	}
}

// Once the owner has seen a plan and asked for changes, the revised plan waits.
func TestChatTaskRevisedPlanWaits(t *testing.T) {
	earlier := []TaskMessage{{ID: "m2", Round: 1, Role: "assistant", Kind: "plan", Plan: &Plan{Title: "Dice"}}, {ID: "m3", Round: 1, Role: "user", Kind: "text", Text: "use a GUI"}}
	tk := runChatPlan(t, dicePlan, earlier)
	tk.mu.Lock()
	approved, st := tk.Approved, tk.Status
	tk.mu.Unlock()
	if approved != nil || st != "awaiting_approval" {
		t.Fatalf("a revised plan should wait for the owner (status %s, approved %v)", st, approved)
	}
}

func TestAutoApproveHold(t *testing.T) {
	if h := autoApproveHold(&Task{Planner: "node:n1"}, &Plan{}); h != "" {
		t.Fatalf("local plan held: %s", h)
	}
	if h := autoApproveHold(&Task{Planner: "claude"}, &Plan{}); h == "" {
		t.Fatal("cloud planner should hold")
	}
	if h := autoApproveHold(&Task{Planner: "node:n1"}, &Plan{Rename: []PlanRename{{}}}); h == "" {
		t.Fatal("rename should hold")
	}
}
