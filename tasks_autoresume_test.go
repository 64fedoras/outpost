package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAutoResumeEligible(t *testing.T) {
	cases := []struct {
		planner, from string
		want          bool
	}{
		{"claude", "running", false},
		{"openrouter", "running", false},
		{"node:abc", "running", true},
		{"node:abc", "planning", true},
		{"node:abc", "paused_cap", false},
	}
	for _, tc := range cases {
		if got := autoResumeEligible(&Task{Planner: tc.planner, PausedFrom: tc.from}); got != tc.want {
			t.Errorf("%s/%s: got %v want %v", tc.planner, tc.from, got, tc.want)
		}
	}
}

func TestLoadTasksMarksAutoResume(t *testing.T) {
	c := &Controller{dataDir: t.TempDir()}
	for id, planner := range map[string]string{"local": "node:n1", "cloud": "claude"} {
		dir := filepath.Join(c.tasksDir(), id)
		os.MkdirAll(dir, 0o755)
		tk := &Task{ID: id, Planner: planner, Status: "running", dir: dir}
		tk.saveLocked()
	}
	c.loadTasks()
	if !c.tasks["local"].autoResume || c.tasks["local"].Status != "interrupted" {
		t.Errorf("local task should be interrupted and auto-resumable")
	}
	if c.tasks["cloud"].autoResume || c.tasks["cloud"].Status != "interrupted" {
		t.Errorf("claude task must stay interrupted without auto-resume")
	}
}

func TestAutoResumeWaitsForNode(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), conns: map[string]*nodeConn{}, lastHB: map[string]*Heartbeat{},
		pipelines: map[string]*pipeline{}, cooldown: map[string]time.Time{}, models: map[string]*ModelRec{}}
	dir := filepath.Join(c.tasksDir(), "t1")
	os.MkdirAll(dir, 0o755)
	tk := &Task{ID: "t1", Planner: "node:n1", Status: "interrupted", autoResume: true, dir: dir}
	c.tasks = map[string]*Task{"t1": tk}
	// No node: times out, task stays interrupted, flag cleared.
	c.autoResumeInterrupted(30*time.Millisecond, 5*time.Millisecond)
	if tk.Status != "interrupted" || tk.autoResume {
		t.Fatalf("expected interrupted and flag cleared, got %s %v", tk.Status, tk.autoResume)
	}
}

func TestAutoResumeWhenNodeReady(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), conns: map[string]*nodeConn{"n1": {}},
		lastHB:    map[string]*Heartbeat{"n1": {InferenceRdy: true, LoadedModel: "m"}},
		pipelines: map[string]*pipeline{}, cooldown: map[string]time.Time{}, models: map[string]*ModelRec{}}
	dir := filepath.Join(c.tasksDir(), "t1")
	os.MkdirAll(dir, 0o755)
	// No plan approved yet: resuming lands on awaiting_approval, no goroutines.
	tk := &Task{ID: "t1", Planner: "node:n1", Status: "interrupted", autoResume: true, dir: dir, Plan: &Plan{}}
	c.tasks = map[string]*Task{"t1": tk}
	c.autoResumeInterrupted(time.Second, 5*time.Millisecond)
	if tk.Status != "awaiting_approval" || tk.autoResume {
		t.Fatalf("got %s autoResume=%v", tk.Status, tk.autoResume)
	}
}

func TestAutoResumeOffLeavesTask(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), settings: Settings{AutoResumeOff: true}, conns: map[string]*nodeConn{"n1": {}},
		lastHB:    map[string]*Heartbeat{"n1": {InferenceRdy: true, LoadedModel: "m"}},
		pipelines: map[string]*pipeline{}, cooldown: map[string]time.Time{}, models: map[string]*ModelRec{}}
	dir := filepath.Join(c.tasksDir(), "t1")
	os.MkdirAll(dir, 0o755)
	tk := &Task{ID: "t1", Planner: "node:n1", Status: "interrupted", autoResume: true, dir: dir}
	c.tasks = map[string]*Task{"t1": tk}
	c.autoResumeInterrupted(time.Second, 5*time.Millisecond)
	if tk.Status != "interrupted" {
		t.Fatalf("setting off must leave the task interrupted, got %s", tk.Status)
	}
}
