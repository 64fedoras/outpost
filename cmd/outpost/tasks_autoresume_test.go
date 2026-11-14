package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	if !c.tasks["local"].AutoResume || c.tasks["local"].Status != "interrupted" {
		t.Errorf("local task should be interrupted and auto-resumable")
	}
	if c.tasks["cloud"].AutoResume || c.tasks["cloud"].Status != "interrupted" {
		t.Errorf("claude task must stay interrupted without auto-resume")
	}
}

func TestAutoResumeWaitsForNode(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), conns: map[string]*nodeConn{}, lastHB: map[string]*Heartbeat{},
		pipelines: map[string]*pipeline{}, cooldown: map[string]time.Time{}, models: map[string]*ModelRec{}}
	dir := filepath.Join(c.tasksDir(), "t1")
	os.MkdirAll(dir, 0o755)
	tk := &Task{ID: "t1", Planner: "node:n1", Status: "interrupted", AutoResume: true, dir: dir}
	c.tasks = map[string]*Task{"t1": tk}
	// No node: times out, task stays interrupted, flag cleared.
	c.autoResumeInterrupted(30*time.Millisecond, 5*time.Millisecond)
	if tk.Status != "interrupted" || tk.AutoResume {
		t.Fatalf("expected interrupted and flag cleared, got %s %v", tk.Status, tk.AutoResume)
	}
}

func TestAutoResumeWhenNodeReady(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), conns: map[string]*nodeConn{"n1": {}},
		lastHB:    map[string]*Heartbeat{"n1": {InferenceRdy: true, LoadedModel: "m"}},
		pipelines: map[string]*pipeline{}, cooldown: map[string]time.Time{}, models: map[string]*ModelRec{}}
	dir := filepath.Join(c.tasksDir(), "t1")
	os.MkdirAll(dir, 0o755)
	// No plan approved yet: resuming lands on awaiting_approval, no goroutines.
	tk := &Task{ID: "t1", Planner: "node:n1", Status: "interrupted", AutoResume: true, dir: dir, Plan: &Plan{}}
	c.tasks = map[string]*Task{"t1": tk}
	c.autoResumeInterrupted(time.Second, 5*time.Millisecond)
	if tk.Status != "awaiting_approval" || tk.AutoResume {
		t.Fatalf("got %s AutoResume=%v", tk.Status, tk.AutoResume)
	}
}

func TestAutoResumeOffLeavesTask(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), settings: Settings{AutoResumeOff: true}, conns: map[string]*nodeConn{"n1": {}},
		lastHB:    map[string]*Heartbeat{"n1": {InferenceRdy: true, LoadedModel: "m"}},
		pipelines: map[string]*pipeline{}, cooldown: map[string]time.Time{}, models: map[string]*ModelRec{}}
	dir := filepath.Join(c.tasksDir(), "t1")
	os.MkdirAll(dir, 0o755)
	tk := &Task{ID: "t1", Planner: "node:n1", Status: "interrupted", AutoResume: true, dir: dir}
	c.tasks = map[string]*Task{"t1": tk}
	c.autoResumeInterrupted(time.Second, 5*time.Millisecond)
	if tk.Status != "interrupted" {
		t.Fatalf("setting off must leave the task interrupted, got %s", tk.Status)
	}
}

func TestTryAgainResetsFailedSubtasks(t *testing.T) {
	tk := &Task{Subtasks: []*SubtaskState{
		{ID: "a", Status: "done"},
		{ID: "b", Status: "failed", Attempts: 3, Error: "boom", ReviewNote: "bad"},
	}}
	if n := tk.resetFailedSubtasksLocked(); n != 1 {
		t.Fatalf("reset %d, want 1", n)
	}
	if tk.Subtasks[0].Status != "done" {
		t.Errorf("finished subtask must be kept")
	}
	b := tk.Subtasks[1]
	if b.Status != "pending" || b.Attempts != 0 || b.Error != "" || b.ReviewNote != "" {
		t.Errorf("failed subtask not reset: %+v", b)
	}
}

