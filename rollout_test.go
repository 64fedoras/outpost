package main

import (
	"strings"
	"testing"
	"time"
)

func rolloutRig(t *testing.T) *Controller {
	t.Helper()
	p, b, w := rolloutPoll, rolloutBackWait, rolloutCallWait
	rolloutPoll, rolloutBackWait, rolloutCallWait = 10*time.Millisecond, 2*time.Second, 300*time.Millisecond
	t.Cleanup(func() { rolloutPoll, rolloutBackWait, rolloutCallWait = p, b, w })
	c := &Controller{dataDir: t.TempDir(), conns: map[string]*nodeConn{}, lastHB: map[string]*Heartbeat{}, jobs: map[string]*JobRec{},
		updates: map[string]*UpdateStatus{}, waiters: map[string]chan *JobResult{}, releases: map[string]*Release{"linux-amd64": {Platform: "linux-amd64", Version: "9.9.9"}},
		nodes: map[string]*NodeRec{}}
	for _, id := range []string{"a", "b", "c"} {
		c.nodes[id] = &NodeRec{ID: id, Name: "node-" + id, Hello: &Hello{OS: "linux", Arch: "amd64", AgentVersion: "0.8.0", RollbackTo: "0.7.3"}}
		c.conns[id] = fakeNodeServer(t, c, nil, false)
	}
	c.lastHB["b"] = &Heartbeat{LoadedModel: ""}
	return c
}

func waitRollout(t *testing.T, c *Controller) *rolloutState {
	for i := 0; i < 300; i++ {
		c.mu.Lock()
		r := *c.rollout
		c.mu.Unlock()
		if r.Ended != nil {
			return &r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("rollout did not end")
	return nil
}

func TestRolloutWaitsForCanaryThenUpdatesRest(t *testing.T) {
	c := rolloutRig(t)
	c.nodes["c"].ExecEnabled = false
	canary, err := c.startRollout([]string{"a", "b", "c"})
	if err != nil || canary != "a" {
		t.Fatalf("canary %q err %v", canary, err)
	}
	if _, err := c.startRollout([]string{"b"}); err == nil {
		t.Fatal("a second rollout started while one runs")
	}
	c.mu.Lock()
	if c.updates["b"] != nil || c.updates["c"] != nil {
		c.mu.Unlock()
		t.Fatal("other nodes updated before the canary came back")
	}
	c.nodes["a"].Hello.AgentVersion = "9.9.9" // back on the new version; no model, commands off: nothing to check
	c.mu.Unlock()
	r := waitRollout(t, c)
	if r.State != "done" {
		t.Fatalf("rollout %+v", r)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.updates["b"] == nil || c.updates["c"] == nil || c.updates["b"].Version != "9.9.9" {
		t.Fatalf("rest not updated: %+v", c.updates)
	}
}

func TestRolloutStopsWhenCanaryFails(t *testing.T) {
	c := rolloutRig(t)
	if _, err := c.startRollout([]string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.updates["a"] = &UpdateStatus{Version: "9.9.9", State: "failed", Error: "Defender blocked it"}
	c.mu.Unlock()
	r := waitRollout(t, c)
	if r.State != "failed" || !strings.Contains(r.Text, "Defender") {
		t.Fatalf("rollout %+v", r)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.updates["b"] != nil {
		t.Fatal("the rest were updated after the canary failed")
	}
}

func TestRolloutRollsBackFailedCheck(t *testing.T) {
	c := rolloutRig(t)
	c.nodes["a"].ExecEnabled = true
	if _, err := c.startRollout([]string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.nodes["a"].Hello.AgentVersion = "9.9.9" // back, but the fake node never answers the command check
	c.mu.Unlock()
	r := waitRollout(t, c)
	if r.State != "failed" || !strings.Contains(r.Text, "rolled back to v0.7.3") {
		t.Fatalf("rollout %+v", r)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settings.GitHubHoldVersion != "9.9.9" || c.updates["b"] != nil {
		t.Fatalf("hold %q, b %+v", c.settings.GitHubHoldVersion, c.updates["b"])
	}
}
