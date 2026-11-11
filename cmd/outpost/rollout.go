package main

// Canary updates: "Update all" (and GitHub auto-install) updates one node
// first, waits for it to come back on the new version, runs a quick check on
// it (a short model call if it had a model loaded, an echo command if commands
// are on), and only then updates the rest. If the first node fails, it is
// rolled back, the rest are left alone and a notification says why.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

type rolloutState struct {
	State      string     `json:"state"` // canary | checking | rolling | done | failed
	Canary     string     `json:"canary"`
	CanaryName string     `json:"canary_name"`
	Version    string     `json:"version"`
	Rest       int        `json:"rest"` // nodes waiting for the canary
	Text       string     `json:"text"`
	Started    time.Time  `json:"started"`
	Ended      *time.Time `json:"ended,omitempty"`
}

// Timings, shortened by tests.
var (
	rolloutBackWait  = 15 * time.Minute // for the canary to reconnect on the new version
	rolloutModelWait = 10 * time.Minute // for its model to be loaded again
	rolloutPoll      = 2 * time.Second
	rolloutCallWait  = 3 * time.Minute // for each check call
)

func (r *rolloutState) running() bool {
	return r != nil && r.Ended == nil
}

// pickCanary chooses the first node to update: an idle one, preferring one
// that can be checked (model loaded or commands on), then by name.
func (c *Controller) pickCanaryLocked(ids []string) string {
	type cand struct {
		id    string
		score int
		name  string
	}
	var cs []cand
	for _, id := range ids {
		n := c.nodes[id]
		if n == nil {
			continue
		}
		s := 0
		if c.runningLocked(id) == 0 {
			s += 4
		}
		if hb := c.lastHB[id]; hb != nil && hb.LoadedModel != "" {
			s += 2
		}
		if n.ExecEnabled {
			s++
		}
		cs = append(cs, cand{id, s, displayName(n)})
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].score != cs[j].score {
			return cs[i].score > cs[j].score
		}
		return cs[i].name < cs[j].name
	})
	if len(cs) == 0 {
		return ""
	}
	return cs[0].id
}

// startRollout updates ids one-node-first. It returns the canary's id.
func (c *Controller) startRollout(ids []string) (string, error) {
	if len(ids) == 0 {
		return "", errors.New("no node needs an update")
	}
	c.mu.Lock()
	if c.rollout.running() {
		c.mu.Unlock()
		return "", errors.New("an update is already rolling out; wait for it to finish")
	}
	canary := c.pickCanaryLocked(ids)
	n := c.nodes[canary]
	rel := c.updateCandidateLocked(n)
	if rel == nil {
		c.mu.Unlock()
		return "", errors.New("no newer release for the first node")
	}
	var rest []string
	for _, id := range ids {
		if id != canary {
			rest = append(rest, id)
		}
	}
	hb := c.lastHB[canary]
	hadModel := hb != nil && hb.LoadedModel != ""
	exec := n.ExecEnabled
	c.rollout = &rolloutState{State: "canary", Canary: canary, CanaryName: displayName(n), Version: rel.Version, Rest: len(rest), Started: time.Now(),
		Text: fmt.Sprintf("Updating %s first", displayName(n))}
	c.mu.Unlock()
	if err := c.startNodeUpdate(canary); err != nil {
		c.endRollout("failed", fmt.Sprintf("Could not start the update on %s: %v", displayName(n), err))
		return "", err
	}
	go c.runRollout(canary, rel.Version, rest, hadModel, exec)
	return canary, nil
}

func (c *Controller) setRollout(state, text string) {
	c.mu.Lock()
	if c.rollout != nil {
		c.rollout.State, c.rollout.Text = state, text
	}
	c.mu.Unlock()
}

func (c *Controller) endRollout(state, text string) {
	c.mu.Lock()
	if c.rollout != nil {
		now := time.Now()
		c.rollout.State, c.rollout.Text, c.rollout.Ended = state, text, &now
	}
	c.mu.Unlock()
	log.Printf("rollout %s: %s", state, text)
}

