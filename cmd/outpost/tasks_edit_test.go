package main

import (
	"strings"
	"testing"
)

func editFixture(status string) *Task {
	return &Task{Status: status, Round: 2, Messages: []TaskMessage{
		{ID: "m1", Role: "user", Kind: "text", Text: "Build a greeter", Round: 1},
		{ID: "m2", Role: "assistant", Kind: "plan", Plan: &Plan{Title: "Greeter"}, Round: 1},
		{ID: "m3", Role: "user", Kind: "text", Text: "make it polite", Round: 1},
		{ID: "m4", Role: "user", Kind: "text", Text: "Add --name", Round: 2},
		{ID: "m5", Role: "user", Kind: "steer", Text: "note", Round: 2},
	}}
}

func TestEditTargetRules(t *testing.T) {
	cases := []struct {
		status, id string
		ok         bool
	}{
		{"running", "m4", false},           // stop first
		{"planning", "m4", false},          // stop first
		{"done", "m1", true},               // opener of round 1
		{"done", "m4", true},               // opener of round 2
		{"done", "m3", false},              // not an opener
		{"done", "m5", false},              // steering notes are not editable
		{"done", "m2", false},              // assistant message
		{"awaiting_approval", "m4", true},  // current round, before the work starts
		{"awaiting_approval", "m1", false}, // an earlier round
		{"done", "m9", false},
	}
	for _, c := range cases {
		task := editFixture(c.status)
		task.mu.Lock()
		idx, _, bad := task.editTargetLocked(c.id)
		task.mu.Unlock()
		if (bad == "") != c.ok {
			t.Errorf("%s %s: ok=%v want %v (%q)", c.status, c.id, bad == "", c.ok, bad)
		}
		if c.ok && task.Messages[idx].ID != c.id {
			t.Errorf("wrong index for %s", c.id)
		}
	}
}

func TestSupersededLeavesPrompt(t *testing.T) {
	task := &Task{dir: t.TempDir(), Round: 1}
	task.Messages = []TaskMessage{
		{Role: "user", Kind: "text", Text: "old wording", Round: 1, Superseded: true},
		{Role: "assistant", Kind: "plan", Plan: &Plan{Title: "OldPlan"}, Round: 1, Superseded: true},
		{Role: "user", Kind: "text", Text: "new wording", Round: 1, Edited: true},
	}
	p := (&Controller{}).plannerPrompt(task, 100000, nil)
	if strings.Contains(p, "old wording") || strings.Contains(p, "OldPlan") || !strings.Contains(p, "User: new wording") {
		t.Fatalf("prompt should only carry the edited branch:\n%s", p)
	}
}
