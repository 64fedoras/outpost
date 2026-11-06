package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstructionsBlock(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "repo"), 0o755)
	task := &Task{dir: dir}
	if task.instructionsBlock() != "" {
		t.Fatal("no instructions should give no block")
	}
	task.Instructions = "no cgo"
	os.WriteFile(filepath.Join(dir, "repo", "OUTPOST.md"), []byte("vanilla JS\n"), 0o644)
	b := task.instructionsBlock()
	if !strings.Contains(b, "no cgo") || !strings.Contains(b, "vanilla JS") {
		t.Fatalf("block = %q", b)
	}
	if got := len(clipInstructions(strings.Repeat("x", 20000))); got != maxInstructions {
		t.Fatalf("clip gave %d", got)
	}
}

func TestInstructionsInPrompts(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "repo"), 0o755)
	task := &Task{dir: dir, Round: 1, Instructions: "ALWAYS-USE-TABS",
		Plan:     &Plan{Title: "P", Subtasks: []PlanSubtask{{ID: "t1", Files: []string{"a.go"}}}},
		Messages: []TaskMessage{{Role: "user", Kind: "text", Text: "hi", Round: 1}}}
	c := &Controller{}
	if p := c.plannerPrompt(task, 1000, nil); !strings.HasPrefix(p, "Project instructions") || !strings.Contains(p, "ALWAYS-USE-TABS") {
		t.Errorf("planner prompt: %s", p)
	}
	spec := task.Plan.Subtasks[0]
	for _, with := range []bool{true, false} {
		if p := c.subtaskPromptWith(task, spec, 1000, with); !strings.Contains(p, "ALWAYS-USE-TABS") {
			t.Errorf("subtask prompt (withCurrent=%v) lacks instructions", with)
		}
	}
}
