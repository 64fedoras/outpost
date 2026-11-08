package main

// Stuck task detector: a running task that has shown no sign of life for
// taskStallAfter (no event in its log, no model output or prompt progress) is
// marked once with a warning in its log and a "stalled" notification. It is not
// stopped: a slow node reading a long prompt can be silent for a while, so the
// owner decides (Stop, or Redo on the subtask). The mark clears by itself when
// the task shows life again.

import (
	"fmt"
	"time"
)

const (
	taskStallAfter = 15 * time.Minute
	taskStallTick  = time.Minute
)

// activeStatus says whether a task is working (not waiting on the owner).
func activeStatus(s string) bool {
	switch s {
	case "planning", "running", "testing", "building", "fixing":
		return true
	}
	return false
}

// lastSign is the newest sign of life: the last log event, or output from a
// live model call.
func lastSign(t *Task, now time.Time) time.Time {
	var last time.Time
	for i := len(t.Events) - 1; i >= 0; i-- {
		if !t.stallNote.IsZero() && t.Events[i].Time.Equal(t.stallNote) {
			continue // our own warning is not a sign of life
		}
		last = t.Events[i].Time
		break
	}
	if last.IsZero() {
		last = t.Created
	}
	if idle := taskLive.idleFor(t.ID); idle > 0 {
		if out := now.Add(-idle); out.After(last) {
			last = out
		}
	} else if taskLive.hasCalls(t.ID) {
		last = now // a call just started
	}
	return last
}

// checkStalled marks and reports stalled tasks; returns how many it marked.
func (c *Controller) checkStalled(now time.Time) int {
	c.mu.Lock()
	list := make([]*Task, 0, len(c.tasks))
	for _, t := range c.tasks {
		list = append(list, t)
	}
	c.mu.Unlock()
	marked := 0
	for _, t := range list {
		t.mu.Lock()
		if !activeStatus(t.Status) {
			t.Stalled = false
			t.mu.Unlock()
			continue
		}
		quiet := now.Sub(lastSign(t, now))
		was := t.Stalled
		t.Stalled = quiet >= taskStallAfter
		mark := t.Stalled && !was
		t.mu.Unlock()
		if mark {
			marked++
			note := time.Now()
			t.mu.Lock()
			t.stallNote = note
			t.Events = append(t.Events, TaskEvent{Time: note, Level: "warn", Text: fmt.Sprintf("No progress for %d minutes. A node may be hung or very slow; Stop the task, or Redo the subtask on another node.", int(quiet/time.Minute))})
			t.saveSoonLocked()
			t.mu.Unlock()
			c.notifyTask(t, "stalled", "No progress for 15 minutes.")
		}
	}
	return marked
}

func (c *Controller) stallLoop() {
	for {
		time.Sleep(taskStallTick)
		c.checkStalled(time.Now())
	}
}
