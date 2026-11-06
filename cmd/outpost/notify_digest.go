package main

// The morning summary: once a day at the chosen hour, one notification that
// lists what finished or failed since the last summary and what is waiting
// for you. Off until ticked in Settings → Notifications. Nothing is sent on a
// day with nothing to report.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// digestText builds the summary of tasks that ended after since. It returns
// "" when there is nothing to say.
func (c *Controller) digestText(since time.Time) string {
	c.mu.Lock()
	tasks := make([]*Task, 0, len(c.tasks))
	for _, t := range c.tasks {
		tasks = append(tasks, t)
	}
	var offline []string
	for id, n := range c.nodes {
		if _, ok := c.conns[id]; !ok {
			offline = append(offline, displayName(n))
		}
	}
	c.mu.Unlock()
	type row struct {
		title string
		at    time.Time
	}
	var done, failed, waiting []row
	for _, t := range tasks {
		t.mu.Lock()
		title := strings.TrimSpace(t.Title)
		if title == "" {
			title = t.ID
		}
		switch {
		case t.Ended != nil && t.Ended.After(since) && t.Status == "done":
			done = append(done, row{title, *t.Ended})
		case t.Ended != nil && t.Ended.After(since) && t.Status == "failed":
			failed = append(failed, row{title, *t.Ended})
		case t.Status == "awaiting_answers" || t.Status == "awaiting_approval" || t.Status == "paused_cap" || t.Status == "interrupted":
			waiting = append(waiting, row{title, t.Created})
		}
		t.mu.Unlock()
	}
	if len(done)+len(failed)+len(waiting) == 0 {
		return ""
	}
	list := func(rs []row) string {
		sort.Slice(rs, func(i, j int) bool { return rs[i].at.Before(rs[j].at) })
		var names []string
		for i, r := range rs {
			if i == 5 {
				names = append(names, fmt.Sprintf("and %d more", len(rs)-5))
				break
			}
			names = append(names, r.title)
		}
		return strings.Join(names, "; ")
	}
	var parts []string
	if len(done) > 0 {
		parts = append(parts, fmt.Sprintf("Finished (%d): %s.", len(done), list(done)))
	}
	if len(failed) > 0 {
		parts = append(parts, fmt.Sprintf("Failed (%d): %s.", len(failed), list(failed)))
	}
	if len(waiting) > 0 {
		parts = append(parts, fmt.Sprintf("Waiting for you (%d): %s.", len(waiting), list(waiting)))
	}
	if len(offline) > 0 {
		sort.Strings(offline)
		parts = append(parts, "Offline nodes: "+strings.Join(offline, ", ")+".")
	}
	return strings.Join(parts, " ")
}

// nextDigest is the next time the summary is due at hour h (local time).
func nextDigest(now time.Time, h int) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// digestLoop sends the summary each day. The first one covers the last 24 hours.
func (c *Controller) digestLoop() {
	last := time.Now().Add(-24 * time.Hour)
	for {
		c.mu.Lock()
		ns := c.settings.Notify
		c.mu.Unlock()
		due := nextDigest(time.Now(), ns.digestHour())
		// Sleep in short steps so a changed hour is picked up.
		for time.Now().Before(due) {
			time.Sleep(min(time.Until(due), 5*time.Minute))
			c.mu.Lock()
			h := c.settings.Notify.digestHour()
			c.mu.Unlock()
			if h != ns.digestHour() {
				break
			}
		}
		if time.Now().Before(due) {
			continue // hour changed
		}
		c.mu.Lock()
		on := c.settings.Notify.eventOn("digest")
		c.mu.Unlock()
		if on {
			if text := c.digestText(last); text != "" {
				c.deliverNotification("digest", "Outpost summary", text, "")
			}
		}
		last = time.Now()
	}
}
