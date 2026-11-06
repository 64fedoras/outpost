package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiskLowRule(t *testing.T) {
	const G = 1 << 30
	cases := []struct {
		free, total uint64
		low, ok     bool
	}{
		{50 * G, 100 * G, false, true},
		{9 * G, 100 * G, true, false},
		{12 * G, 100 * G, false, false}, // between 10 and 15 %: neither starts nor clears the warning
		{4 * G, 20 * G, true, false},    // 20 % free but under 5 GB
		{0, 0, false, false},            // unknown
	}
	for _, c := range cases {
		if diskLow(c.free, c.total) != c.low || diskRecovered(c.free, c.total) != c.ok {
			t.Errorf("free %d of %d: low %v recovered %v", c.free/G, c.total/G, diskLow(c.free, c.total), diskRecovered(c.free, c.total))
		}
	}
}

func TestCleanupKeepsRecentAndUnfinished(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), tasks: map[string]*Task{}}
	mk := func(id, status string, age time.Duration) *Task {
		dir := filepath.Join(c.dataDir, "tasks", id)
		for _, sub := range []string{"rounds/1", "shots", "artifacts", "repo"} {
			os.MkdirAll(filepath.Join(dir, sub), 0o700)
		}
		os.WriteFile(filepath.Join(dir, "rounds/1/a.go"), make([]byte, 1000), 0o600)
		os.WriteFile(filepath.Join(dir, "shots/s.png"), make([]byte, 500), 0o600)
		os.WriteFile(filepath.Join(dir, "artifacts/bin"), make([]byte, 2000), 0o600)
		os.WriteFile(filepath.Join(dir, "repo/main.go"), []byte("package main"), 0o600)
		os.WriteFile(filepath.Join(dir, "result.zip"), make([]byte, 100), 0o600)
		ended := time.Now().Add(-age)
		t := &Task{ID: id, Status: status, Ended: &ended, dir: dir, Artifacts: []Artifact{{Path: "bin"}}}
		c.tasks[id] = t
		return t
	}
	old := mk("old", "done", 40*24*time.Hour)
	recent := mk("recent", "done", 2*24*time.Hour)
	running := mk("running", "running", 40*24*time.Hour)

	freed, n := c.cleanupTasks(30, false, true)
	if n != 1 || freed != 1600 {
		t.Fatalf("dry run: %d bytes from %d tasks", freed, n)
	}
	if _, err := os.Stat(filepath.Join(old.dir, "rounds")); err != nil {
		t.Fatal("dry run removed files")
	}
	freed, n = c.cleanupTasks(30, true, false)
	if n != 1 || freed != 3600 {
		t.Fatalf("clean-up: %d bytes from %d tasks", freed, n)
	}
	for _, gone := range []string{"rounds", "shots", "artifacts", "result.zip"} {
		if _, err := os.Stat(filepath.Join(old.dir, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s kept", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(old.dir, "repo", "main.go")); err != nil {
		t.Fatal("the task's files must stay")
	}
	if old.Cleaned == nil || old.Artifacts != nil {
		t.Fatalf("task not marked: %+v", old.Cleaned)
	}
	for _, kept := range []*Task{recent, running} {
		if _, err := os.Stat(filepath.Join(kept.dir, "rounds")); err != nil || kept.Cleaned != nil {
			t.Fatalf("%s was cleaned", kept.ID)
		}
	}
	old.flush()
}
