package main

import "testing"

// "auto" never leaves the nodes; "auto_api" falls back to a keyed cloud planner
// only when no node is ready, and schedules refuse it.
func TestResolveAutoPlanner(t *testing.T) {
	c := testController(t)
	if _, err := c.resolveAutoPlanner("auto"); err == nil {
		t.Fatal("auto with no node must fail")
	}
	if _, err := c.resolveAutoPlanner("auto_api"); err == nil {
		t.Fatal("auto_api with no node and no key must fail")
	}
	c.settings.OpenRouterModel = "x/y"
	if err := c.scheduleAllowed(createReq{Planner: "auto_api"}); err == nil {
		t.Fatal("a schedule must refuse auto_api")
	}
	if err := c.scheduleAllowed(createReq{Planner: "auto"}); err != nil {
		t.Fatalf("a schedule may use auto: %v", err)
	}
}
