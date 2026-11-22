package main

import (
	"strings"
	"testing"
	"time"
)

func everythingRig(t *testing.T) (*Controller, *int) {
	t.Helper()
	c := rolloutRig(t)
	c.tasks = map[string]*Task{}
	p, w := everythingPoll, everythingTaskWait
	everythingPoll, everythingTaskWait = 10*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { everythingPoll, everythingTaskWait = p, w })
	nw := nodeUpdateWait
	nodeUpdateWait = 100 * time.Millisecond // the fake nodes never finish their downloads
	t.Cleanup(func() { nodeUpdateWait = nw })
	restarts := new(int)
	c.restartHook = func(*Release) error { *restarts++; return nil }
	return c, restarts
}

func waitEverything(t *testing.T, c *Controller) everythingState {
	t.Helper()
	for i := 0; i < 500; i++ {
		c.mu.Lock()
		var e everythingState
		if c.everything != nil {
			e = *c.everything
		}
		c.mu.Unlock()
		if e.Ended != nil {
			return e
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("update everything did not end")
	return everythingState{}
}

func TestUpdateEverythingNodesThenController(t *testing.T) {
	c, restarts := everythingRig(t)
	n, ctl, canary, err := c.startEverything()
	if err != nil || n != 3 || !ctl || canary != "a" {
		t.Fatalf("n %d ctl %v canary %q err %v", n, ctl, canary, err)
	}
	if _, _, _, err := c.startEverything(); err == nil {
		t.Fatal("a second run started while one runs")
	}
	if *restarts != 0 {
		t.Fatal("the controller restarted before the canary passed")
	}
	c.mu.Lock()
	c.nodes["a"].Hello.AgentVersion = "9.9.9" // the canary is back on the new version
	c.mu.Unlock()
	e := waitEverything(t, c)
	if e.State != "done" || *restarts != 1 {
		t.Fatalf("state %+v restarts %d", e, *restarts)
	}
}

func TestUpdateEverythingStopsWhenCanaryFails(t *testing.T) {
	c, restarts := everythingRig(t)
	if _, _, _, err := c.startEverything(); err != nil {
		t.Fatal(err)
	}
	// the canary never comes back on the new version
	e := waitEverything(t, c)
	if e.State != "failed" || !strings.Contains(e.Text, "controller was not updated") || *restarts != 0 {
		t.Fatalf("state %+v restarts %d", e, *restarts)
	}
}

func TestUpdateEverythingControllerOnlyWaitsForTasks(t *testing.T) {
	c, restarts := everythingRig(t)
	for id := range c.nodes {
		c.nodes[id].Hello.AgentVersion = "9.9.9" // nodes are current
	}
	c.tasks["t1"] = &Task{ID: "t1", Status: "running"}
	n, ctl, _, err := c.startEverything()
	if err != nil || n != 0 || !ctl {
		t.Fatalf("n %d ctl %v err %v", n, ctl, err)
	}
	e := waitEverything(t, c)
	if e.State != "failed" || !strings.Contains(e.Text, "tasks are still running") || *restarts != 0 {
		t.Fatalf("busy: %+v restarts %d", e, *restarts)
	}
	c.mu.Lock()
	c.tasks["t1"].Status = "done"
	c.mu.Unlock()
	if _, _, _, err := c.startEverything(); err != nil {
		t.Fatal(err)
	}
	if e := waitEverything(t, c); e.State != "done" || *restarts != 1 {
		t.Fatalf("idle: %+v restarts %d", e, *restarts)
	}
}

func TestUpdateEverythingNothingToDo(t *testing.T) {
	c, _ := everythingRig(t)
	for id := range c.nodes {
		c.nodes[id].Hello.AgentVersion = "9.9.9"
	}
	c.releases[thisPlatform()].Version = Version
	if _, _, _, err := c.startEverything(); err == nil || !strings.Contains(err.Error(), "up to date") {
		t.Fatalf("err %v", err)
	}
}
