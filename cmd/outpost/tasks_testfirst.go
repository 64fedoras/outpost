package main

// Tests first: with the "tests first" option the planner is told to make the
// first subtask write the test files, and every other subtask is made to depend
// on it, so each worker is given the tests it must satisfy. The fix rounds then
// have a fixed target instead of whatever the model thinks is wrong.

import (
	"fmt"
	"path"
	"strings"
)

const testFirstRules = `

Tests first: the user asked for tests before code. Make the FIRST subtask write only the test files (names containing "test" or in a tests directory), with no dependencies, and give a test_command that runs them. Describe exactly what each test checks in that subtask's instructions and keep the tests small and deterministic. Every other subtask will be given those test files and must make them pass without editing them.`

func looksLikeTestFile(f string) bool {
	l := strings.ToLower(f)
	base := path.Base(l)
	return strings.Contains(base, "test") || strings.Contains(base, "spec") || strings.HasPrefix(l, "tests/") || strings.Contains(l, "/tests/") || strings.Contains(l, "/__tests__/")
}

// enforceTestFirst finds the subtask that writes only test files, clears its
// dependencies and makes every other subtask depend on it. It returns an error
// when the plan has no such subtask or no test command, so the planner is asked again.
func enforceTestFirst(p *Plan) error {
	if strings.TrimSpace(p.TestCommand) == "" {
		return fmt.Errorf("tests first needs a test_command that runs the tests")
	}
	idx := -1
	for i, s := range p.Subtasks {
		if len(s.Files) == 0 {
			continue
		}
		all := true
		for _, f := range s.Files {
			if !looksLikeTestFile(f) {
				all = false
			}
		}
		if all {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("tests first needs a subtask that writes only the test files")
	}
	id := p.Subtasks[idx].ID
	p.Subtasks[idx].DependsOn = nil
	for i := range p.Subtasks {
		if i == idx {
			continue
		}
		has := false
		for _, d := range p.Subtasks[i].DependsOn {
			if d == id {
				has = true
			}
		}
		if !has {
			p.Subtasks[i].DependsOn = append(p.Subtasks[i].DependsOn, id)
		}
	}
	return nil
}
