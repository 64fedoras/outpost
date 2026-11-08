package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordStartCrashLoop(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if recordStart(dir, now) || recordStart(dir, now.Add(time.Minute)) {
		t.Fatal("two starts are not a crash loop")
	}
	if !recordStart(dir, now.Add(2*time.Minute)) {
		t.Fatal("third start within five minutes should enter safe mode")
	}
	clearStarts(dir)
	if recordStart(dir, now.Add(3*time.Minute)) {
		t.Fatal("history should be forgotten after a stable run")
	}
}

func TestRecordStartOldAndDamaged(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	recordStart(dir, now)
	recordStart(dir, now.Add(time.Minute))
	if recordStart(dir, now.Add(10*time.Minute)) {
		t.Fatal("old starts must not count")
	}
	os.WriteFile(filepath.Join(dir, "starts.json"), []byte("{not json"), 0o600)
	if recordStart(dir, now) {
		t.Fatal("a damaged file counts as no history")
	}
}

func TestCheckStalled(t *testing.T) {
	c := &Controller{tasks: map[string]*Task{}, dataDir: t.TempDir()}
	now := time.Now()
	old := &Task{ID: "t_old", Status: "running", Title: "x", dir: t.TempDir(), Created: now.Add(-time.Hour),
		Events: []TaskEvent{{Time: now.Add(-20 * time.Minute)}}}
	fresh := &Task{ID: "t_new", Status: "running", Title: "y", dir: t.TempDir(), Created: now.Add(-time.Hour),
		Events: []TaskEvent{{Time: now.Add(-time.Minute)}}}
	waiting := &Task{ID: "t_wait", Status: "awaiting_approval", dir: t.TempDir(), Created: now.Add(-time.Hour)}
	c.tasks["t_old"], c.tasks["t_new"], c.tasks["t_wait"] = old, fresh, waiting
	if n := c.checkStalled(now); n != 1 {
		t.Fatalf("marked %d, want 1", n)
	}
	if n := c.checkStalled(now); n != 0 {
		t.Fatalf("a second pass must not mark again, got %d", n)
	}
	if !old.Stalled || fresh.Stalled || waiting.Stalled {
		t.Fatal("wrong tasks marked")
	}
	old.mu.Lock()
	old.Events = append(old.Events, TaskEvent{Time: now})
	old.mu.Unlock()
	c.checkStalled(now)
	if old.Stalled {
		t.Fatal("mark should clear when the task shows life")
	}
}
