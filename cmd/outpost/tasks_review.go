package main

// Peer review of finished subtasks.
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

const reviewSystem = `You are a careful senior engineer reviewing a teammate's code for one subtask of a larger project. Check it against the subtask instructions and the project's interfaces: names, signatures, imports, file paths, data formats, and obvious bugs.

If the files are correct, answer with exactly:
<<<OK>>>

If something is wrong, output the corrected files, each one complete, and nothing else.
` + fileFormatHelp + `
Only output files that belong to this subtask. Do not rewrite code that is already correct.`

// reviewSubtask asks a different node to check a subtask's files and fix them.
// A review never fails the subtask: if no second node is free, the reviewer
// errors, or its answer looks broken, the writer's files are kept.
func (c *Controller) reviewSubtask(ctx context.Context, t *Task, spec PlanSubtask, writer string) {
	note := func(msg string) {
		t.mu.Lock()
		t.subtask(spec.ID).ReviewNote = msg
		t.saveLocked()
		t.mu.Unlock()
	}
	reviewer := ""
	deadline := time.Now().Add(2 * time.Minute)
	for reviewer == "" {
		others := 0
		workers := c.taskWorkers(t)
		for _, w := range workers {
			if w != writer {
				others++
			}
		}
		scores := c.scoreNodes(workers)
		var ws nodeScore
		for _, s := range scores {
			if s.ID == writer {
				ws = s
			}
		}
		// A reviewer at least as strong as the writer when one is free.
		if w := pickReviewer(ws, c.freeScores(scores, writer)); w != "" && c.tryReserve(w) {
			reviewer = w
			break
		}
		if others == 0 {
			note("skipped: no second node with a model")
			return
		}
		if time.Now().After(deadline) {
			note("skipped: no second node was free for 2 minutes")
			t.logf("warn", "%s: review skipped, no second node was free.", spec.ID)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	defer c.releaseNode(reviewer)

	t.mu.Lock()
	st := t.subtask(spec.ID)
	st.Status, st.Reviewer = "reviewing", c.nodeLabel(reviewer)
	t.saveLocked()
	t.mu.Unlock()
	t.logf("info", "%s: reviewing on %s", spec.ID, c.nodeLabel(reviewer))

	ctxTokens := c.nodeCtx(reviewer)
	maxOut := 4096
	if maxOut > ctxTokens/2 {
		maxOut = ctxTokens / 2
	}
	budget := (ctxTokens-maxOut)*3 - len(reviewSystem) - 1500
	original := map[string]string{}
	var written strings.Builder
	for _, f := range spec.Files {
		b, err := os.ReadFile(filepath.Join(t.repoDir(), filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		original[f] = string(b)
		fmt.Fprintf(&written, "<<<FILE %s>>>\n%s<<<END>>>\n", f, b)
	}
	prompt := c.subtaskPromptWith(t, spec, budget-written.Len(), false)
	prompt = strings.Replace(prompt, "Write these files:", "Files in this subtask:", 1)
	user := prompt + "\nYour teammate wrote:\n" + written.String()
	resp, err := c.nodeCall(ctx, t, reviewer, llmRequest{System: reviewSystem, User: user, MaxTokens: maxOut, Temperature: 0.1,
		Label: fmt.Sprintf("%s · review %s", t.Title, spec.ID), Sub: spec.ID}, false)
	if err != nil {
		if !errors.Is(err, errCancelled) {
			note("skipped: " + err.Error())
			t.logf("warn", "%s: review on %s failed (%v); keeping the original.", spec.ID, c.nodeLabel(reviewer), err)
		}
		return
	}
	blocks := parseFileBlocks(resp.Text)
	if len(blocks) == 0 {
		if strings.Contains(resp.Text, "<<<OK>>>") {
			t.logf("info", "%s: approved by %s.", spec.ID, c.nodeLabel(reviewer))
		} else {
			note("reviewer gave no usable answer")
			t.logf("warn", "%s: %s gave no usable review; keeping the original.", spec.ID, c.nodeLabel(reviewer))
		}
		return
	}
	var changed []string
	for _, b := range blocks {
		p := strings.TrimPrefix(b.Path, "./")
		orig, ok := original[p]
		if !ok {
			continue // not part of this subtask
		}
		// Guard against a truncated rewrite from a small model.
		if strings.TrimSpace(b.Content) == "" || (len(orig) > 400 && len(b.Content) < len(orig)*2/5) {
			t.logf("warn", "%s: ignored %s's rewrite of %s (much shorter than the original, probably cut off).", spec.ID, c.nodeLabel(reviewer), p)
			continue
		}
		if b.Content == orig {
			continue
		}
		if err := t.writeFile(p, b.Content); err == nil {
			changed = append(changed, p)
		}
	}
	if len(changed) == 0 {
		t.logf("info", "%s: approved by %s.", spec.ID, c.nodeLabel(reviewer))
		return
	}
	t.mu.Lock()
	t.subtask(spec.ID).ReviewFix = true
	t.saveLocked()
	t.mu.Unlock()
	t.logf("info", "%s: %s fixed %s.", spec.ID, c.nodeLabel(reviewer), strings.Join(changed, ", "))
}

// roundHasFiles reports whether the task repo holds any files yet.
func (t *Task) roundHasFiles() bool { return len(listRepo(t.repoDir())) > 0 }
