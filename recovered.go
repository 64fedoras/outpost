package main

// Notes about state files that were damaged when the controller started:
// loaded from the previous copy (.bak) or set aside. Shown once at the top of
// the web UI until dismissed, and kept in memory only (they describe this run).

import (
	"log"
	"net/http"
	"sync"
)

type recoveredNotes struct {
	mu    sync.Mutex
	notes []string
}

func (c *Controller) noteRecovered(msg string) {
	log.Printf("recovery: %s", msg)
	c.recovered.mu.Lock()
	defer c.recovered.mu.Unlock()
	if len(c.recovered.notes) < 50 {
		c.recovered.notes = append(c.recovered.notes, msg)
	}
}

func (c *Controller) recoveredList() []string {
	c.recovered.mu.Lock()
	defer c.recovered.mu.Unlock()
	return append([]string{}, c.recovered.notes...)
}

func (c *Controller) handleDismissRecovered(w http.ResponseWriter, r *http.Request) {
	c.recovered.mu.Lock()
	c.recovered.notes = nil
	c.recovered.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}
