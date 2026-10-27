package main

import (
	"os"
	"path/filepath"
	"testing"
)

// These tests run with Version = the current build. Fake copies are named
// relative to it: "older" < Version < "newer".
func fakeCopies(t *testing.T, versions ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, v := range versions {
		if err := os.WriteFile(filepath.Join(dir, agentBinaryName(v)), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const older, newer = "0.0.9", "99.0.0"

func TestSelectNewest(t *testing.T) {
	dir := fakeCopies(t, older, newer)
	c, ok := selectTarget(dir)
	if !ok || c.version != newer {
		t.Fatalf("got %v %v", c, ok)
	}
	dir2 := fakeCopies(t, older)
	if _, ok := selectTarget(dir2); ok {
		t.Fatal("switched to an older copy without a pin")
	}
}

func TestRollbackPinsAndHolds(t *testing.T) {
	dir := fakeCopies(t, older, Version, newer)
	if prev := previousVersion(dir); prev != older {
		t.Fatalf("previousVersion=%q", prev)
	}
	target, err := prepareRollback(dir, older)
	if err != nil || target.version != older {
		t.Fatal(target, err)
	}
	// Newer copies are set aside so pre-0.3.1 versions do not jump forward.
	for _, v := range []string{Version, newer} {
		if _, err := os.Stat(filepath.Join(heldDir(dir), agentBinaryName(v))); err != nil {
			t.Errorf("%s not held: %v", v, err)
		}
	}
	// A process of this version starting now goes to the pinned older copy.
	c, ok := selectTarget(dir)
	if !ok || c.version != older {
		t.Fatalf("after rollback got %v %v", c, ok)
	}
}

func TestUpdateAfterRollbackClearsPin(t *testing.T) {
	dir := fakeCopies(t, older, newer)
	if _, err := prepareRollback(dir, older); err != nil {
		t.Fatal(err)
	}
	// An older version installs an update (it does not know about the pin).
	if err := os.WriteFile(filepath.Join(dir, agentBinaryName(newer)), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, ok := selectTarget(dir)
	if !ok || c.version != newer {
		t.Fatalf("stale pin kept: %v %v", c, ok)
	}
	if readPin(dir) != "" {
		t.Error("pin not cleared")
	}
}

func TestRollbackRefusesNewerOrMissing(t *testing.T) {
	dir := fakeCopies(t, newer)
	if _, err := prepareRollback(dir, newer); err == nil {
		t.Error("rolled back to a newer version")
	}
	if _, err := prepareRollback(dir, "0.0.1"); err == nil {
		t.Error("rolled back to a version that is not installed")
	}
}

func TestPrunKeepsPinned(t *testing.T) {
	dir := fakeCopies(t, "0.0.1", "0.0.2", "0.0.3", "0.0.4", "0.0.5")
	if err := os.WriteFile(pinPath(dir), []byte("0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pruneAgentDir(dir, 3)
	got := map[string]bool{}
	for _, c := range installedCopies(dir) {
		got[c.version] = true
	}
	if !got["0.0.1"] || !got["0.0.5"] || got["0.0.2"] {
		t.Errorf("after prune: %v", got)
	}
}

func TestMissingLibMessage(t *testing.T) {
	m := missingLibRe.FindSubmatch([]byte("/x/llama-server: error while loading shared libraries: libgomp.so.1: cannot open shared object file"))
	if m == nil || string(m[1]) != "libgomp.so.1" {
		t.Fatalf("got %q", m)
	}
}
