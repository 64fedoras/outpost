package main

import "testing"

func TestWithSearchBrief(t *testing.T) {
	h := []ChatMessage{{Role: "system", Content: "s"}, {Role: "user", Content: "q"}}
	if got := withSearchBrief(h, ""); got[1].Content != "q" {
		t.Fatal("empty brief must not change history")
	}
	got := withSearchBrief(h, "BRIEF")
	if got[1].Content == "q" || h[1].Content != "q" {
		t.Fatalf("brief not added or input mutated: %q / %q", got[1].Content, h[1].Content)
	}
}

func TestChatSearchOffByDefault(t *testing.T) {
	c := &Controller{}
	if b, n := c.chatSearchBrief(t.Context(), "x", nil); b != "" || n != "" {
		t.Fatalf("got %q %q", b, n)
	}
}

func TestPlannerSearchCostEstimate(t *testing.T) {
	// Documented per-result prices stay in step with what the cap check assumes.
	if webSearchUSD != 0.01 || webSearchUses != 3 {
		t.Fatalf("claude search price changed: %v x %v", webSearchUSD, webSearchUses)
	}
}
