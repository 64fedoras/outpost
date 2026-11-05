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

// nudgeResume wakes autoResumeInterrupted at once; called on every node
// heartbeat, so a task resumes the moment its node reports a loaded model.
func (c *Controller) nudgeResume() {
	select {
	case c.resumeWake <- struct{}{}:
	default:
	}
}

// autoResumeInterrupted resumes the tasks loadTasks marked, each as soon as
// its planner node can run inference. It wakes on every node heartbeat
// (nudgeResume) and also checks every tick, for up to wait.
func (c *Controller) autoResumeInterrupted(wait, tick time.Duration) {
	deadline := time.Now().Add(wait)
	for {
		c.mu.Lock()
		off := c.settings.AutoResumeOff
		var cands []*Task
		for _, t := range c.tasks {
			t.mu.Lock()
			if t.AutoResume {
				cands = append(cands, t)
			}
			t.mu.Unlock()
		}
		c.mu.Unlock()
		if len(cands) == 0 {
			return
		}
		ready := map[string]bool{}
		for _, id := range c.workerNodes() {
			ready[id] = true
		}
		timedOut := time.Now().After(deadline)
		waiting := false
		for _, t := range cands {
			t.mu.Lock()
			still := t.AutoResume && t.Status == "interrupted"
			status, from := t.Status, t.PausedFrom
			node := strings.TrimPrefix(t.Planner, "node:")
			act := !still || off || ready[node] || timedOut
			if act {
				t.AutoResume = false
				if !still || !ready[node] || off {
					t.saveLocked() // restartTask saves the resumed ones
				}
			}
			t.mu.Unlock()
			if !act {
				waiting = true
			}
			if !act || !still {
				continue
			}
			switch {
			case off:
				// the setting is off: leave it for the Resume button
			case ready[node]:
				c.restartTask(t, status, from, "Resumed automatically after the restart.")
			default:
				t.logf("warn", "Node %s did not come back with a loaded model within %d minutes. Click Resume when it is ready.", c.nodeLabel(node), int(wait/time.Minute))
			}
		}
		if !waiting {
			return
		}
		select {
		case <-c.resumeWake:
		case <-time.After(tick):
		}
	}
}
