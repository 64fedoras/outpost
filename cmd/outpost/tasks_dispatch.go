package main

// The dispatch loop executeTask: running subtasks on nodes, repo writes.
// Split out of tasks.go; see that file for the task model and overview.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

func (c *Controller) executeTask(ctx context.Context, t *Task) {
	t.mu.Lock()
	plan := t.Plan
	t.mu.Unlock()
	if plan == nil {
		c.failTask(t, errors.New("no plan"))
		return
	}
	if err := c.applyFileOps(t, plan); err != nil {
		c.failTask(t, err)
		return
	}
	specs := map[string]PlanSubtask{}
	for _, s := range plan.Subtasks {
		specs[s.ID] = s
	}
	t.mu.Lock()
	for _, st := range t.Subtasks {
		if sp, ok := specs[st.ID]; ok && st.RedoNote != "" && st.Status == "pending" {
			sp.Instructions += "\n\nNote from the user for this redo: " + st.RedoNote
			specs[st.ID] = sp
		}
	}
	t.mu.Unlock()
	type doneMsg struct {
		id  string
		err error
	}
	done := make(chan doneMsg, len(plan.Subtasks))
	running := 0
	waitingLogged := false
	readySince := map[string]time.Time{}
	bonMode := c.bestOfMode()
	groups := map[string]*bonGroup{} // best-of-N groups of running subtasks
	var groupsMu sync.Mutex
	for {
		if ctx.Err() != nil {
			return
		}
		// Pick subtasks whose dependencies are done.
		t.mu.Lock()
		state := map[string]*SubtaskState{}
		pending, failed := 0, 0
		for _, s := range t.Subtasks {
			state[s.ID] = s
			switch s.Status {
			case "pending":
				pending++
			case "failed":
				failed++
			}
		}
		var ready []string
		for _, s := range t.Subtasks {
			if s.Status != "pending" {
				continue
			}
			ok := true
			for _, d := range specs[s.ID].DependsOn {
				if state[d] == nil || state[d].Status != "done" {
					ok = false
				}
			}
			if ok {
				ready = append(ready, s.ID)
			}
		}
		t.mu.Unlock()
		if pending == 0 && running == 0 {
			if failed > 0 {
				c.failTask(t, fmt.Errorf("%d subtask(s) could not be completed", failed))
				return
			}
			break
		}
		if len(ready) == 0 && running == 0 {
			c.failTask(t, errors.New("remaining subtasks depend on subtasks that failed"))
			return
		}
		workers := c.taskWorkers(t)
		if len(workers) == 0 && len(ready) > 0 && running == 0 && !waitingLogged {
			if t.NodeLabel != "" {
				t.logf("warn", "Waiting for a node labelled %q with a loaded model.", t.NodeLabel)
			} else {
				t.logf("warn", "Waiting for a node with a loaded model. Load one on the Nodes tab.")
			}
			waitingLogged = true
			c.wakeForWork(t.NodeLabel)
		}
		// Hard subtasks choose first, so they get the strongest free node.
		rank := map[string]int{"hard": 0, "normal": 1, "easy": 2}
		sort.SliceStable(ready, func(i, j int) bool {
			return rank[normDifficulty(specs[ready[i]].Difficulty)] < rank[normDifficulty(specs[ready[j]].Difficulty)]
		})
		scores := c.scoreNodes(workers)
		placed := 0
		var newGroups []*bonGroup
		var newSpecs []PlanSubtask
		for _, id := range ready {
			if _, ok := readySince[id]; !ok {
				readySince[id] = time.Now()
			}
			t.mu.Lock()
			prefer := state[id].Prefer
			t.mu.Unlock()
			if prefer == "planner" {
				placed++
				delete(readySince, id)
				running++
				c.redoByPlanner(ctx, t, specs[id], state[id], func(err error) { done <- doneMsg{id, err} })
				continue
			}
			free := c.freeScores(scores, "")
			if len(free) == 0 {
				break
			}
			diff := normDifficulty(specs[id].Difficulty)
			if prefer == "strongest" {
				diff = "hard"
			}
			node := chooseNode(diff, free, scores, time.Since(readySince[id]))
			if prefer != "" && prefer != "strongest" {
				node = c.preferredNode(t, state[id], free, time.Since(readySince[id]))
			}
			if node == "" || !c.tryReserve(node) {
				continue // a hard subtask waiting for the strongest model, or the node was just taken
			}
			placed++
			delete(readySince, id)
			waitingLogged = false
			t.mu.Lock()
			st := state[id]
			now := time.Now()
			st.Status, st.Node, st.Started, st.Error = "running", node, &now, ""
			st.NodeName = c.nodeLabel(node)
			t.saveLocked()
			t.mu.Unlock()
			running++
			var grp *bonGroup
			if bonEligible(bonMode, specs[id]) {
				grp = &bonGroup{}
				groupsMu.Lock()
				groups[id] = grp
				groupsMu.Unlock()
				newGroups, newSpecs = append(newGroups, grp), append(newSpecs, specs[id])
			}
			go func(id, node string, grp *bonGroup) {
				var once sync.Once
				primary := node
				release := func() { once.Do(func() { c.releaseNode(primary) }) }
				var err error
				if grp != nil {
					node, err = c.runBestOf(ctx, t, specs[id], node, grp, release)
					groupsMu.Lock()
					delete(groups, id)
					groupsMu.Unlock()
				} else {
					err = c.runSubtask(ctx, t, specs[id], node)
				}
				release()
				if err == nil {
					t.mu.Lock()
					review := t.Review
					t.mu.Unlock()
					if review && specs[id].Kind != "investigate" {
						c.reviewSubtask(ctx, t, specs[id], node)
					}
					t.mu.Lock()
					st := t.subtask(id)
					now := time.Now()
					st.Status, st.Ended = "done", &now
					t.saveLocked()
					t.mu.Unlock()
				}
				done <- doneMsg{id, err}
			}(id, node, grp)
		}
		// Best-of-N: spare nodes nobody is waiting for take extra copies of a
		// subtask that just started; if a ready subtask finds no free node,
		// extra copies give theirs up.
		unplaced := len(ready) - placed
		free := c.freeScores(scores, "")
		if unplaced == 0 {
			for i, grp := range newGroups {
				picked := pickExtraNodes(free)
				var got []string
				for _, n := range picked {
					if c.tryReserve(n) {
						got = append(got, n)
					}
				}
				if len(got) < 1 {
					break
				}
				c.launchExtras(ctx, t, grp, newSpecs[i], got)
				free = c.freeScores(scores, "")
			}
		} else if len(free) == 0 {
			groupsMu.Lock()
			gs := make([]*bonGroup, 0, len(groups))
			ids := make([]string, 0, len(groups))
			for id, g := range groups {
				gs, ids = append(gs, g), append(ids, id)
			}
			groupsMu.Unlock()
			need := unplaced
			for i, g := range gs {
				if need <= 0 {
					break
				}
				need -= c.cancelExtras(t, g, ids[i], need)
			}
		}
		select {
		case <-ctx.Done():
			return
		case m := <-done:
			running--
			if errors.Is(m.err, errCancelled) {
				return
			}
			if errors.Is(m.err, errRequeue) {
				t.mu.Lock()
				st := state[m.id]
				st.Status, st.Node, st.NodeName, st.Started = "pending", "", "", nil
				t.saveLocked()
				t.mu.Unlock()
				continue
			}
			if m.err != nil {
				// Last resort: the planner writes the subtask itself.
				t.logf("warn", "Subtask %s failed on workers (%v); asking the planner to write it.", m.id, m.err)
				err := c.plannerWritesSubtask(ctx, t, specs[m.id])
				if errors.Is(err, errCancelled) {
					return
				}
				t.mu.Lock()
				st := state[m.id]
				now := time.Now()
				st.Ended = &now
				if err != nil {
					st.Status, st.Error = "failed", err.Error()
				} else {
					st.Status, st.ByPlanner, st.Error = "done", true, ""
				}
				t.saveLocked()
				t.mu.Unlock()
			}
		case <-time.After(2 * time.Second):
		}
	}
	c.visualCheck(ctx, t)
	if ctx.Err() != nil {
		return
	}
	c.testAndFix(ctx, t)
}

