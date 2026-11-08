package main

// The weekly report: Mondays at the morning-summary hour, one notification
// with the week's tasks (finished, failed), what the cloud planners cost, which
// nodes wrote the most subtasks, tasks waiting for you, offline nodes and disk
// use. Off until ticked in Settings → Notifications. Cost counts tasks that
// ended in the week (their full cost, including earlier days); deleted tasks
// are not counted.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const weeklyEvent = "weekly"

// weeklyText builds the report for the week before now.
func (c *Controller) weeklyText(now time.Time) string {
	since := now.AddDate(0, 0, -7)
	c.mu.Lock()
	tasks := make([]*Task, 0, len(c.tasks))
	for _, t := range c.tasks {
		tasks = append(tasks, t)
	}
	var offline []string
	names := map[string]string{}
	for id, n := range c.nodes {
		names[id] = displayName(n)
		if _, ok := c.conns[id]; !ok {
			offline = append(offline, displayName(n))
		}
	}
	c.mu.Unlock()
	done, failed, waiting, subtasks := 0, 0, 0, 0
	cost := 0.0
	perNode := map[string]int{}
	for _, t := range tasks {
		t.mu.Lock()
		switch {
		case t.Ended != nil && t.Ended.After(since) && t.Status == "done":
			done++
		case t.Ended != nil && t.Ended.After(since) && t.Status == "failed":
			failed++
		case t.Status == "awaiting_answers" || t.Status == "awaiting_approval" || t.Status == "paused_cap" || t.Status == "interrupted":
			waiting++
		}
		if t.Ended != nil && t.Ended.After(since) {
			cost += t.CostUSD
			for _, s := range t.Subtasks {
				if s.Status == "done" && s.Node != "" && !s.ByPlanner {
					perNode[s.Node]++
					subtasks++
				}
			}
		}
		t.mu.Unlock()
	}
	parts := []string{fmt.Sprintf("This week: %d task(s) finished, %d failed, %d waiting for you.", done, failed, waiting)}
	if cost > 0 {
		parts = append(parts, fmt.Sprintf("Cloud planner cost: $%.2f.", cost))
	}
	if subtasks > 0 {
		type kv struct {
			name string
			n    int
		}
		var top []kv
		for id, n := range perNode {
			nm := names[id]
			if nm == "" {
				nm = id
			}
			top = append(top, kv{nm, n})
		}
		sort.Slice(top, func(i, j int) bool {
			if top[i].n != top[j].n {
				return top[i].n > top[j].n
			}
			return top[i].name < top[j].name
		})
		var s []string
		for i, k := range top {
			if i == 3 {
				break
			}
			s = append(s, fmt.Sprintf("%s (%d)", k.name, k.n))
		}
		parts = append(parts, fmt.Sprintf("Busiest nodes by subtasks written: %s.", strings.Join(s, ", ")))
	}
	if len(offline) > 0 {
		sort.Strings(offline)
		parts = append(parts, "Offline nodes: "+strings.Join(offline, ", ")+".")
	}
	if free, total := diskFree(c.dataDir), diskTotal(c.dataDir); total > 0 {
		parts = append(parts, fmt.Sprintf("Controller disk: %s free of %s.", humanBytes(int64(free)), humanBytes(int64(total))))
	}
	return strings.Join(parts, " ")
}

// nextWeekly is the next Monday at hour h after now.
func nextWeekly(now time.Time, h int) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
	for t.Weekday() != time.Monday || !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

func (c *Controller) weeklyLoop() {
	for {
		c.mu.Lock()
		h := c.settings.Notify.digestHour()
		c.mu.Unlock()
		due := nextWeekly(time.Now(), h)
		for time.Now().Before(due) {
			time.Sleep(min(time.Until(due), 5*time.Minute))
			c.mu.Lock()
			nh := c.settings.Notify.digestHour()
			c.mu.Unlock()
			if nh != h {
				break
			}
		}
		if time.Now().Before(due) {
			continue // the hour changed
		}
		c.mu.Lock()
		on := c.settings.Notify.eventOn(weeklyEvent)
		c.mu.Unlock()
		if on {
			c.deliverNotification(weeklyEvent, "Outpost weekly report", c.weeklyText(time.Now()), "")
		}
		time.Sleep(time.Minute) // never twice for the same Monday
	}
}
