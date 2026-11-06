package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Test-and-fix activity: a short in-memory trail of what the task is doing
// right now (running tests on a node, the fixer editing a file, waiting for a
// node that restarted). It is shown as the "Tests and fixes" card and is not
// written to disk; task events keep the long-term record.

const maxActSteps = 10

// ActStep is one line of the trail. Status is running, ok, bad or warn.
type ActStep struct {
	Text   string    `json:"text"`
	Node   string    `json:"node,omitempty"`
	Status string    `json:"status"`
	Quiet  bool      `json:"-"` // a "thinking" step: dropped when the next step starts
	Start  time.Time `json:"start"`
	End    time.Time `json:"end,omitempty"`
}

// actBegin starts a step and closes any step still running as ok.
func (t *Task) actBegin(text, node string) { t.actBeginQ(text, node, false) }

func (t *Task) actBeginQ(text, node string, quiet bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if n := len(t.act); n > 0 && t.act[n-1].Status == "running" {
		if t.act[n-1].Quiet {
			t.act = t.act[:n-1]
		} else {
			t.act[n-1].Status, t.act[n-1].End = "ok", now
		}
	}
	t.act = append(t.act, ActStep{Text: text, Node: node, Status: "running", Quiet: quiet, Start: now})
	if len(t.act) > maxActSteps {
		t.act = t.act[len(t.act)-maxActSteps:]
	}
	taskLive.changed(t.ID)
}

// actSet changes the text of the running step, keeping its start time.
func (t *Task) actSet(text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n := len(t.act); n > 0 && t.act[n-1].Status == "running" {
		t.act[n-1].Text = text
	}
	taskLive.changed(t.ID)
}

// actEnd closes the running step with a result.
func (t *Task) actEnd(status, text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n := len(t.act); n > 0 && t.act[n-1].Status == "running" {
		t.act[n-1].Status, t.act[n-1].End = status, time.Now()
		if text != "" {
			t.act[n-1].Text = text
		}
	}
	taskLive.changed(t.ID)
}

// actClear drops the trail (the task finished or a new request began).
func (t *Task) actClear() {
	t.mu.Lock()
	t.act = nil
	t.mu.Unlock()
}

var testCountRes = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^Ran (\d+) tests? in`),                // unittest
	regexp.MustCompile(`(?m)=+ .*?(\d+) (?:passed|failed).* in `), // pytest
}

// testSummary turns test output into a short result such as "3 failing" or
// "passed". It uses the counts the common runners print and otherwise falls
// back to the exit code.
func testSummary(output string, exit int) string {
	if exit == 0 {
		for _, re := range testCountRes[:1] {
			if m := re.FindStringSubmatch(output); m != nil {
				return m[1] + " tests passed"
			}
		}
		if m := regexp.MustCompile(`(\d+) passed`).FindStringSubmatch(output); m != nil {
			return m[1] + " tests passed"
		}
		return "passed"
	}
	if m := regexp.MustCompile(`FAILED \((?:failures=(\d+))?(?:, )?(?:errors=(\d+))?`).FindStringSubmatch(output); m != nil {
		n := 0
		fmt.Sscan(m[1], &n)
		e := 0
		fmt.Sscan(m[2], &e)
		ran := ""
		if r := testCountRes[0].FindStringSubmatch(output); r != nil {
			ran = " of " + r[1]
		}
		return fmt.Sprintf("%d failing%s", n+e, ran)
	}
	if m := regexp.MustCompile(`(\d+) failed`).FindStringSubmatch(output); m != nil {
		return m[1] + " failing"
	}
	if n := len(regexp.MustCompile(`(?m)^--- FAIL`).FindAllString(output, -1)); n > 0 {
		return fmt.Sprintf("%d failing", n)
	}
	return fmt.Sprintf("failed (exit code %d)", exit)
}

// actDetail is the activity as the browser sees it, with the stall check.
type actDetail struct {
	Steps  []ActStep `json:"steps"`
	Round  int       `json:"round"`
	Max    int       `json:"max_rounds"`
	Stall  string    `json:"stall,omitempty"`
	IdleS  int       `json:"idle_s,omitempty"`
	Server time.Time `json:"server_time"`
}

// stallNote says why the running step looks stuck, or "". A node that is
// offline or paused is reported at once; a model that has produced no text for
// stallAfter is reported as silent (a long prompt on a slow CPU can be normal,
// so the wording is a hint, not a verdict).
const stallAfter = 120 * time.Second

func (c *Controller) activityFor(t *Task) *actDetail {
	t.mu.Lock()
	steps := append([]ActStep(nil), t.act...)
	status, planner, rounds := t.Status, t.Planner, t.FixRounds
	t.mu.Unlock()
	if len(steps) == 0 || (status != "testing" && status != "fixing" && status != "building") {
		return nil
	}
	d := &actDetail{Steps: steps, Round: rounds + 1, Max: maxFixRounds, Server: time.Now()}
	cur := steps[len(steps)-1]
	if cur.Status != "running" {
		return d
	}
	nodeID := cur.Node
	if nodeID == "" && strings.HasPrefix(planner, "node:") {
		nodeID = strings.TrimPrefix(planner, "node:")
	}
	if nodeID != "" {
		c.mu.Lock()
		_, online := c.conns[nodeID]
		hb := c.lastHB[nodeID]
		c.mu.Unlock()
		name := c.nodeLabel(nodeID)
		switch {
		case !online:
			d.Stall = name + " is offline. Anything it was running is marked lost after 60 seconds and retried, so this should pick up when it reconnects."
		case hb != nil && hb.Paused:
			by := "from its tray menu"
			if hb.PausedBy == "web" {
				by = "from the web UI"
			}
			d.Stall = name + " is paused " + by + "; this step waits until it accepts work again."
		}
	}
	if d.Stall == "" {
		if idle := taskLive.idleFor(t.ID); idle > stallAfter {
			d.IdleS = int(idle.Seconds())
			d.Stall = fmt.Sprintf("No output for %s. A large prompt can take a while to start on a CPU node. If the model has stopped answering altogether, the node restarts it after a few minutes; if nothing moves, Stop and send a message to retry.", idle.Round(time.Second))
		}
	}
	return d
}
