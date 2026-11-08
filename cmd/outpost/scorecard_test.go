package main

import "testing"

func TestScorecardCountsOnce(t *testing.T) {
	c := &Controller{dataDir: t.TempDir()}
	tk := &Task{ID: "t1", dir: t.TempDir(), Plan: &Plan{Language: "Go"}, TestStatus: "passed",
		Subtasks: []*SubtaskState{
			{ID: "s1", Status: "done", Model: "qwen", Attempts: 1},
			{ID: "s2", Status: "done", Model: "qwen", Attempts: 3},
			{ID: "s3", Status: "done", Model: "qwen", Attempts: 1, ReviewFix: true},
			{ID: "s4", Status: "done", ByPlanner: true, Model: "x"},
		}}
	c.recordScorecard(tk)
	c.recordScorecard(tk) // a follow-up round must not count them again
	rows := c.cardLog().list()
	if len(rows) != 1 {
		t.Fatalf("rows: %+v", rows)
	}
	r := rows[0]
	if r.Model != "qwen" || r.Language != "go" || r.Subtasks != 3 || r.Clean != 1 || r.Retries != 2 || r.ReviewFix != 1 {
		t.Fatalf("row: %+v", r)
	}
}

func TestScorecardNoCleanWhenTestsNeededFixes(t *testing.T) {
	c := &Controller{dataDir: t.TempDir()}
	tk := &Task{ID: "t1", dir: t.TempDir(), Plan: &Plan{Language: "go"}, FixRounds: 2,
		Subtasks: []*SubtaskState{{ID: "s1", Status: "done", Model: "m", Attempts: 1}}}
	c.recordScorecard(tk)
	if r := c.cardLog().list()[0]; r.Clean != 0 {
		t.Fatalf("a task that needed fix rounds has no clean subtasks: %+v", r)
	}
}
