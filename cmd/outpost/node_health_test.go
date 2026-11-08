package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScoreHealth(t *testing.T) {
	now := time.Now()
	ev := []healthEvent{{now.Add(-10 * 24 * time.Hour), "problem"}}
	if _, col, _ := scoreHealth(ev, now); col != "green" {
		t.Fatal("old events must not count")
	}
	for i := 0; i < 3; i++ {
		ev = append(ev, healthEvent{now.Add(-time.Hour), "problem"})
	}
	if s, col, note := scoreHealth(ev, now); s != 6 || col != "amber" || note == "" {
		t.Fatalf("got %d %s %q", s, col, note)
	}
	for i := 0; i < 10; i++ {
		ev = append(ev, healthEvent{now, "disconnect"})
	}
	if _, col, _ := scoreHealth(ev, now); col != "red" {
		t.Fatal("expected red")
	}
}

func TestRecordHealthWhileLocked(t *testing.T) {
	c := &Controller{dataDir: t.TempDir()}
	c.mu.Lock()
	c.healthOf("n1") // must not deadlock with c.mu held
	c.mu.Unlock()
	c.recordHealth("n1", "problem")
	if s, _, _ := c.healthOf("n1"); s != 2 {
		t.Fatalf("score %d", s)
	}
}

func TestResetHealth(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), nodes: map[string]*NodeRec{"n1": {ID: "n1"}}}
	for i := 0; i < 6; i++ {
		c.recordHealth("n1", "problem")
	}
	if _, col, _ := c.healthOf("n1"); col == "green" {
		t.Fatal("expected amber before reset")
	}
	c.resetHealth("n1")
	if s, col, _ := c.healthOf("n1"); s != 0 || col != "green" {
		t.Fatalf("after reset: %d %s", s, col)
	}
	if b, err := os.ReadFile(filepath.Join(c.dataDir, "health.json")); err != nil || strings.Contains(string(b), "problem") {
		t.Fatalf("reset not saved: %v %s", err, b)
	}
}
