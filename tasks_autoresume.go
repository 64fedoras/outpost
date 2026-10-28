package main

import (
	"strings"
	"time"
)

// autoResumeWait is how long after a restart the controller waits for a node
// with a loaded model before leaving interrupted tasks for the user.
const autoResumeWait = 10 * time.Minute

// autoResumeEligible says whether an interrupted task may resume without the
// user. Only tasks on a local planner qualify: Claude and OpenRouter cost
// money, so a restart must never spend it. Tasks that stopped at the spending
// cap also wait for the user.
func autoResumeEligible(t *Task) bool {
	return strings.HasPrefix(t.Planner, "node:") && t.PausedFrom != "paused_cap"
}

// autoResumeInterrupted resumes the tasks loadTasks marked once a node can run
// inference, checking every tick for up to wait.
func (c *Controller) autoResumeInterrupted(wait, tick time.Duration) {
	deadline := time.Now().Add(wait)
	for {
		c.mu.Lock()
		off := c.settings.AutoResumeOff
		var cands []*Task
		for _, t := range c.tasks {
			t.mu.Lock()
			if t.autoResume {
				cands = append(cands, t)
			}
			t.mu.Unlock()
		}
		c.mu.Unlock()
		if len(cands) == 0 {
			return
		}
		ready := len(c.workerNodes()) > 0
		timedOut := time.Now().After(deadline)
		if !off && !ready && !timedOut {
			time.Sleep(tick)
			continue
		}
		for _, t := range cands {
			t.mu.Lock()
			still := t.autoResume && t.Status == "interrupted"
			status, from := t.Status, t.PausedFrom
			t.autoResume = false
			t.mu.Unlock()
			if !still {
				continue
			}
			switch {
			case off:
				// the setting is off: leave it for the Resume button
			case ready:
				c.restartTask(t, status, from, "Resumed automatically after the restart.")
			default:
				t.logf("warn", "No node with a loaded model came back within %d minutes. Click Resume when one is ready.", int(wait/time.Minute))
			}
		}
		return
	}
}
