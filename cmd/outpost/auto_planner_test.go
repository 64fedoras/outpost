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

// The order setting decides which provider "auto_api" tries first, and a
// provider that is not usable is skipped.
func TestAutoOrder(t *testing.T) {
	if got := normAutoOrder(nil); len(got) != 3 || got[0] != "local" {
		t.Fatalf("default: %v", got)
	}
	if got := normAutoOrder([]string{"openrouter", "bogus", "openrouter", "local"}); len(got) != 2 || got[0] != "openrouter" || got[1] != "local" {
		t.Fatalf("normalised: %v", got)
	}
	c := testController(t)
	c.settings.OpenRouterModel = "x/y"
	enc, err := c.encrypt("sk-test")
	if err != nil {
		t.Fatal(err)
	}
	c.settings.OpenRouterKeyEnc = enc
	c.settings.AutoOrder = []string{"claude", "openrouter"}
	if p, err := c.resolveAutoPlanner("auto_api"); err != nil || p != "openrouter" {
		t.Fatalf("claude has no key, so openrouter: %q %v", p, err)
	}
	c.settings.AnthropicKeyEnc = enc
	if p, _ := c.resolveAutoPlanner("auto_api"); p != "claude" {
		t.Fatalf("claude first: %q", p)
	}
	c.settings.AutoOrder = []string{"openrouter", "claude"}
	if p, _ := c.resolveAutoPlanner("auto_api"); p != "openrouter" {
		t.Fatalf("openrouter first: %q", p)
	}
}
