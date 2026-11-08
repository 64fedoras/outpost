package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTrialRollsBackAfterCrashLoop(t *testing.T) {
	dir := t.TempDir()
	sfx := exeSuffix(runtime.GOOS)
	for _, v := range []string{"0.1.0", Version} {
		os.WriteFile(filepath.Join(dir, "outpost-"+v+sfx), []byte("x"), 0o755)
	}
	// "0.1.0" must be older than Version for the rollback to be valid.
	beginTrial(dir, "0.1.0", Version)
	for i := 1; i <= trialMaxStarts; i++ {
		if _, ok := trialRollbackTarget(dir, Version); ok {
			t.Fatalf("start %d should still be allowed", i)
		}
	}
	tgt, ok := trialRollbackTarget(dir, Version)
	if !ok || tgt.version != "0.1.0" {
		t.Fatalf("expected rollback to 0.1.0, got %v %v", tgt, ok)
	}
	if readPin(dir) != "0.1.0" {
		t.Fatal("rollback should pin the old version")
	}
	if _, ok := readTrial(dir); ok {
		t.Fatal("trial should be over")
	}
}

func TestTrialIgnoredForOtherVersionAndClearedByRollback(t *testing.T) {
	dir := t.TempDir()
	beginTrial(dir, "0.1.0", "9.9.9")
	if _, ok := trialRollbackTarget(dir, Version); ok {
		t.Fatal("the old binary must not count starts of the new one")
	}
	if tr, _ := readTrial(dir); tr.Starts != 0 {
		t.Fatal("old binary changed the count")
	}
	beginTrial(dir, Version, Version) // no trial for a same-version install
	if tr, _ := readTrial(dir); tr.To != "9.9.9" {
		t.Fatal("same-version install must not replace the trial")
	}
}
