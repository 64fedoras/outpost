package main

import (
	"os"
	"testing"
)

// The test binary doubles as the sandbox child, the same way the real binary
// does (main.go dispatches "sandbox-child"). Node exec tests that run with the
// sandbox on re-execute this binary, so without this they would re-run the
// whole suite instead of the child.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "sandbox-child" {
		runSandboxChild(os.Args[2:])
		return
	}
	os.Exit(m.Run())
}

func TestWithSandboxNote(t *testing.T) {
	out := &ExecOutput{SandboxDenied: true, SandboxNote: "net off"}
	if got := withSandboxNote("boom", out); got == "boom" {
		t.Errorf("note missing: %q", got)
	}
	if withSandboxNote("boom", &ExecOutput{}) != "boom" || withSandboxNote("boom", nil) != "boom" {
		t.Error("no note expected when the sandbox did not block anything")
	}
}
