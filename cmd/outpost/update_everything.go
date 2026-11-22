package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"time"
)

// "Update everything": the nodes one-first (rollout.go), then the controller, in one click. The controller step
// comes last because it restarts this process; nothing is left to remember afterwards.
type everythingState struct {
	State   string     `json:"state"` // nodes | waiting_tasks | controller | done | failed
	Text    string     `json:"text"`
	Started time.Time  `json:"started"`
	Ended   *time.Time `json:"ended,omitempty"`
}

func (e *everythingState) running() bool { return e != nil && e.Ended == nil }

// Timings, shortened by tests.
var (
	everythingPoll     = 2 * time.Second
	everythingTaskWait = 60 * time.Minute // for running tasks to finish before the controller restarts
)

func (c *Controller) setEverything(state, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.everything != nil {
		c.everything.State, c.everything.Text = state, text
	}
}

func (c *Controller) endEverything(state, text string) {
	now := time.Now()
	c.mu.Lock()
	if c.everything != nil {
		c.everything.State, c.everything.Text, c.everything.Ended = state, text, &now
	}
	c.mu.Unlock()
	log.Printf("update everything %s: %s", state, text)
	if state == "failed" {
		go c.deliverNotification("update_failed", "Update stopped", text, "")
	}
}

// controllerReleaseLocked is the uploaded release for this controller when it is newer than the running one.
func (c *Controller) controllerReleaseLocked() *Release {
	rel := c.releases[thisPlatform()]
	if rel == nil || compareVersions(rel.Version, Version) <= 0 {
		return nil
	}
	return rel
}

// restartIntoRelease installs rel and restarts the controller into it. It only returns when that failed.
func (c *Controller) restartIntoRelease(rel *Release, tag string) error {
	if c.restartHook != nil {
		return c.restartHook(rel)
	}
	if _, err := installRelease(c.releasePath(rel.Platform), c.agentDir(), rel.Version, rel.SHA256); err != nil {
		return err
	}
	c.prepareRestart("update installed")
	relaunch(c.agentDir(), os.Args[1:])
	controllerClosing.Store(false) // still running: the restart did not happen
	log.Printf("%s: controller restart failed; still running %s", tag, Version)
	return errors.New("the controller did not restart; it is still running v" + Version)
}

// startEverything begins the chain. It reports how many nodes will update and whether the controller will.
func (c *Controller) startEverything() (nodes int, controller bool, canary string, err error) {
	c.mu.Lock()
	if c.everything.running() {
		c.mu.Unlock()
		return 0, false, "", errors.New("an update is already running")
	}
	var ids []string
	for id, n := range c.nodes {
		if c.conns[id] != nil && c.updateCandidateLocked(n) != nil {
			ids = append(ids, id)
		}
	}
	controller = c.controllerReleaseLocked() != nil
	c.mu.Unlock()
	sort.Strings(ids)
	if len(ids) == 0 && !controller {
		return 0, false, "", errors.New("everything is up to date")
	}
	if len(ids) > 0 {
		if canary, err = c.startRollout(ids); err != nil {
			return 0, false, "", err
		}
	}
	c.mu.Lock()
	c.everything = &everythingState{State: "nodes", Text: "Updating the nodes", Started: time.Now()}
	c.mu.Unlock()
	go c.runEverything(ids, controller)
	return len(ids), controller, canary, nil
}

func (c *Controller) runEverything(ids []string, controller bool) {
	if len(ids) > 0 {
		for {
			c.mu.Lock()
			r := c.rollout
			var ended bool
			var rs, rt string
			if r != nil {
				ended, rs, rt = r.Ended != nil, r.State, r.Text
			}
			c.mu.Unlock()
			if ended {
				if rs != "done" {
					c.endEverything("failed", rt+" The controller was not updated.")
					return
				}
				break
			}
			if r != nil {
				c.setEverything("nodes", rt)
			}
			time.Sleep(everythingPoll)
		}
		// The controller serves the node downloads: let them finish before it restarts.
		c.waitNodeUpdates(ids, nodeUpdateWait)
	}
	if !controller {
		c.endEverything("done", "All nodes are updated.")
		return
	}
	deadline := time.Now().Add(everythingTaskWait)
	for c.tasksBusy() {
		if time.Now().After(deadline) {
			c.endEverything("failed", "Nodes are updated, but tasks are still running, so the controller was not restarted. Press Update everything again when they finish.")
			return
		}
		c.setEverything("waiting_tasks", "Nodes are updated. Waiting for running tasks to finish before restarting the controller.")
		time.Sleep(everythingPoll)
	}
	c.mu.Lock()
	rel := c.controllerReleaseLocked()
	c.mu.Unlock()
	if rel == nil {
		c.endEverything("done", "All nodes are updated; the controller is already current.")
		return
	}
	c.setEverything("controller", fmt.Sprintf("Restarting the controller on v%s", rel.Version))
	if err := c.restartIntoRelease(rel, "update everything"); err != nil {
		c.endEverything("failed", "Nodes are updated, but the controller update failed: "+err.Error())
		return
	}
	c.endEverything("done", "Everything is updated.")
}

func (c *Controller) handleUpdateEverything(w http.ResponseWriter, r *http.Request) {
	nodes, controller, canary, err := c.startEverything()
	if err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"nodes": nodes, "controller": controller, "canary": c.nodeLabel(canary)})
}
