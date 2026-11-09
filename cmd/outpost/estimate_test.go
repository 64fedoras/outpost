package main

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestSimulatePlanParallelAndDependencies(t *testing.T) {
	subs := []PlanSubtask{
		{ID: "a", Files: []string{"a.py"}},
		{ID: "b", Files: []string{"b.py"}},
		{ID: "c", Files: []string{"c.py"}, DependsOn: []string{"a", "b"}},
	}
	slots := []*estSlot{{id: "n1", name: "one", tier: 2, speed: 10}, {id: "n2", name: "two", tier: 2, speed: 10}}
	none := func(string) int64 { return -1 }
	e := simulatePlan(subs, slots, 2, false, true, none)
	// a and b run side by side (20 s + 1000/10 s each), c after both.
	if !near(e.Subtasks["a"].Seconds, 120) || !near(e.WriteSeconds, 240) || !near(e.Seconds, 300) || e.Nodes != 2 || e.Rough {
		t.Fatalf("got %+v", e)
	}
	// One node: everything in a row.
	e = simulatePlan(subs, slots[:1], 2, false, false, none)
	if !near(e.Seconds, 360) || e.Nodes != 1 {
		t.Fatalf("one node %+v", e)
	}
}

func TestSimulatePlanHardGoesToStrongest(t *testing.T) {
	subs := []PlanSubtask{{ID: "h", Files: []string{"x.go"}, Difficulty: "hard"}}
	slots := []*estSlot{{id: "fast", name: "fast", tier: 1, speed: 100}, {id: "big", name: "big", tier: 3, speed: 5}}
	e := simulatePlan(subs, slots, 3, false, false, func(string) int64 { return -1 })
	if e.Subtasks["h"].NodeName != "big" || !near(e.Seconds, 20+1600.0/5) {
		t.Fatalf("got %+v", e)
	}
}

func TestSimulatePlanExistingFilesAndUnmeasured(t *testing.T) {
	subs := []PlanSubtask{{ID: "e", Files: []string{"big.py"}}}
	slots := []*estSlot{{id: "n", name: "n", tier: 2}} // never benchmarked
	e := simulatePlan(subs, slots, 2, true, false, func(string) int64 { return 3500 })
	// 3500 bytes ≈ 1100 tokens at the slow guess; review needs a second node, so none here.
	if !e.Rough || !near(e.Seconds, 20+1100/slowSpeedGuess) {
		t.Fatalf("got %+v", e)
	}
	if simulatePlan(subs, nil, 0, false, false, nil) != nil {
		t.Fatal("no workers: no estimate")
	}
}
