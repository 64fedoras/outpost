package main

// Node health score: how often a node has dropped its connection, hung or
// handed back work in the last week, shown as green, amber or red on its card.
// Red nodes are put last when work is handed out. The record is kept in
// health.json in the controller's data folder (small, trimmed to a week).

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
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
}

func (c *Controller) healthPath() string { return filepath.Join(c.dataDir, "health.json") }

// healthLog loads the log on first use. It never takes c.mu, so it is safe to
// call with c.mu held.
func (c *Controller) healthLog() *healthLog {
	c.healthOnce.Do(func() {
		h := &healthLog{path: c.healthPath(), events: map[string][]healthEvent{}}
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
	return scoreHealth(h.events[node], time.Now())
}

func scoreHealth(ev []healthEvent, now time.Time) (int, string, string) {
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
	case score >= healthRed:
		colour = "red"
	case score >= healthAmber:
		colour = "amber"
	}
	note := ""
	if colour != "green" {
		note = fmt.Sprintf("Unreliable this week: %d disconnect(s), %d time(s) it could not take or finish work.", disc, prob)
	}
	return score, colour, note
}
