package main

// Controller disk guard: how the data folder's disk is used, a warning (in the
// web UI and as a notification) when it runs low, and an optional daily
// clean-up of what old finished tasks no longer need. Conversations, the
// task's files and attachments are always kept.

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	diskLowPct     = 10      // warn below this share of the disk free
	diskLowBytes   = 5 << 30 // or below this many bytes, whichever comes first
	diskClearPct   = 15      // the warning clears above this share
	storageMaxAge  = 10 * time.Minute
	cleanupMaxDays = 3650
)

// storageInfo is GET /api/storage.
type storageInfo struct {
	Free     uint64           `json:"free"`
	Total    uint64           `json:"total"`
	Low      bool             `json:"low"`
	Parts    map[string]int64 `json:"parts"` // tasks, models, releases, runtime, projects, other
	Measured time.Time        `json:"measured"`
	// Clean-up settings and what the last run did.
	CleanupDays   int        `json:"cleanup_days"`
	CleanupBuilds bool       `json:"cleanup_builds"`
	LastCleanup   *time.Time `json:"last_cleanup,omitempty"`
	LastFreed     int64      `json:"last_freed"`
	LastTasks     int        `json:"last_tasks"`
	// Would be freed now with the current settings (0 when clean-up is off).
	Reclaimable int64 `json:"reclaimable"`
}

type storageState struct {
	mu          sync.Mutex
	parts       map[string]int64
	measured    time.Time
	low         bool // the warning is showing (hysteresis: set below 10 %, cleared above 15 %)
	lastCleanup *time.Time
	lastFreed   int64
	lastTasks   int
}

// measureStorage sizes the data folder's parts. It walks the folder, so it is
// cached for storageMaxAge.
func (c *Controller) measureStorage(force bool) (map[string]int64, time.Time) {
	c.storage.mu.Lock()
	if !force && c.storage.parts != nil && time.Since(c.storage.measured) < storageMaxAge {
		p, m := c.storage.parts, c.storage.measured
		c.storage.mu.Unlock()
		return p, m
	}
	c.storage.mu.Unlock()
	parts := map[string]int64{}
	known := map[string]string{"tasks": "tasks", "models": "models", "releases": "releases", "runtime": "runtime", "projects": "projects", "agent": "releases"}
	ents, _ := os.ReadDir(c.dataDir)
	for _, e := range ents {
		p := filepath.Join(c.dataDir, e.Name())
		var size int64
		if e.IsDir() {
			size = dirSize(p)
		} else if fi, err := e.Info(); err == nil {
			size = fi.Size()
		}
		key := known[e.Name()]
		if key == "" {
			key = "other"
		}
		parts[key] += size
	}
	now := time.Now()
	c.storage.mu.Lock()
	c.storage.parts, c.storage.measured = parts, now
	c.storage.mu.Unlock()
	return parts, now
}

func (c *Controller) storageView(force bool) storageInfo {
	parts, measured := c.measureStorage(force)
	c.mu.Lock()
	days, builds := c.settings.CleanupDays, c.settings.CleanupBuilds
	c.mu.Unlock()
	c.storage.mu.Lock()
	v := storageInfo{Free: diskFree(c.dataDir), Total: diskTotal(c.dataDir), Low: c.storage.low, Parts: parts, Measured: measured,
		CleanupDays: days, CleanupBuilds: builds, LastCleanup: c.storage.lastCleanup, LastFreed: c.storage.lastFreed, LastTasks: c.storage.lastTasks}
	c.storage.mu.Unlock()
	if days > 0 {
		v.Reclaimable, _ = c.cleanupTasks(days, builds, true)
	}
	return v
}

// diskLow is the rule for the warning: under 10 % or under 5 GB free.
func diskLow(free, total uint64) bool {
	if total == 0 {
		return false
	}
	return free*100 < total*diskLowPct || free < diskLowBytes
}

func diskRecovered(free, total uint64) bool {
	return total > 0 && free*100 >= total*diskClearPct && free >= diskLowBytes
}

// checkDisk updates the warning and sends a notification when it first shows.
func (c *Controller) checkDisk() {
	free, total := diskFree(c.dataDir), diskTotal(c.dataDir)
	c.storage.mu.Lock()
	was := c.storage.low
	switch {
	case !was && diskLow(free, total):
		c.storage.low = true
	case was && diskRecovered(free, total):
		c.storage.low = false
	}
	now := c.storage.low
	c.storage.mu.Unlock()
	if now && !was {
		log.Printf("disk: only %s of %s free for the controller's data", humanBytes(int64(free)), humanBytes(int64(total)))
		go c.deliverNotification("disk_low", "Controller disk low", "Only "+humanBytes(int64(free))+" of "+humanBytes(int64(total))+" is free on the controller. Tasks and model downloads may fail; see Settings → Storage.", "")
	}
}

