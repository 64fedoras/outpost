package main

// Test runs and fix rounds.
// Split out of tasks.go; see that file for the task model and overview.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const fixSystem = `You are the lead engineer integrating code written by several small models. The project's tests fail. Find the cause and fix it. Change as few files as needed; you may also add missing files.

The tests describe the intended behaviour: fix the code to match them, and change a test only when the test itself is clearly wrong. If earlier fix rounds are listed and the tests still fail, their approach did not work; look for a different cause instead of repeating it.

` + fileFormatHelp + `
Output only the files you change or add, each one complete.`

func (c *Controller) testAndFix(ctx context.Context, t *Task) {
	t.mu.Lock()
	plan := t.Plan
	t.mu.Unlock()
	cmd := strings.TrimSpace(plan.TestCommand)
	if setup := strings.TrimSpace(plan.SetupCommand); setup != "" && cmd != "" {
		cmd = setup + " && " + cmd
	}
	if cmd == "" {
		t.mu.Lock()
		t.TestStatus = "skipped"
		t.mu.Unlock()
		t.logf("info", "The plan has no automated tests.")
		if !c.runBuild(ctx, t) {
			return
		}
		c.finishTask(t)
		return
	}
	var history []string
	nodeRetries, plannerRetries := 0, 0
	t.actClear()
	for round := 0; ; round++ {
		if ctx.Err() != nil {
			return
		}
		execs := c.execNodes()
		if len(execs) == 0 {
			t.mu.Lock()
			t.TestStatus = "skipped"
			t.mu.Unlock()
			t.logf("warn", "No node has exec enabled, so the tests were not run. Enable exec on a node to test automatically.")
			c.finishTask(t)
			return
		}
		if need := toolsInCommand(cmd); len(need) > 0 {
			capable := c.execNodesWith(execs, need)
			if len(capable) == 0 {
				t.mu.Lock()
				t.TestStatus = "skipped"
				t.mu.Unlock()
				t.logf("warn", "No exec node has %s, so the tests were not run. Install it on a node (Go can be installed from the Nodes tab).", c.missingToolsNote(execs, need))
				c.finishTask(t)
				return
			}
			execs = capable
		}
		node := execs[round%len(execs)]
		t.setStatus("testing")
		t.actBegin(fmt.Sprintf("Running the tests: %s", firstLine(cmd, 100)), node)
		t.logf("info", "Running tests on %s: %s", c.nodeLabel(node), cmd)
		inputs, srcs, skipped := repoJobInputs(t.repoDir())
		if len(skipped) > 0 {
			t.logf("warn", "Not sent to the test node (over the 2 GiB limit): %s", strings.Join(skipped, ", "))
		}
		out, res, err := c.runExecJobFiles(ctx, node, c.execArgv(node, plan.SetupCommand, plan.TestCommand), inputs, srcs, nil, t.Title+" · tests", t.testLimit())
		status := ""
		if res != nil {
			status = res.Status
		}
		if errors.Is(err, errCancelled) || ctx.Err() != nil {
			return
		}
		if err != nil && isNodeProblem(err) && nodeRetries < 2 {
			// The node stopped (paused from its tray, went offline): run the same
			// round again, on another node if one is free, without using up a fix round.
			nodeRetries++
			t.actEnd("warn", fmt.Sprintf("%s stopped during the tests; running them again", c.nodeLabel(node)))
			t.logf("warn", "%s stopped running the tests (%v); trying again.", c.nodeLabel(node), err)
			c.coolDown(node, 15*time.Second)
			round--
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		var output string
		passed := false
		switch {
		case err != nil && status == "timed_out":
			output = "The test command timed out after 10 minutes."
			t.actEnd("bad", "The tests timed out after 10 minutes")
		case err != nil:
			t.actEnd("bad", "Could not run the tests: "+firstLine(err.Error(), 120))
			t.logf("warn", "Could not run the tests: %v", err)
			t.mu.Lock()
			t.TestStatus = "skipped"
			t.mu.Unlock()
			c.finishTask(t)
			return
		default:
			output = strings.TrimSpace(out.StdoutTail + "\n" + out.StderrTail)
			passed = out.ExitCode == 0
			st := "ok"
			if !passed {
				st = "bad"
			}
			t.actEnd(st, fmt.Sprintf("Ran the tests on %s: %s", c.nodeLabel(node), testSummary(output, out.ExitCode)))
		}
		if len(output) > 12000 {
			output = "…" + output[len(output)-12000:]
		}
		if !passed {
			output = withSandboxNote(output, out)
		}
		t.mu.Lock()
		t.TestOutput = output
		t.mu.Unlock()
		if passed {
			t.mu.Lock()
			t.TestStatus = "passed"
			t.mu.Unlock()
			t.logf("info", "Tests passed.")
			if !c.runBuild(ctx, t) {
				return
			}
			c.finishTask(t)
			return
		}
		if round >= t.fixLimit() {
			t.mu.Lock()
			t.TestStatus = "failing"
			t.mu.Unlock()
			t.logf("warn", "Tests still fail after %d fix rounds. The result is available, with the last test output.", t.fixLimit())
			c.finishTask(t)
			return
		}
		t.logf("warn", "Tests failed; fix round %d of %d.", round+1, t.fixLimit())
		t.setStatus("fixing")
		t.actBegin(fmt.Sprintf("Fix round %d of %d: the planner is reading the failures", round+1, t.fixLimit()), "")
		var changed []string
		agentDone := false
		if c.agentEnabled() {
			ch, err := c.agentFix(ctx, t, plan, cmd, output, history, round)
			switch {
			case errors.Is(err, errCancelled) || ctx.Err() != nil:
				return
			case err == nil:
				changed, agentDone = ch, true
			case errors.Is(err, errAgentUnsupported):
				t.logf("warn", "The planner cannot use tools here; fixing with a single request instead.")
			case errors.Is(err, errAgentToolParse):
				t.logf("warn", "The model's tool calls kept getting cut off; fixing with a single request instead.")
			case isNodeProblem(err) && c.waitPlannerBack(ctx, t, err, &plannerRetries):
				round--
				continue
			default:
				c.failTask(t, err)
				return
			}
		}
		if !agentDone {
			budget := c.plannerBudget(t)
			// A local planner has a small window: keep the test output to a third
			// of it so the code under test still fits.
			shown := output
			if limit := budget / 3; len(shown) > limit && limit > 0 {
				shown = "…" + shown[len(shown)-limit:]
			}
			var earlier strings.Builder
			if len(history) > 0 {
				earlier.WriteString("\n\nEarlier fix rounds (the tests still failed after each):\n")
				for _, h := range history {
					earlier.WriteString("- " + h + "\n")
				}
			}
			user := t.instructionsBlock() + fmt.Sprintf("Project: %s\n%s\n\nTest command: %s\n\nTest output:\n%s%s\n\nProject files:\n%s",
				plan.Title, plan.Summary, cmd, shown, earlier.String(),
				repoSnapshotFirst(t.repoDir(), budget-len(shown)-earlier.Len()-4000, output)) + t.steeringText()
			resp, err := c.plannerCall(ctx, t, llmRequest{System: fixSystem, User: user, MaxTokens: 16000, Temperature: 0.2, Label: fmt.Sprintf("Fix round %d", round+1)})
			if err != nil {
				if isNodeProblem(err) && c.waitPlannerBack(ctx, t, err, &plannerRetries) {
					round--
					continue
				}
				c.failTask(t, err)
				return
			}
			for _, b := range parseFileBlocks(resp.Text) {
				p := strings.TrimPrefix(b.Path, "./")
				if err := t.writeFile(p, b.Content); err != nil {
					t.logf("warn", "Ignored a fix for %s: %v", p, err)
					continue
				}
				changed = append(changed, p)
			}
			t.touch(changed...)
		}
		if len(changed) == 0 {
			t.mu.Lock()
			t.TestStatus = "failing"
			t.mu.Unlock()
			t.actEnd("bad", fmt.Sprintf("Fix round %d: the planner proposed no change", round+1))
			t.logf("warn", "The planner did not propose a fix. The result is available with the failing tests.")
			c.finishTask(t)
			return
		}
		t.mu.Lock()
		t.FixRounds++
		t.mu.Unlock()
		history = append(history, fmt.Sprintf("round %d changed %s", round+1, strings.Join(changed, ", ")))
		t.actEnd("ok", fmt.Sprintf("Fix round %d changed %s", round+1, strings.Join(changed, ", ")))
		t.logf("info", "Fix round %d changed: %s", round+1, strings.Join(changed, ", "))
	}
}

// waitPlannerBack handles a local planner node that stopped mid-fix (restarted,
// updated, paused). It waits for the node to be ready again and says so on the
// activity card, so the caller can run the same round again without using one
// up. It returns false when the planner is a cloud one, retries ran out, the
// node did not return within 10 minutes, or the task was stopped.
func (c *Controller) waitPlannerBack(ctx context.Context, t *Task, cause error, retries *int) bool {
	t.mu.Lock()
	planner := t.Planner
	t.mu.Unlock()
	if !strings.HasPrefix(planner, "node:") || *retries >= 3 {
		return false
	}
	*retries++
	node := strings.TrimPrefix(planner, "node:")
	name := c.nodeLabel(node)
	t.actEnd("warn", fmt.Sprintf("%s stopped while fixing (%s)", name, firstLine(cause.Error(), 80)))
	t.actBegin(fmt.Sprintf("Waiting for %s to come back with its model loaded, then repeating this fix round", name), node)
	t.logf("warn", "%s stopped while fixing (%v). Waiting for it to come back, then trying the round again.", name, cause)
	c.coolDown(node, 5*time.Second)
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
		for _, id := range c.workerNodes() {
			if id == node {
				t.actEnd("ok", name+" is back")
				t.logf("info", "%s is back; repeating the fix round.", name)
				return true
			}
		}
	}
	return false
}

func (c *Controller) finishTask(t *Task) {
	t.actClear()
	zp := filepath.Join(t.dir, "result.zip")
	os.Remove(zp)
	if err := zipDir(t.repoDir(), zp); err != nil {
		c.failTask(t, fmt.Errorf("packaging the result: %w", err))
		return
	}
	st, _ := os.Stat(zp)
	t.mu.Lock()
	if st != nil {
		t.ResultBytes = st.Size()
	}
	t.mu.Unlock()
	t.setStatus("done")
	c.recordScorecard(t)
	stats := c.roundStats(t)
	t.mu.Lock()
	cost := t.CostUSD
	t.addMsgLocked(TaskMessage{Role: "assistant", Kind: "result", Text: resultText(t), Files: sortedCopy(t.Touched), Stats: stats})
	t.mu.Unlock()
	c.notifyTask(t, "done", "Finished. Download the result from the task page.")
	t.mu.Lock()
	learn := t.ProjectID != "" && t.TestStatus == "failing"
	t.mu.Unlock()
	if learn {
		go c.suggestFromFailure(t)
	}
	t.logf("info", "Finished. Cloud model cost: %s. Download the result from this page.", fmtUSD(cost))
}