const workerSystem = `You are a careful coding worker on a team. Write complete, working code for the files you are assigned, following the plan's interfaces exactly so your files fit with your teammates' files.

` + fileFormatHelp + `
Output only your assigned files, with no explanations before or after.`

// subtaskResult is what a worker produced for a subtask, before it is written.
type subtaskResult struct {
	files map[string]string
	resp  *llmResponse
}

// runSubtask asks a worker node to write a subtask's files, retrying with feedback.
func (c *Controller) runSubtask(ctx context.Context, t *Task, spec PlanSubtask, node string) error {
	if spec.Kind == "investigate" {
		return c.investigate(ctx, t, spec, node)
	}
	res, err := c.generateSubtask(ctx, t, spec, node, "", true)
	if err != nil {
		return err
	}
	return c.commitSubtask(t, spec, node, res)
}

// generateSubtask gets the files for a subtask from one node without writing
// them. extra marks a best-of-N copy: it adds to the label and does not count
// as an attempt on the subtask.
func (c *Controller) generateSubtask(ctx context.Context, t *Task, spec PlanSubtask, node, extra string, primary bool) (*subtaskResult, error) {
	ctxTokens := c.nodeCtx(node)
	maxOut := 4096
	if maxOut > ctxTokens/2 {
		maxOut = ctxTokens / 2
	}
	budget := (ctxTokens-maxOut)*3 - len(workerSystem) - 1500
	feedback := ""
	var lastErr error
	for attempt := 1; attempt <= workerAttempts; attempt++ {
		user := c.subtaskPrompt(t, spec, budget) + feedback
		label := fmt.Sprintf("%s · %s%s (attempt %d)", t.Title, spec.ID, extra, attempt)
		t.logf("info", "%s: %s → %s (attempt %d)", spec.ID, spec.Title, c.nodeLabel(node), attempt)
		resp, err := c.nodeCall(ctx, t, node, llmRequest{System: workerSystem, User: user, MaxTokens: maxOut, Temperature: 0.2, Label: label, Sub: spec.ID}, false)
		if err != nil && !errors.Is(err, errCancelled) && isNodeProblem(err) {
			t.logf("warn", "%s is not ready (%v); %s goes back in the queue.", c.nodeLabel(node), err, spec.ID)
			c.coolDown(node, 15*time.Second)
			return nil, errRequeue
		}
		if primary {
			t.mu.Lock()
			t.subtask(spec.ID).Attempts++
			t.mu.Unlock()
		}
		if err != nil {
			if errors.Is(err, errCancelled) {
				return nil, err
			}
			lastErr = err
			continue
		}
		blocks := parseFileBlocks(resp.Text)
		got := map[string]string{}
		for _, b := range blocks {
			got[strings.TrimPrefix(b.Path, "./")] = b.Content
		}
		var missing []string
		for _, f := range spec.Files {
			if strings.TrimSpace(got[f]) == "" {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			lastErr = fmt.Errorf("missing %s", strings.Join(missing, ", "))
			feedback = "\n\nYour previous answer did not include complete content for: " + strings.Join(missing, ", ") +
				". Output every assigned file using the <<<FILE path>>> ... <<<END>>> format."
			continue
		}
		files := map[string]string{}
		for _, f := range spec.Files {
			files[f] = got[f]
		}
		return &subtaskResult{files: files, resp: resp}, nil
	}
	return nil, lastErr
}

// commitSubtask writes a generated result into the task's files.
func (c *Controller) commitSubtask(t *Task, spec PlanSubtask, node string, res *subtaskResult) error {
	for _, f := range spec.Files {
		if err := t.writeFile(f, res.files[f]); err != nil {
			return err
		}
	}
	t.touch(spec.Files...)
	resp := res.resp
	t.mu.Lock()
	st := t.subtask(spec.ID)
	now := time.Now()
	st.Status, st.Ended, st.TokPerSec = "written", &now, resp.TokensPerSec
	t.saveLocked()
	t.mu.Unlock()
	c.recordSpeed(node, resp.TokensPerSec)
	t.logf("info", "%s written on %s (%s output tokens, %.1f tok/s).", spec.ID, c.nodeLabel(node), fmtInt(resp.OutTokens), resp.TokensPerSec)
	return nil
}

func (t *Task) subtask(id string) *SubtaskState {
	for _, s := range t.Subtasks {
		if s.ID == id {
			return s
		}
	}
	s := &SubtaskState{ID: id}
	t.Subtasks = append(t.Subtasks, s)
	return s
}

func (c *Controller) subtaskPrompt(t *Task, spec PlanSubtask, budget int) string {
	return c.subtaskPromptWith(t, spec, budget, true)
}

// subtaskPromptWith builds a worker prompt; withCurrent adds the current
// content of existing files the subtask changes (not wanted for reviews,
// which see the new content anyway).
func (c *Controller) subtaskPromptWith(t *Task, spec PlanSubtask, budget int, withCurrent bool) string {
	t.mu.Lock()
	plan := t.Plan
	t.mu.Unlock()
	var b strings.Builder
	b.WriteString(t.instructionsBlock())
	fmt.Fprintf(&b, "Project: %s\n%s\nLanguage: %s\n", plan.Title, plan.Summary, plan.Language)
	if plan.TestCommand != "" {
		fmt.Fprintf(&b, "Tests run with: %s\n", plan.TestCommand)
	}
	b.WriteString("\nAll subtasks (other workers write the files not assigned to you):\n")
	for _, s := range plan.Subtasks {
		fmt.Fprintf(&b, "- %s %s: %s\n", s.ID, s.Title, strings.Join(s.Files, ", "))
	}
	fmt.Fprintf(&b, "\nYour subtask %s: %s\n%s\n\nWrite these files: %s\n", spec.ID, spec.Title, spec.Instructions, strings.Join(spec.Files, ", "))
	if note := t.steeringText(); note != "" {
		b.WriteString(note)
	}
	// Files this subtask changes that already exist (follow-up rounds).
	var existing []string
	for _, f := range spec.Files {
		if _, err := os.Stat(filepath.Join(t.repoDir(), filepath.FromSlash(f))); err == nil {
			existing = append(existing, f)
		}
	}
	if len(existing) > 0 && withCurrent {
		b.WriteString("\nCurrent content of the existing files you are changing. Output each one complete, with your changes:\n")
		for _, f := range existing {
			data, _ := os.ReadFile(filepath.Join(t.repoDir(), filepath.FromSlash(f)))
			s := string(data)
			if room := budget/2 - b.Len(); len(s) > room {
				if room < 200 {
					room = 200
				}
				if len(s) > room {
					s = s[:room] + "\n…(truncated)\n"
				}
			}
			fmt.Fprintf(&b, "<<<FILE %s>>>\n%s<<<END>>>\n", f, s)
		}
	}
	b.WriteString(c.dependencyReports(t, spec))
	// Dependencies' files, trimmed to fit the worker's context window.
	var depFiles []string
	for _, d := range spec.DependsOn {
		for _, s := range plan.Subtasks {
			if s.ID == d {
				depFiles = append(depFiles, s.Files...)
			}
		}
	}
	if len(depFiles) > 0 {
		b.WriteString("\nFiles you depend on (already written):\n")
		remaining := budget - b.Len()
		indexed := repoNeedsIndex(t.repoDir(), budget*2)
		for _, f := range depFiles {
			data, err := os.ReadFile(filepath.Join(t.repoDir(), filepath.FromSlash(f)))
			if err != nil {
				continue
			}
			s := string(data)
			if indexed {
				// Large project: declarations only, not whole files.
				s = declsText(t.repoDir(), f)
			}
			if remaining <= 200 {
				fmt.Fprintf(&b, "<<<FILE %s>>>\n(omitted to fit the context window)\n<<<END>>>\n", f)
				continue
			}
			if len(s) > remaining-100 {
				s = s[:remaining-100] + "\n…(truncated)\n"
			}
			fmt.Fprintf(&b, "<<<FILE %s>>>\n%s<<<END>>>\n", f, s)
			remaining -= len(s) + len(f) + 30
		}
	}
	return b.String()
}

func writeRepoFile(repo, rel, content string) error {
	if inGitDir(rel) {
		return fmt.Errorf("refusing to write inside .git: %s", rel)
	}
	p, err := safeJoin(repo, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
		mode = st.Mode().Perm() // keep an existing script executable
	}
	return writeFileAtomic(p, []byte(content), mode)
}

// repoSnapshot returns the project's files as text for a planner prompt.
func repoSnapshot(repo string, budget int) string {
	return repoSnapshotFirst(repo, budget, "")
}

// repoSnapshotFirst is repoSnapshot, but files whose names appear in hint (a
// test output) come first, so they are the last to be left out of a small
// prompt.
func repoSnapshotFirst(repo string, budget int, hint string) string {
	files := promptFiles(repo)
	if hint != "" {
		sort.SliceStable(files, func(i, j int) bool {
			return strings.Contains(hint, path.Base(files[i].Path)) && !strings.Contains(hint, path.Base(files[j].Path))
		})
	}
	var b strings.Builder
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(f.Path)))
		if err != nil {
			continue
		}
		s := string(data)
		if b.Len()+len(s) > budget {
			fmt.Fprintf(&b, "<<<FILE %s>>>\n(omitted: too large for this prompt; declarations follow)\n%s<<<END>>>\n", f.Path, declsText(repo, f.Path))
			continue
		}
		fmt.Fprintf(&b, "<<<FILE %s>>>\n%s<<<END>>>\n", f.Path, s)
	}
	return b.String()
}

