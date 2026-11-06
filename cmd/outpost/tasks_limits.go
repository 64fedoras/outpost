package main

import (
	"strings"
	"time"
)

// Per-task limits (idea 17): how many fix rounds to try after failing tests
// and how long one test run may take. Set on the new-task options or as a
// project default; 0 means the built-in default.

func clampFixRounds(n int) int {
	switch {
	case n <= 0:
		return 0
	case n > 8:
		return 8
	}
	return n
}

func clampTestMinutes(n int) int {
	switch {
	case n <= 0:
		return 0
	case n > 120:
		return 120
	}
	return n
}

// fixLimit is the number of fix rounds for this task.
func (t *Task) fixLimit() int {
	t.mu.Lock()
	n := t.MaxFix
	t.mu.Unlock()
	if n <= 0 {
		return maxFixRounds
	}
	return n
}

// testLimit is the time limit for one test run of this task.
func (t *Task) testLimit() time.Duration {
	t.mu.Lock()
	m := t.TestMinutes
	t.mu.Unlock()
	if m <= 0 {
		return testTimeout
	}
	return time.Duration(m) * time.Minute
}

func projectInstructions(p *Project) string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Instructions
}

// joinInstructions puts the Settings rules first, then the project's.
func joinInstructions(global, project string) string {
	global, project = strings.TrimSpace(global), strings.TrimSpace(project)
	switch {
	case global == "":
		return clipInstructions(project)
	case project == "":
		return clipInstructions(global)
	}
	return clipInstructions(global + "\n\n" + project)
}
