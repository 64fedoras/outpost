package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptWasCut(t *testing.T) {
	if promptWasCut("fine") || !promptWasCut("x\n…(truncated)\n") || !promptWasCut("(omitted to fit the context window)") {
		t.Fatal("cut detection wrong")
	}
}

func TestExistingFilesWarning(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "big.go"), []byte(strings.Repeat("x", 40000)), 0o644)
	spec := PlanSubtask{ID: "s1", Files: []string{"big.go", "new.go"}}
	if w := existingFilesWarning(dir, spec, 60000); w == "" {
		t.Fatal("40 KB into a 30 KB share should warn")
	}
	if w := existingFilesWarning(dir, spec, 200000); w != "" {
		t.Fatalf("fits, got %q", w)
	}
}
