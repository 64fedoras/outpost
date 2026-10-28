package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanQuestions(t *testing.T) {
	in := []Question{
		{Question: "  Language? ", Options: []string{"Python", " ", "Go"}},
		{Question: ""},
		{Question: "a"}, {Question: "b"}, {Question: "c"}, {Question: "d"},
	}
	out := cleanQuestions(in)
	if len(out) != 4 {
		t.Fatalf("want 4 questions, got %d", len(out))
	}
	if out[0].Question != "Language?" || len(out[0].Options) != 2 || out[0].ID != "q1" {
		t.Fatalf("first question not cleaned: %+v", out[0])
	}
}

func TestPlannerPromptFollowUp(t *testing.T) {
	dir := t.TempDir()
	task := &Task{dir: dir, Round: 2}
	os.MkdirAll(filepath.Join(dir, "repo"), 0o755)
	os.WriteFile(filepath.Join(dir, "repo", "main.py"), []byte("print('hi')\n"), 0o644)
	task.Messages = []TaskMessage{
		{Role: "user", Kind: "text", Text: "Build a greeter", Round: 1},
		{Role: "assistant", Kind: "plan", Plan: &Plan{Title: "Greeter", Summary: "says hi", TestCommand: "python3 -m unittest"}, Round: 1},
		{Role: "user", Kind: "steer", Text: "be polite", Round: 1},
		{Role: "user", Kind: "text", Text: "Add a --name flag", Round: 2},
	}
	c := &Controller{}
	p := c.plannerPrompt(task, 100000)
	for _, want := range []string{"follow-up request", "Greeter", "Build a greeter", "<<<FILE main.py>>>", "User: Add a --name flag"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "be polite") {
		t.Errorf("steering notes from an earlier round leaked into the planner prompt")
	}
}

func TestSteeringText(t *testing.T) {
	task := &Task{}
	if task.steeringText() != "" {
		t.Fatal("no notes should give no text")
	}
	task.Steering = []string{"use SQLite"}
	if s := task.steeringText(); !strings.Contains(s, "- use SQLite") {
		t.Fatalf("got %q", s)
	}
}

func TestRoundSnapshotAndUndo(t *testing.T) {
	dir := t.TempDir()
	task := &Task{dir: dir, Round: 1, Status: "done"}
	repo := task.repoDir()
	os.MkdirAll(repo, 0o755)
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644)

	// Round 1 changes a.txt twice (only the first copy is kept) and adds sub/b.txt.
	for _, w := range [][2]string{{"a.txt", "two\n"}, {"a.txt", "three\n"}, {"sub/b.txt", "new\n"}} {
		if err := task.writeFile(w[0], w[1]); err != nil {
			t.Fatal(err)
		}
	}
	m := readManifest(task.roundDir(1))
	if m.Files["a.txt"] != "modified" || m.Files["sub/b.txt"] != "added" || len(m.Files) != 2 {
		t.Fatalf("manifest wrong: %+v", m)
	}
	kept, _ := os.ReadFile(filepath.Join(task.roundDir(1), "orig", "a.txt"))
	if string(kept) != "one\n" {
		t.Fatalf("kept copy should be the original, got %q", kept)
	}
	if err := task.writeFile("../escape.txt", "x"); err == nil {
		t.Fatal("path escape should be refused")
	}

	n, err := task.undoRound(1)
	if err != nil || n != 2 {
		t.Fatalf("undo: n=%d err=%v", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(b) != "one\n" {
		t.Fatalf("a.txt not restored: %q", b)
	}
	if _, err := os.Stat(filepath.Join(repo, "sub")); !os.IsNotExist(err) {
		t.Fatal("added file and its empty folder should be gone")
	}
	if len(task.roundNumbers()) != 0 {
		t.Fatal("round copy should be removed after undo")
	}
}
