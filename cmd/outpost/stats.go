package main

// One-line result summary (idea 24): the Finished card says how long the
// round took, which nodes wrote it, how many subtasks needed another try, the
// fix rounds, the cloud cost and roughly how many lines changed.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type roundStats struct {
	Seconds   int      `json:"seconds,omitempty"`
	Nodes     []string `json:"nodes,omitempty"`
	Retried   int      `json:"retried,omitempty"`
	FixRounds int      `json:"fix_rounds,omitempty"`
	CostUSD   float64  `json:"cost_usd,omitempty"`
	Files     int      `json:"files,omitempty"`
	Added     int      `json:"lines_added,omitempty"`
	Removed   int      `json:"lines_removed,omitempty"`
}

// lineDelta counts lines only in after (added) and only in before (removed),
// as multisets: close to a diff's counts without computing the diff.
func lineDelta(before, after string) (added, removed int) {
	count := map[string]int{}
	if before != "" {
		for _, l := range strings.Split(strings.TrimRight(before, "\n"), "\n") {
			count[l]++
		}
	}
	if after != "" {
		for _, l := range strings.Split(strings.TrimRight(after, "\n"), "\n") {
			if count[l] > 0 {
				count[l]--
			} else {
				added++
			}
		}
	}
	for _, n := range count {
		removed += n
	}
	return
}

func (c *Controller) roundStats(t *Task) *roundStats {
	t.mu.Lock()
	st := &roundStats{FixRounds: t.FixRounds, CostUSD: t.CostUSD}
	if t.Approved != nil {
		st.Seconds = int(time.Since(*t.Approved).Seconds())
	}
	seen := map[string]bool{}
	for _, s := range t.Subtasks {
		if s.Attempts > 1 { // the first try counts as an attempt too
			st.Retried++
		}
		name := s.NodeName
		if s.ByPlanner {
			name = "planner"
		}
		if name != "" && !seen[name] {
			seen[name] = true
			st.Nodes = append(st.Nodes, name)
		}
	}
	n := t.Round
	t.mu.Unlock()
	if n == 0 {
		n = 1
	}
	sort.Strings(st.Nodes)
	dir := t.roundDir(n)
	m := readManifest(dir)
	st.Files = len(m.Files)
	for p, status := range m.Files {
		var before, after string
		if status != "added" {
			b, _ := os.ReadFile(filepath.Join(dir, "orig", filepath.FromSlash(p)))
			before = string(b)
		}
		if cur, err := safeJoin(t.repoDir(), p); err == nil {
			b, _ := os.ReadFile(cur)
			after = string(b)
		}
		a, r := lineDelta(before, after)
		st.Added += a
		st.Removed += r
	}
	return st
}
