package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCleanAssumptions(t *testing.T) {
	in := []Assumption{
		{Topic: " Storage ", Choice: "SQLite", Options: []string{"JSON file", "sqlite", "none", ""}},
		{Topic: "", Choice: "x"},
		{Topic: "Interface", Choice: ""},
		{Topic: "Language", Choice: "Python", Options: []string{"Go", "Python", "JavaScript", "Rust", "Ruby", "Perl"}},
	}
	for i := 0; i < 6; i++ {
		in = append(in, Assumption{Topic: "T" + string(rune('a'+i)), Choice: "c"})
	}
	got := cleanAssumptions(in)
	if len(got) != 6 {
		t.Fatalf("kept %d, want 6", len(got))
	}
	if got[0].Topic != "Storage" || !reflect.DeepEqual(got[0].Options, []string{"SQLite", "JSON file", "none"}) {
		t.Fatalf("first: %+v", got[0])
	}
	if got[1].Topic != "Language" || len(got[1].Options) != 5 || got[1].Options[0] != "Python" {
		t.Fatalf("second: %+v", got[1])
	}
	if got := cleanAssumptions(nil); len(got) != 0 {
		t.Fatalf("nil: %+v", got)
	}
}

func TestPlanAssumptionsRoundTripAndSchema(t *testing.T) {
	var p Plan
	raw := `{"title":"T","summary":"S","subtasks":[],"assumptions":[{"topic":"Storage","choice":"SQLite","options":["SQLite","none"]}]}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil || len(p.Assumptions) != 1 || p.Assumptions[0].Choice != "SQLite" {
		t.Fatalf("%v %+v", err, p.Assumptions)
	}
	b, _ := json.Marshal(p)
	if !strings.Contains(string(b), `"assumptions"`) {
		t.Fatalf("not saved: %s", b)
	}
	var schema map[string]any
	if err := json.Unmarshal(planSchema, &schema); err != nil || !strings.Contains(string(planSchema), `"assumptions"`) {
		t.Fatalf("schema: %v", err)
	}
	// a plan without the field is still fine
	p = Plan{}
	if err := json.Unmarshal([]byte(`{"title":"T","summary":"S","subtasks":[]}`), &p); err != nil || len(p.Assumptions) != 0 {
		t.Fatalf("%v %+v", err, p.Assumptions)
	}
}

func TestPlannerPromptAsksOnlyWhenBlocked(t *testing.T) {
	for _, want := range []string{`"assumptions"`, "do not ask", "no sensible default", "at most 2 questions"} {
		if !strings.Contains(plannerSystem, want) {
			t.Fatalf("planner prompt lacks %q", want)
		}
	}
}
