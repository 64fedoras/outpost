package main

import (
	"strings"
	"testing"
	"time"
)

func TestTestSummary(t *testing.T) {
	cases := []struct {
		out  string
		exit int
		want string
	}{
		{"Ran 12 tests in 0.1s\n\nOK", 0, "12 tests passed"},
		{"Ran 12 tests in 0.1s\n\nFAILED (failures=3, errors=1)", 1, "4 failing of 12"},
		{"Ran 5 tests in 0.1s\n\nFAILED (failures=2)", 1, "2 failing of 5"},
		{"==== 2 failed, 8 passed in 0.5s ====", 1, "2 failing"},
		{"--- FAIL: TestA\n--- FAIL: TestB\nFAIL", 1, "2 failing"},
		{"boom", 2, "failed (exit code 2)"},
		{"", 0, "passed"},
		{"3 passed in 0.2s", 0, "3 tests passed"},
	}
	for _, c := range cases {
		if got := testSummary(c.out, c.exit); got != c.want {
			t.Errorf("testSummary(%q,%d) = %q, want %q", c.out, c.exit, got, c.want)
		}
	}
}

func TestActivityTrail(t *testing.T) {
	task := &Task{ID: "t1", dir: t.TempDir()}
	task.actBegin("Running the tests", "n1")
	task.actEnd("bad", "Ran the tests: 3 failing")
	task.actBeginQ("thinking", "", true)
	task.actBegin("editing a.py", "")
	task.actEnd("ok", "Edited a.py")
	if len(task.act) != 2 || task.act[0].Status != "bad" || task.act[1].Text != "Edited a.py" {
		t.Fatalf("trail = %+v", task.act)
	}
	for i := 0; i < 30; i++ {
		task.actBegin("step", "")
	}
	if len(task.act) != maxActSteps {
		t.Fatalf("trail not capped: %d", len(task.act))
	}
	task.actClear()
	if len(task.act) != 0 {
		t.Fatal("not cleared")
	}
}

func TestActivityStallOffline(t *testing.T) {
	c := testController(t)
	task := &Task{ID: "t2", dir: t.TempDir(), Status: "testing"}
	task.actBegin("Running the tests", "gone")
	d := c.activityFor(task)
	if d == nil || !strings.Contains(d.Stall, "offline") {
		t.Fatalf("want offline stall, got %+v", d)
	}
	task.Status = "done"
	if c.activityFor(task) != nil {
		t.Fatal("finished tasks have no activity")
	}
}

func TestLiveIdle(t *testing.T) {
	h := newLiveHub()
	lh := h.begin("t3", "", "Fix", "n", false)
	h.mu.Lock()
	for _, lc := range h.tasks["t3"].calls {
		lc.lastOut = time.Now().Add(-3 * time.Minute)
	}
	h.mu.Unlock()
	if h.idleFor("t3") < 2*time.Minute {
		t.Fatal("idle not reported")
	}
	lh.Write("x")
	if h.idleFor("t3") > time.Second {
		t.Fatal("output should reset idle")
	}
}

func TestWaitPlannerBackOnlyForLocalPlanner(t *testing.T) {
	c := testController(t)
	task := &Task{ID: "t4", dir: t.TempDir(), Planner: "claude"}
	n := 0
	if c.waitPlannerBack(t.Context(), task, errf("lost", true, "x"), &n) {
		t.Fatal("a cloud planner has no node to wait for")
	}
	task.Planner = "node:n1"
	n = 3
	if c.waitPlannerBack(t.Context(), task, errf("lost", true, "x"), &n) {
		t.Fatal("retries are limited")
	}
}
