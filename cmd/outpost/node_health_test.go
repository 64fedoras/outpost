package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestScoreHealth(t *testing.T) {
	now := time.Now()
	ev := []healthEvent{{now.Add(-10 * 24 * time.Hour), "problem"}}
	if _, col, _ := scoreHealth(ev, now, healthAmber, healthRed); col != "green" {
		t.Fatal("old events must not count")
	}
	for i := 0; i < 3; i++ {
		ev = append(ev, healthEvent{now.Add(-time.Hour), "problem"})
	}
	if s, col, note := scoreHealth(ev, now, healthAmber, healthRed); s != 6 || col != "amber" || note == "" {
		t.Fatalf("got %d %s %q", s, col, note)
	}
	for i := 0; i < 10; i++ {
		ev = append(ev, healthEvent{now, "disconnect"})
	}
	if _, col, _ := scoreHealth(ev, now, healthAmber, healthRed); col != "red" {
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

func TestHealthThresholdsFromSettings(t *testing.T) {
	if healthAmberOf(Settings{}) != healthAmber || healthRedOf(Settings{}) != healthRed {
		t.Fatal("defaults not used when settings are empty")
	}
	if healthAmberOf(Settings{HealthAmber: 2}) != 2 || healthRedOf(Settings{HealthRed: 4}) != 4 {
		t.Fatal("settings ignored")
	}
	now := time.Now()
	ev := []healthEvent{{now, "problem"}, {now, "problem"}}
	if _, col, _ := scoreHealth(ev, now, 4, 8); col != "amber" {
		t.Fatalf("4 points with amber at 4 should be amber, got %s", col)
	}
	if _, col, _ := scoreHealth(ev, now, 2, 4); col != "red" {
		t.Fatalf("4 points with red at 4 should be red, got %s", col)
	}
}

func TestPlannedRestart(t *testing.T) {
	now := time.Now()
	recent, old := now.Add(-time.Minute), now.Add(-time.Hour)
	cases := []struct {
		name    string
		u       *UpdateStatus
		asked   time.Time
		code    websocket.StatusCode
		planned bool
	}{
		{"plain drop", nil, time.Time{}, -1, false},
		{"node said service restart", nil, time.Time{}, websocket.StatusServiceRestart, true},
		{"restart asked from the UI", nil, recent, -1, true},
		{"restart asked long ago", nil, old, -1, false},
		{"update restarting", &UpdateStatus{State: "restarting", Updated: recent}, time.Time{}, -1, true},
		{"just came back from update", &UpdateStatus{State: "done", Updated: recent}, time.Time{}, -1, true},
		{"update done long ago", &UpdateStatus{State: "done", Updated: old}, time.Time{}, -1, false},
		{"update failed", &UpdateStatus{State: "failed", Updated: recent}, time.Time{}, -1, false},
		{"still downloading", &UpdateStatus{State: "downloading", Updated: recent}, time.Time{}, -1, false},
	}
	for _, c := range cases {
		if got := plannedRestartLocked(c.u, c.asked, c.code, now); got != c.planned {
			t.Errorf("%s: planned = %v, want %v", c.name, got, c.planned)
		}
	}
}