func (c *Controller) plannerBudget(t *Task) int {
	t.mu.Lock()
	planner, orModel := t.Planner, t.ORModel
	t.mu.Unlock()
	if planner == "claude" {
		return 400000
	}
	if planner == "openrouter" {
		ctxTok := 128000 // unknown model: assume a common window
		if m := openRouterCatalog().Get(orModel); m != nil && m.Ctx > 0 {
			ctxTok = m.Ctx
		}
		return min(400000, max(8000, (ctxTok-16384)*3))
	}
	ctxTok := c.nodeCtx(strings.TrimPrefix(planner, "node:"))
	return (ctxTok - 4096) * 3
}

const plannerWriteSystem = `You are the lead engineer. A worker could not complete a subtask, so you write its files yourself, following the plan's interfaces exactly.

` + fileFormatHelp

func (c *Controller) plannerWritesSubtask(ctx context.Context, t *Task, spec PlanSubtask) error {
	if spec.Kind == "investigate" {
		return c.investigate(ctx, t, spec, "")
	}
	user := c.subtaskPrompt(t, spec, c.plannerBudget(t)/2) + "\n\nCurrent project files:\n" + repoSnapshot(t.repoDir(), c.plannerBudget(t)/2)
	resp, err := c.plannerCall(ctx, t, llmRequest{System: plannerWriteSystem, User: user, MaxTokens: 16000, Temperature: 0.2, Label: "Writing " + spec.ID})
	if err != nil {
		return err
	}
	got := map[string]string{}
	for _, b := range parseFileBlocks(resp.Text) {
		got[strings.TrimPrefix(b.Path, "./")] = b.Content
	}
	for _, f := range spec.Files {
		if strings.TrimSpace(got[f]) == "" {
			return fmt.Errorf("the planner did not write %s", f)
		}
	}
	for _, f := range spec.Files {
		if err := t.writeFile(f, got[f]); err != nil {
			return err
		}
	}
	t.touch(spec.Files...)
	return nil
}
