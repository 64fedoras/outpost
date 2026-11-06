package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateFileOps(t *testing.T) {
	ok := &Plan{Subtasks: []PlanSubtask{{ID: "t1", Title: "x", Files: []string{"new.go"}, Instructions: "i"}},
		Delete: []string{"./old.go", "old.go", " "}, Rename: []PlanRename{{From: "a.go", To: "b/a.go"}, {From: "same", To: "same"}}}
	if err := validatePlan(ok); err != nil {
		t.Fatal(err)
	}
	if len(ok.Delete) != 1 || ok.Delete[0] != "old.go" || len(ok.Rename) != 1 {
		t.Fatalf("not cleaned: %+v %+v", ok.Delete, ok.Rename)
	}
	for name, p := range map[string]*Plan{
		"delete a written file": {Subtasks: []PlanSubtask{{ID: "t1", Title: "x", Files: []string{"a.go"}}}, Delete: []string{"a.go"}},
		"escape":                {Delete: []string{"../etc/passwd"}},
		"rename away a written": {Subtasks: []PlanSubtask{{ID: "t1", Title: "x", Files: []string{"a.go"}}}, Rename: []PlanRename{{From: "a.go", To: "c.go"}}},
		"twice":                 {Delete: []string{"a.go"}, Rename: []PlanRename{{From: "a.go", To: "c.go"}}},
	} {
		if err := validatePlan(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := validatePlan(&Plan{Delete: []string{"junk.txt"}}); err != nil {
		t.Fatalf("a plan that only deletes must be allowed: %v", err)
	}
}

func TestApplyFileOpsAndUndo(t *testing.T) {
	dir := t.TempDir()
	tk := &Task{ID: "t", Round: 2, dir: dir}
	repo := tk.repoDir()
	os.MkdirAll(filepath.Join(repo, "pkg"), 0o755)
	os.WriteFile(filepath.Join(repo, "old.go"), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(repo, "pkg", "a.go"), []byte("package a"), 0o644)
	plan := &Plan{Delete: []string{"old.go", "missing.go"}, Rename: []PlanRename{{From: "pkg/a.go", To: "lib/a.go"}}}
	c := &Controller{}
	if err := c.applyFileOps(tk, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "old.go")); !os.IsNotExist(err) {
		t.Fatal("old.go not deleted")
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "lib", "a.go")); string(b) != "package a" {
		t.Fatalf("renamed content %q", b)
	}
	if _, err := os.Stat(filepath.Join(repo, "pkg")); !os.IsNotExist(err) {
		t.Fatal("empty folder left behind")
	}
	// Running it again (a resumed round) changes nothing.
	if err := c.applyFileOps(tk, plan); err != nil {
		t.Fatal(err)
	}
	m := readManifest(tk.roundDir(2))
	b, _ := json.Marshal(m.Files)
	if m.Files["old.go"] != "deleted" || m.Files["pkg/a.go"] != "deleted" || m.Files["lib/a.go"] != "added" {
		t.Fatalf("manifest %s", b)
	}
	if _, err := tk.undoRound(2); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "old.go")); string(b) != "old" {
		t.Fatal("undo did not restore old.go")
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "pkg", "a.go")); string(b) != "package a" {
		t.Fatal("undo did not restore pkg/a.go")
	}
	if _, err := os.Stat(filepath.Join(repo, "lib", "a.go")); !os.IsNotExist(err) {
		t.Fatal("undo left the renamed copy")
	}
	if !strings.Contains(string(planSchema), "rename") || !json.Valid(planSchema) {
		t.Fatal("plan schema")
	}
	tk.flush()
}
