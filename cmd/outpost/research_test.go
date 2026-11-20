package main

import (
	"strings"
	"testing"
)

func TestParseSubQuestions(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{`Sure! ["A one?", "B two?", "a one?", "C three?"]`, 3},              // prose around, a duplicate dropped
		{`[{"question": "X?"}, {"q": "Y?"}]`, 2},                             // objects
		{"1. What is A today?\n2. How does B compare?\n- not a question", 2}, // lines
		{`["1","2","3","4","5","6","7","8"]`, maxResearchLanes},              // capped
		{"no idea", 1}, // falls back to the question
	}
	for _, c := range cases {
		got := parseSubQuestions(c.in, "  the   question ")
		if len(got) != c.want {
			t.Errorf("%q: got %v", c.in, got)
		}
	}
	if got := parseSubQuestions("nothing", "the   question"); got[0] != "the question" {
		t.Errorf("fallback = %q", got[0])
	}
}

func TestResearchFindings(t *testing.T) {
	lanes := []ResearchLane{
		{Q: "Idle?", Findings: "- 2.7 W idle (https://example.org/a).\n- also (https://example.org/b)"},
		{Q: "Empty?", Status: "failed"},
		{Q: "Load?", Findings: "- 8 W (https://example.org/a)"},
	}
	f, src := researchFindings(lanes)
	if !strings.Contains(f, "## Idle?") || strings.Contains(f, "Empty?") || !strings.Contains(f, "## Load?") {
		t.Fatalf("findings:\n%s", f)
	}
	if src != "[1] https://example.org/a\n[2] https://example.org/b\n" {
		t.Fatalf("sources:\n%s", src)
	}
	lanes[0].Node, lanes[2].Node, lanes[0].Status, lanes[2].Status, lanes[0].Searches = "a", "b", "done", "done", 3
	if n := researchNote(&ResearchRun{Lanes: lanes}); !strings.Contains(n, "2 of 3 sub-questions answered on 2 node(s), 3 search(es)") {
		t.Fatal(n)
	}
}