// storageLoop checks the disk every 10 minutes and runs the clean-up once a day.
func (c *Controller) storageLoop() {
	var lastClean time.Time
	for {
		c.checkDisk()
		if time.Since(lastClean) > 24*time.Hour {
			c.mu.Lock()
			days, builds := c.settings.CleanupDays, c.settings.CleanupBuilds
			c.mu.Unlock()
			if days > 0 {
				lastClean = time.Now()
				c.runCleanup(days, builds)
			}
		}
		time.Sleep(10 * time.Minute)
	}
}

func (c *Controller) runCleanup(days int, builds bool) (int64, int) {
	freed, n := c.cleanupTasks(days, builds, false)
	now := time.Now()
	c.storage.mu.Lock()
	c.storage.lastCleanup, c.storage.lastFreed, c.storage.lastTasks = &now, freed, n
	c.storage.parts = nil // measure again
	c.storage.mu.Unlock()
	if n > 0 {
		log.Printf("clean-up: freed %s from %d finished task(s) older than %d days", humanBytes(freed), n, days)
	}
	c.checkDisk()
	return freed, n
}

// cleanupTasks removes round copies, screenshots and the cached result zip
// (rebuilt on download) of tasks that finished more than days ago, and with
// builds also their build outputs. dry only adds up what would go.
func (c *Controller) cleanupTasks(days int, builds, dry bool) (int64, int) {
	cut := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	c.mu.Lock()
	tasks := make([]*Task, 0, len(c.tasks))
	for _, t := range c.tasks {
		tasks = append(tasks, t)
	}
	c.mu.Unlock()
	var freed int64
	n := 0
	for _, t := range tasks {
		t.mu.Lock()
		old := (t.Status == "done" || t.Status == "failed" || t.Status == "cancelled") && t.Ended != nil && t.Ended.Before(cut)
		dir := t.dir
		t.mu.Unlock()
		if !old || dir == "" {
			continue
		}
		paths := []string{filepath.Join(dir, "rounds"), filepath.Join(dir, "shots"), filepath.Join(dir, "result.zip")}
		if builds {
			paths = append(paths, filepath.Join(dir, "artifacts"))
		}
		var got int64
		for _, p := range paths {
			fi, err := os.Stat(p)
			if err != nil {
				continue
			}
			sz := fi.Size()
			if fi.IsDir() {
				sz = dirSize(p)
			}
			if !dry {
				if err := os.RemoveAll(p); err != nil {
					log.Printf("clean-up %s: %v", p, err)
					continue
				}
			}
			got += sz
		}
		if got == 0 {
			continue
		}
		freed += got
		n++
		if !dry {
			t.mu.Lock()
			now := time.Now()
			t.Cleaned = &now
			if builds {
				t.Artifacts = nil
			}
			t.mu.Unlock()
			t.logf("info", "Clean-up removed %s of old round copies%s (Settings → Storage).", humanBytes(got), map[bool]string{true: ", screenshots and build outputs", false: " and screenshots"}[builds])
		}
	}
	return freed, n
}

func (c *Controller) handleStorage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, c.storageView(r.URL.Query().Get("refresh") != ""))
}

// handleStorageSettings saves the clean-up settings and, with run, cleans up now.
// POST /api/storage {"cleanup_days":30,"cleanup_builds":false,"run":true}
func (c *Controller) handleStorageSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Days   int  `json:"cleanup_days"`
		Builds bool `json:"cleanup_builds"`
		Run    bool `json:"run"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	if req.Days < 0 || req.Days > cleanupMaxDays {
		writeJSON(w, 400, map[string]string{"error": "days must be 0 (never) or more"})
		return
	}
	c.mu.Lock()
	c.settings.CleanupDays, c.settings.CleanupBuilds = req.Days, req.Builds
	err := c.saveSettingsLocked()
	c.mu.Unlock()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if req.Run && req.Days > 0 {
		c.runCleanup(req.Days, req.Builds)
	}
	writeJSON(w, 200, c.storageView(false))
}

// diskWarning is shown at the top of the web UI while the disk is low.
func (c *Controller) diskWarning() string {
	c.storage.mu.Lock()
	low := c.storage.low
	c.storage.mu.Unlock()
	if !low {
		return ""
	}
	return "Only " + humanBytes(int64(diskFree(c.dataDir))) + " is free on the controller's disk. Tasks, uploads and model downloads may fail. See Settings → Storage."
}