// A task waits for its own planner node, not just any ready node.
func TestAutoResumeWaitsForPlannerNode(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), conns: map[string]*nodeConn{"other": {}},
		lastHB:    map[string]*Heartbeat{"other": {InferenceRdy: true, LoadedModel: "m"}},
		pipelines: map[string]*pipeline{}, cooldown: map[string]time.Time{}, models: map[string]*ModelRec{},
		resumeWake: make(chan struct{}, 1)}
	dir := filepath.Join(c.tasksDir(), "t1")
	os.MkdirAll(dir, 0o755)
	tk := &Task{ID: "t1", Planner: "node:n1", Status: "interrupted", AutoResume: true, dir: dir, Plan: &Plan{}}
	c.tasks = map[string]*Task{"t1": tk}
	done := make(chan struct{})
	go func() { c.autoResumeInterrupted(5*time.Second, time.Hour); close(done) }()
	time.Sleep(50 * time.Millisecond)
	tk.mu.Lock()
	st := tk.Status
	tk.mu.Unlock()
	if st != "interrupted" {
		t.Fatalf("resumed before its planner node was back: %s", st)
	}
	// The planner node's first heartbeat resumes it at once (the tick is an hour).
	c.mu.Lock()
	c.conns["n1"] = &nodeConn{}
	c.lastHB["n1"] = &Heartbeat{InferenceRdy: true, LoadedModel: "m"}
	c.mu.Unlock()
	c.nudgeResume()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		buf := make([]byte, 1<<16)
		t.Fatalf("heartbeat did not wake the resume loop: %s\n%s", tk.Status, buf[:runtime.Stack(buf, true)])
	}
	if tk.Status != "awaiting_approval" || tk.AutoResume {
		t.Fatalf("got %s AutoResume=%v", tk.Status, tk.AutoResume)
	}
}

// The flag is saved, so a second restart while waiting keeps waiting, and it
// is dropped when the setting is off.
func TestLoadTasksKeepsAutoResumeAcrossRestarts(t *testing.T) {
	for _, off := range []bool{false, true} {
		c := &Controller{dataDir: t.TempDir(), settings: Settings{AutoResumeOff: off}}
		dir := filepath.Join(c.tasksDir(), "t1")
		os.MkdirAll(dir, 0o755)
		tk := &Task{ID: "t1", Planner: "node:n1", Status: "interrupted", PausedFrom: "running", AutoResume: true, dir: dir}
		tk.saveLocked()
		c.loadTasks()
		if got := c.tasks["t1"].AutoResume; got == off {
			t.Errorf("off=%v: AutoResume=%v", off, got)
		}
		if s := c.tasks["t1"].summaryLocked(); s.NeedsYou == !off {
			t.Errorf("off=%v: NeedsYou=%v", off, s.NeedsYou)
		}
	}
}

func TestStopClearsAutoResume(t *testing.T) {
	c := &Controller{dataDir: t.TempDir()}
	dir := filepath.Join(c.tasksDir(), "t1")
	os.MkdirAll(dir, 0o755)
	tk := &Task{ID: "t1", Planner: "node:n1", Status: "interrupted", AutoResume: true, dir: dir}
	c.tasks = map[string]*Task{"t1": tk}
	r := httptest.NewRequest("POST", "/api/tasks/t1/cancel", nil)
	r.SetPathValue("id", "t1")
	w := httptest.NewRecorder()
	c.handleCancelTask(w, r)
	if w.Code != 200 || tk.Status != "cancelled" || tk.AutoResume {
		t.Fatalf("code %d status %s AutoResume=%v", w.Code, tk.Status, tk.AutoResume)
	}
}

func TestOrphanJobs(t *testing.T) {
	jobs := map[string]*JobRec{"live": {Status: "running"}, "gone": {Status: "lost"}}
	got := orphanJobsLocked(jobs, []string{"live", "gone", "unknown"})
	if len(got) != 2 || got[0] != "gone" || got[1] != "unknown" {
		t.Fatalf("got %v", got)
	}
}

// Auto-resume must not hold c.mu while it takes a task's lock: dispatch holds a
// task's lock and then takes c.mu (nodeLabel), so the two used to deadlock.
func TestAutoResumeLockOrder(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), conns: map[string]*nodeConn{}, lastHB: map[string]*Heartbeat{},
		pipelines: map[string]*pipeline{}, cooldown: map[string]time.Time{}, models: map[string]*ModelRec{}, nodes: map[string]*NodeRec{}}
	c.resumeWake = make(chan struct{}, 1)
	dir := filepath.Join(c.tasksDir(), "t1")
	os.MkdirAll(dir, 0o755)
	waiting := &Task{ID: "t1", Planner: "node:n1", Status: "interrupted", AutoResume: true, dir: dir}
	running := &Task{ID: "t2", Planner: "node:n2", Status: "running", dir: dir}
	c.tasks = map[string]*Task{"t1": waiting, "t2": running}
	go c.autoResumeInterrupted(time.Second, 50*time.Microsecond)
	done := make(chan struct{})
	go func() {
		for end := time.Now().Add(700 * time.Millisecond); time.Now().Before(end); {
			running.mu.Lock() // as executeTask does: the task's lock, then c.mu in nodeLabel
			_ = c.nodeLabel("n2")
			running.mu.Unlock()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock between auto-resume and task dispatch")
	}
}