func (c *Controller) runRollout(canary, version string, rest []string, hadModel, exec bool) {
	name := c.nodeLabel(canary)
	if err := c.waitCanaryBack(canary, version); err != nil {
		msg := fmt.Sprintf("%s did not come back on v%s: %v. The other %d node(s) were not updated.", name, version, err, len(rest))
		c.endRollout("failed", msg)
		go c.deliverNotification("update_failed", "Update stopped", msg, "")
		return
	}
	c.setRollout("checking", fmt.Sprintf("%s is on v%s; checking it", name, version))
	if err := c.checkCanary(canary, hadModel, exec); err != nil {
		rolled := c.rollBackCanary(canary, version)
		msg := fmt.Sprintf("%s failed its check on v%s: %v. %s The other %d node(s) were not updated.", name, version, err, rolled, len(rest))
		c.endRollout("failed", msg)
		go c.deliverNotification("update_failed", "Update stopped", msg, "")
		return
	}
	c.setRollout("rolling", fmt.Sprintf("%s passed; updating the other %d node(s)", name, len(rest)))
	var skipped []string
	for _, id := range rest {
		if err := c.startNodeUpdate(id); err != nil {
			skipped = append(skipped, c.nodeLabel(id)+" ("+err.Error()+")")
		}
	}
	msg := fmt.Sprintf("%s passed its check on v%s; started %d other update(s).", name, version, len(rest)-len(skipped))
	if len(skipped) > 0 {
		msg += " Not started: " + strings.Join(skipped, "; ") + "."
	}
	c.endRollout("done", msg)
}

// waitCanaryBack waits for the node to reconnect on version.
func (c *Controller) waitCanaryBack(id, version string) error {
	deadline := time.Now().Add(rolloutBackWait)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		n := c.nodes[id]
		online := c.conns[id] != nil
		u := c.updates[id]
		ver, failed, failMsg := "", false, "" // read under the lock: a reconnect replaces Hello and the update status
		if n != nil && n.Hello != nil {
			ver = n.Hello.AgentVersion
		}
		if u != nil && u.State == "failed" {
			failed, failMsg = true, u.Error
		}
		c.mu.Unlock()
		if n == nil {
			return errors.New("the node was removed")
		}
		if online && ver == version {
			return nil
		}
		if failed {
			return errors.New(firstNonEmpty(failMsg, "the update failed"))
		}
		time.Sleep(rolloutPoll)
	}
	return fmt.Errorf("no answer after %s", rolloutBackWait)
}

// checkCanary runs the quick checks on the updated node.
func (c *Controller) checkCanary(id string, hadModel, exec bool) error {
	if hadModel {
		deadline := time.Now().Add(rolloutModelWait)
		for {
			c.mu.Lock()
			hb := c.lastHB[id]
			c.mu.Unlock()
			if hb != nil && hb.InferenceRdy && hb.LoadedModel != "" {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("its model did not load again")
			}
			time.Sleep(rolloutPoll)
		}
		ctx, cancel := context.WithTimeout(context.Background(), rolloutCallWait)
		spec := InferenceSpec{Messages: []ChatMessage{{Role: "user", Content: "Reply with the single word OK."}}, Params: InferenceParams{MaxTokens: 16}}
		_, err := c.runInferenceJob(ctx, id, spec, "Update check", rolloutCallWait)
		cancel()
		if err != nil {
			return fmt.Errorf("a model call failed: %w", err)
		}
	}
	if exec {
		ctx, cancel := context.WithTimeout(context.Background(), rolloutCallWait)
		out, status, err := c.runExecJob(ctx, id, c.execArgv(id, "echo outpost-check"), nil, "Update check", rolloutCallWait)
		cancel()
		if err != nil {
			return fmt.Errorf("running a command failed: %w", err)
		}
		if out == nil || out.ExitCode != 0 {
			return fmt.Errorf("running a command failed (%s)", status)
		}
	}
	return nil
}

// rollBackCanary sends the node back to its previous version and holds the
// failed version so auto-install does not try it again.
func (c *Controller) rollBackCanary(id, version string) string {
	c.mu.Lock()
	n := c.nodes[id]
	nc := c.conns[id]
	to := ""
	if n != nil && n.Hello != nil {
		to = n.Hello.RollbackTo
	}
	c.holdAfterRollbackLocked(version)
	if to != "" && nc != nil {
		c.updates[id] = &UpdateStatus{Version: to, State: "sent", Updated: time.Now()}
	}
	c.mu.Unlock()
	if to == "" || nc == nil {
		return "It could not be rolled back automatically; use Roll back on the Updates page."
	}
	if err := nc.send("node.rollback", NodeRollback{Version: to}); err != nil {
		return "Rolling it back failed (" + err.Error() + "); use Roll back on the Updates page."
	}
	return "It is being rolled back to v" + to + "."
}
