package main

// Safe mode: when the controller has started three times in five minutes it is
// probably crashing on something it loads (a task, a schedule, an auto-resume).
// It then starts with auto-resume and schedules switched off and says so in the
// banner at the top of the web UI, so a bad task file cannot keep it in a
// crash loop. Start times live in starts.json in the data folder; the file is
// emptied once a start has stayed up for five minutes.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	crashLoopStarts = 3
	crashLoopWindow = 5 * time.Minute
)

// recordStart notes this start in dir/starts.json and says whether it is the
// crashLoopStarts-th within crashLoopWindow.
func recordStart(dir string, now time.Time) bool {
	path := filepath.Join(dir, "starts.json")
	var all []time.Time
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &all) // a damaged file counts as no history
	}
	recent := all[:0]
	for _, t := range all {
		if d := now.Sub(t); d >= 0 && d < crashLoopWindow {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	if b, err := json.Marshal(recent); err == nil {
		_ = writeFileAtomic(path, b, 0o600)
	}
	return len(recent) >= crashLoopStarts
}

// clearStarts forgets the recorded starts; called after a stable run.
func clearStarts(dir string) { _ = os.Remove(filepath.Join(dir, "starts.json")) }

// checkSafeMode is called once at start-up. In safe mode it sets c.safeMode
// and adds the banner note; either way it schedules the all-clear.
func (c *Controller) checkSafeMode() {
	if recordStart(c.dataDir, time.Now()) {
		c.safeMode = true
		c.noteRecovered("Safe mode: the controller started " + "3 times in 5 minutes, so automatic resume and scheduled tasks are off for this run. Look at the log for the cause, then restart; tasks can still be resumed by hand.")
	}
	time.AfterFunc(crashLoopWindow, func() { clearStarts(c.dataDir) })
}
