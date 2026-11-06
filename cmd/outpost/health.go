package main

// /healthz for uptime monitors (Uptime Kuma, Home Assistant, a cron curl).
// It needs no login, so it reports counts only: no names, versions or
// addresses that would tell someone on the network what runs here.

import "net/http"

func (c *Controller) handleHealth(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	online := len(c.conns)
	list := make([]*Task, 0, len(c.tasks))
	for _, t := range c.tasks {
		list = append(list, t)
	}
	c.mu.Unlock() // never hold c.mu while taking a task's lock
	running := 0
	for _, t := range list {
		t.mu.Lock()
		switch t.Status {
		case "planning", "running", "testing", "building", "fixing":
			running++
		}
		t.mu.Unlock()
	}
	status := 200
	if controllerClosing.Load() {
		status = 503
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]any{"ok": status == 200, "nodes_online": online, "tasks_active": running})
}
