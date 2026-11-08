package main

import (
	"strings"
	"testing"
	"time"
)

func TestNextWeekly(t *testing.T) {
	// Wednesday 2026-11-04 10:00 -> Monday 2026-11-09 08:00
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	if got := nextWeekly(now, 8); !got.Equal(time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("got %v", got)
	}
	// Monday 07:00 before the hour -> that same Monday; after it -> next week.
	mon := time.Date(2026, 11, 9, 7, 0, 0, 0, time.UTC)
	if got := nextWeekly(mon, 8); got.Day() != 9 {
		t.Fatalf("same Monday expected, got %v", got)
	}
	if got := nextWeekly(mon.Add(2*time.Hour), 8); got.Day() != 16 {
		t.Fatalf("next Monday expected, got %v", got)
	}
}

func TestWeeklyText(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), tasks: map[string]*Task{}, nodes: map[string]*NodeRec{"n1": {ID: "n1", Name: "gaming"}}, conns: map[string]*nodeConn{}}
	now := time.Now()
	recent, old := now.Add(-24*time.Hour), now.Add(-10*24*time.Hour)
	c.tasks["a"] = &Task{ID: "a", Status: "done", Ended: &recent, CostUSD: 0.5, Subtasks: []*SubtaskState{{Status: "done", Node: "n1"}, {Status: "done", Node: "n1"}}}
	c.tasks["b"] = &Task{ID: "b", Status: "failed", Ended: &recent}
	c.tasks["c"] = &Task{ID: "c", Status: "done", Ended: &old, CostUSD: 9}
	c.tasks["d"] = &Task{ID: "d", Status: "awaiting_approval"}
	s := c.weeklyText(now)
	for _, want := range []string{"1 task(s) finished, 1 failed, 1 waiting", "$0.50", "gaming (2)", "Offline nodes: gaming"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %q", want, s)
		}
	}
	if strings.Contains(s, "$9") {
		t.Errorf("old task cost counted: %q", s)
	}
}
