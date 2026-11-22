package main

import "testing"

// Without a Claude key the stock default planner must not fail the first task: it behaves as Auto.
func TestCreateTaskDefaultPlannerWithoutKey(t *testing.T) {
	c := testController(t)
	c.mu.Lock()
	c.settings.DefaultPlanner = "claude"
	c.mu.Unlock()
	_, _, err := c.createTask(createReq{Prompt: "make a todo app"}, nil, createOpts{})
	if err == nil {
		t.Skip("a ready node exists in this test setup")
	}
	if got := err.Error(); got == "add a Claude API key in Settings, or pick a node as the planner" {
		t.Fatalf("stock default still demands a Claude key: %v", err)
	}
}

// Recommendations survive only when they are real options, one per single-choice question.
func TestCleanQuestionsRecommended(t *testing.T) {
	out := cleanQuestions([]Question{
		{Question: "Language?", Options: []string{"Python", "Go"}, Recommended: []string{"python", "Go", "Rust"}},
		{Question: "Extras?", Multi: true, Options: []string{"JSON", "Top-N", "None"}, Recommended: []string{"JSON", "Top-N", "Top-N", "Perl"}},
	})
	if len(out) != 2 {
		t.Fatalf("got %d questions", len(out))
	}
	if len(out[0].Recommended) != 1 || out[0].Recommended[0] != "Python" {
		t.Fatalf("single choice keeps one real option: %v", out[0].Recommended)
	}
	if len(out[1].Recommended) != 2 {
		t.Fatalf("multi keeps each real option once: %v", out[1].Recommended)
	}
}
