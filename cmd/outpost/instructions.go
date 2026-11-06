package main

import (
	"os"
	"path/filepath"
	"strings"
)

// maxInstructions caps the standing rules so they cannot crowd a small
// local model's context window.
const maxInstructions = 8000

func clipInstructions(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxInstructions {
		s = strings.ToValidUTF8(s[:maxInstructions], "")
	}
	return s
}

// instructionsText is the task's standing rules: the text copied from
// Settings when the task was created, then the OUTPOST.md at the root of the
// task's files if there is one (a follow-up round on a project that has its
// own). Projects can later replace the settings text with their own.
func (t *Task) instructionsText() string {
	t.mu.Lock()
	text := t.Instructions
	t.mu.Unlock()
	if b, err := os.ReadFile(filepath.Join(t.repoDir(), "OUTPOST.md")); err == nil {
		if f := strings.TrimSpace(string(b)); f != "" {
			if text != "" {
				text += "\n\n"
			}
			text += f
		}
	}
	return clipInstructions(text)
}

// instructionsBlock is the text put at the top of planner, worker, review
// and fix prompts, or "" when there are none.
func (t *Task) instructionsBlock() string {
	s := t.instructionsText()
	if s == "" {
		return ""
	}
	return "Project instructions (standing rules; follow them):\n" + s + "\n\n"
}
