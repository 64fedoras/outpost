package main

// Working hours per node (idea 28): a node can be told to take work only on
// some days and between two times, for example a gaming desktop overnight.
// Outside its hours the node gets no new model calls or test runs (a call
// already running finishes), and its idle unload wait drops to the minimum
// so its model is stopped soon after and the memory is free. The model loads
// again on the first call once the hours start. Times are the controller's
// local time.

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type WorkHours struct {
	Days []int  `json:"days,omitempty"` // 0 = Sunday … 6 = Saturday; empty = every day
	From string `json:"from"`           // "22:00"
	To   string `json:"to"`             // "07:00"; earlier than From means past midnight
}

func parseHM(s string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("%q is not a time like 22:00", s)
	}
	return h*60 + m, nil
}

// check validates and tidies the hours.
func (w *WorkHours) check() error {
	from, err := parseHM(w.From)
	if err != nil {
		return err
	}
	to, err := parseHM(w.To)
	if err != nil {
		return err
	}
	if from == to {
		return errors.New("the start and end time are the same; clear the hours to work all day")
	}
	w.From, w.To = fmt.Sprintf("%02d:%02d", from/60, from%60), fmt.Sprintf("%02d:%02d", to/60, to%60)
	seen := map[int]bool{}
	var days []int
	for _, d := range w.Days {
		if d < 0 || d > 6 {
			return errors.New("days are 0 (Sunday) to 6 (Saturday)")
		}
		if !seen[d] {
			seen[d] = true
			days = append(days, d)
		}
	}
	if len(days) == 7 {
		days = nil
	}
	w.Days = days
	return nil
}

func (w *WorkHours) dayOn(d time.Weekday) bool {
	if len(w.Days) == 0 {
		return true
	}
	for _, x := range w.Days {
		if x == int(d) {
			return true
		}
	}
	return false
}

// open reports whether the node takes work at now. Nil hours are always open.
// A window past midnight (22:00 to 07:00) belongs to the day it starts on.
func (w *WorkHours) open(now time.Time) bool {
	if w == nil {
		return true
	}
	from, err1 := parseHM(w.From)
	to, err2 := parseHM(w.To)
	if err1 != nil || err2 != nil || from == to {
		return true
	}
	m := now.Hour()*60 + now.Minute()
	if from < to {
		return m >= from && m < to && w.dayOn(now.Weekday())
	}
	if m >= from {
		return w.dayOn(now.Weekday())
	}
	if m < to {
		return w.dayOn(now.AddDate(0, 0, -1).Weekday())
	}
	return false
}

// hoursLoop pushes new settings to a node when its hours open or close, so its
// idle unload wait follows (see nodeSettingsLocked).
func (c *Controller) hoursLoop() {
	last := map[string]bool{}
	for {
		now := time.Now()
		c.mu.Lock()
		type send struct {
			nc *nodeConn
			s  NodeSettings
		}
		var out []send
		for id, n := range c.nodes {
			open := n.Hours.open(now)
			was, seen := last[id]
			last[id] = open
			if seen && was != open {
				if nc := c.conns[id]; nc != nil {
					out = append(out, send{nc, c.nodeSettingsLocked(n)})
				}
			}
		}
		c.mu.Unlock()
		for _, x := range out {
			_ = x.nc.send("node.settings", x.s)
		}
		time.Sleep(time.Until(now.Truncate(time.Minute).Add(time.Minute + time.Second)))
	}
}
