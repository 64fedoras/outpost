package main

// Node labels (idea 45): the owner tags nodes ("gpu", "fast", "office") on the
// Nodes page, and a task or a project can ask for one label. Model work
// (subtasks, reviews, best-of-N judges) then goes only to nodes with that
// label. Test runs on exec nodes and the planner node are not filtered: a CPU
// server that runs the tests rarely carries the GPU label the writers need.

import (
	"sort"
	"strings"
)

const maxLabels = 8

// normLabel lower-cases a label and keeps letters, digits and dashes.
func normLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '_':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}

// normLabels cleans a list: no blanks, no duplicates, sorted, at most maxLabels.
func normLabels(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range in {
		if l = normLabel(l); l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	if len(out) > maxLabels {
		out = out[:maxLabels]
	}
	return out
}

func hasLabel(labels []string, l string) bool {
	for _, x := range labels {
		if x == l {
			return true
		}
	}
	return false
}

// workersWithLabel filters a worker list to nodes carrying label ("" = all).
func (c *Controller) workersWithLabel(ids []string, label string) []string {
	if label == "" {
		return ids
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, id := range ids {
		if n := c.nodes[id]; n != nil && hasLabel(n.Labels, label) {
			out = append(out, id)
		}
	}
	return out
}

// taskWorkers are the nodes that may do a task's model work, strongest first.
func (c *Controller) taskWorkers(t *Task) []string {
	t.mu.Lock()
	label := t.NodeLabel
	t.mu.Unlock()
	return c.workersWithLabel(c.workerNodes(), label)
}

// allLabels lists every label in use, for the UI's pickers.
func (c *Controller) allLabelsLocked() []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range c.nodes {
		for _, l := range n.Labels {
			if !seen[l] {
				seen[l] = true
				out = append(out, l)
			}
		}
	}
	sort.Strings(out)
	return out
}
