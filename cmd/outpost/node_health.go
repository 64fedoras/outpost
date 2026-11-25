package main

// Node health score: how often a node has dropped its connection, hung or
// handed back work in the last week, shown as green, amber or red on its card.
// Red nodes are put last when work is handed out. The record is kept in
// health.json in the controller's data folder (small, trimmed to a week).

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	healthWindow = 7 * 24 * time.Hour
	healthAmber  = 5
	healthRed    = 15
)

// healthWeights are the points an event adds to a node's score.
var healthWeights = map[string]int{"disconnect": 1, "problem": 2}

type healthEvent struct {
	T    time.Time `json:"t"`
	Kind string    `json:"kind"`
}

type healthLog struct {
	mu     sync.Mutex
	path   string
	events map[string][]healthEvent
	saved  time.Time
	amber  int // points for amber and red, from Settings (healthAmberOf, healthRedOf)
	red    int
}

func healthAmberOf(s Settings) int {
	if s.HealthAmber > 0 {
		return s.HealthAmber
	}
	return healthAmber
}

func healthRedOf(s Settings) int {
	if s.HealthRed > 0 {
		return s.HealthRed
	}
	return healthRed
}

// setThresholds changes the colour rule; the next score uses it.
func (h *healthLog) setThresholds(amber, red int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.amber, h.red = amber, red
}

func (c *Controller) healthPath() string { return filepath.Join(c.dataDir, "health.json") }

// healthLog loads the log on first use. It never takes c.mu, so it is safe to
// call with c.mu held.
func (c *Controller) healthLog() *healthLog {
	c.healthOnce.Do(func() {
		h := &healthLog{path: c.healthPath(), events: map[string][]healthEvent{}, amber: healthAmber, red: healthRed}
		if b, err := os.ReadFile(h.path); err == nil {
			_ = json.Unmarshal(b, &h.events)
		}
		c.health = h
	})
	return c.health
}

// recordHealth notes a bad event for a node.
func (c *Controller) recordHealth(node, kind string) {
	h := c.healthLog()
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	h.events[node] = append(trimHealth(h.events[node], now), healthEvent{now, kind})
	if now.Sub(h.saved) > time.Minute {
		h.saved = now
		if b, err := json.Marshal(h.events); err == nil {
			if err := writeFileAtomic(h.path, b, 0o600); err != nil {
				log.Printf("saving health.json: %v", err)
			}
		}
	}
}

// plannedRestartWindow is how long after an update, rollback or restart
// request a dropped connection still counts as planned.
const plannedRestartWindow = 10 * time.Minute

// plannedRestartLocked reports whether a node's dropped connection was a
// restart someone asked for, which must not lower its health score: an
// Outpost update or rollback (the node sends "restarting" just before it goes,
// or has already come back on the new version), a restart from the web UI,
// or the node closing with "service restart" (restart from the controller or
// the tray icon).
func plannedRestartLocked(u *UpdateStatus, restartAsked time.Time, code websocket.StatusCode, now time.Time) bool {
	if code == websocket.StatusServiceRestart {
		return true
	}
	if !restartAsked.IsZero() && now.Sub(restartAsked) < plannedRestartWindow {
		return true
	}
	if u != nil && (u.State == "restarting" || u.State == "done") && now.Sub(u.Updated) < plannedRestartWindow {
		return true
	}
	return false
}

func trimHealth(ev []healthEvent, now time.Time) []healthEvent {
	i := 0
	for i < len(ev) && now.Sub(ev[i].T) > healthWindow {
		i++
	}
	return ev[i:]
}

// healthOf returns the score, a colour ("green", "amber", "red") and a
// sentence for the card.
func (c *Controller) healthOf(node string) (int, string, string) {
	h := c.healthLog()
	h.mu.Lock()
	defer h.mu.Unlock()
	return scoreHealth(h.events[node], time.Now(), h.amber, h.red)
}

func scoreHealth(ev []healthEvent, now time.Time, amber, red int) (int, string, string) {
	score, disc, prob := 0, 0, 0
	for _, e := range trimHealth(ev, now) {
		score += healthWeights[e.Kind]
		switch e.Kind {
		case "disconnect":
			disc++
		case "problem":
			prob++
		}
	}
	colour := "green"
	switch {
	case score >= red:
		colour = "red"
	case score >= amber:
		colour = "amber"
	}
	note := ""
	if colour != "green" {
		note = fmt.Sprintf("Unreliable this week: %d disconnect(s), %d time(s) it could not take or finish work.", disc, prob)
	}
	return score, colour, note
}

// resetHealth forgets a node's recent events, so its badge goes back to green.
func (c *Controller) resetHealth(node string) {
	h := c.healthLog()
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.events, node)
	if b, err := json.Marshal(h.events); err == nil {
		if err := writeFileAtomic(h.path, b, 0o600); err != nil {
			log.Printf("saving health.json: %v", err)
		}
	}
	h.saved = time.Now()
}

func (c *Controller) handleResetHealth(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c.mu.Lock()
	_, ok := c.nodes[id]
	c.mu.Unlock()
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "no such node"})
		return
	}
	c.resetHealth(id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
