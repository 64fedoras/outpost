package main

import "testing"

func TestEnforceTestFirst(t *testing.T) {
	p := &Plan{TestCommand: "go test ./...", Subtasks: []PlanSubtask{
		{ID: "t1", Files: []string{"main.go"}},
		{ID: "t2", Files: []string{"main_test.go"}, DependsOn: []string{"t1"}},
		{ID: "t3", Files: []string{"util.go"}},
	}}
	if err := enforceTestFirst(p); err != nil {
		t.Fatal(err)
	}
	if len(p.Subtasks[1].DependsOn) != 0 {
		t.Fatal("the test subtask must not depend on anything")
	}
	for _, i := range []int{0, 2} {
		if len(p.Subtasks[i].DependsOn) != 1 || p.Subtasks[i].DependsOn[0] != "t2" {
			t.Fatalf("subtask %d: %v", i, p.Subtasks[i].DependsOn)
		}
	}
	if err := validatePlan(p); err != nil {
		t.Fatalf("result must be a valid plan: %v", err)
	}
}

func TestEnforceTestFirstRefuses(t *testing.T) {
	if enforceTestFirst(&Plan{Subtasks: []PlanSubtask{{ID: "t1", Files: []string{"a.go"}}}}) == nil {
		t.Fatal("no test command should be refused")
	}
	p := &Plan{TestCommand: "x", Subtasks: []PlanSubtask{{ID: "t1", Files: []string{"a.go", "a_test.go"}}}}
	if enforceTestFirst(p) == nil {
		t.Fatal("a subtask mixing code and tests is not a test-only subtask")
	}
}
