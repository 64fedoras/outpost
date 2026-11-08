package main

// Context warnings: a worker's prompt is cut to fit the model's context window
// (tasks_dispatch.go), which used to happen without a word. These helpers say so
// in the task's log, once per subtask, so a strange result can be traced to a
// small context and fixed (load the model with a bigger context, or Redo the
// subtask on a bigger node).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// promptWasCut says whether subtaskPromptWith had to cut or leave out file content.
func promptWasCut(prompt string) bool {
	return strings.Contains(prompt, "…(truncated)") || strings.Contains(prompt, "(omitted to fit the context window)")
}

// existingFilesWarning is the plan-time version: it sums the files the subtask
// changes that already exist and compares them with the share of the prompt
// budget they may use (half). Files that other subtasks have yet to write are
// not counted. Returns "" when they fit.
func existingFilesWarning(repo string, spec PlanSubtask, budget int) string {
	total := 0
	for _, f := range spec.Files {
		if st, err := os.Stat(filepath.Join(repo, filepath.FromSlash(f))); err == nil {
			total += int(st.Size())
		}
	}
	if total > budget/2 {
		return fmt.Sprintf("%s: the existing files it changes are %d KB but only about %d KB fit in the model's context window", spec.ID, total/1024, budget/2/1024)
	}
	return ""
}

// warnContextCut logs the warning once per subtask in this run.
func (c *Controller) warnContextCut(t *Task, seen map[string]bool, spec PlanSubtask, node string, ctxTokens int) {
	if seen[spec.ID] {
		return
	}
	seen[spec.ID] = true
	t.logf("warn", "%s: some file content was cut to fit %s's context window (%d tokens), so the result may be incomplete. Load the model with a bigger context on the Nodes page, or Redo this subtask on a bigger node.", spec.ID, c.nodeLabel(node), ctxTokens)
}

// warnPlanFit checks each subtask against the smallest context among the
// nodes that can work now and logs those that will not fit, before any model call.
func (c *Controller) warnPlanFit(t *Task, plan *Plan) {
	workers := c.taskWorkers(t)
	if len(workers) == 0 {
		return
	}
	smallest := 0
	for _, w := range workers {
		if n := c.nodeCtx(w); smallest == 0 || n < smallest {
			smallest = n
		}
	}
	maxOut := 4096
	if maxOut > smallest/2 {
		maxOut = smallest / 2
	}
	budget := (smallest-maxOut)*3 - len(workerSystem) - 1500
	for _, s := range plan.Subtasks {
		if w := existingFilesWarning(t.repoDir(), s, budget); w != "" {
			t.logf("warn", "%s. Load the model with a bigger context on the Nodes page, or expect it to be cut.", w)
		}
	}
}
