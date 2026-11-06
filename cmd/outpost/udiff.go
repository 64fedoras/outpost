package main

// A small unified diff writer (git style) for patch downloads. Lines are
// compared with the Myers O(ND) algorithm; files over maxDiffLines lines on
// either side are shown as a whole replacement instead.

import (
	"fmt"
	"strings"
)

const (
	diffContext  = 3
	maxDiffLines = 20000
)

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	ls := strings.SplitAfter(s, "\n")
	if ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	return ls
}

// diffOp is one line of an edit script: ' ' keep, '-' remove, '+' add.
type diffOp struct {
	kind byte
	line string
}

// myers returns the edit script turning a into b.
func myers(a, b []string) []diffOp {
	n, m := len(a), len(b)
	max := n + m
	if max == 0 {
		return nil
	}
	v := make([]int, 2*max+2)
	var trace [][]int
	off := max + 1
	found := false
	for d := 0; d <= max && !found; d++ {
		snap := make([]int, len(v))
		copy(snap, v)
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				found = true
				break
			}
		}
	}
	// Walk back through the trace.
	var ops []diffOp
	x, y := n, m
	for d := len(trace) - 1; d >= 0 && (x > 0 || y > 0); d-- {
		vv := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && vv[off+k-1] < vv[off+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := vv[off+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			ops = append(ops, diffOp{' ', a[x]})
		}
		if d == 0 {
			break
		}
		if x == prevX {
			y--
			ops = append(ops, diffOp{'+', b[y]})
		} else {
			x--
			ops = append(ops, diffOp{'-', a[x]})
		}
	}
	for x > 0 && y > 0 { // leading common lines
		x--
		y--
		ops = append(ops, diffOp{' ', a[x]})
	}
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops
}

// unifiedDiff writes one file's diff in git's format. before or after "" with
// existed false means the file was added or deleted.
func unifiedDiff(path, before, after string, beforeExists, afterExists bool) string {
	if before == after && beforeExists == afterExists {
		return ""
	}
	a, b := splitLines(before), splitLines(after)
	var sb strings.Builder
	fmt.Fprintf(&sb, "diff --git a/%s b/%s\n", path, path)
	from, to := "a/"+path, "b/"+path
	switch {
	case !beforeExists:
		sb.WriteString("new file mode 100644\n")
		from = "/dev/null"
	case !afterExists:
		sb.WriteString("deleted file mode 100644\n")
		to = "/dev/null"
	}
	if len(a) == 0 && len(b) == 0 {
		return sb.String()
	}
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", from, to)
	var ops []diffOp
	if len(a) > maxDiffLines || len(b) > maxDiffLines {
		for _, l := range a {
			ops = append(ops, diffOp{'-', l})
		}
		for _, l := range b {
			ops = append(ops, diffOp{'+', l})
		}
	} else {
		ops = myers(a, b)
	}
	// Group into hunks with diffContext lines around each change.
	type span struct{ s, e int }
	var spans []span
	for i, op := range ops {
		if op.kind == ' ' {
			continue
		}
		s, e := i-diffContext, i+diffContext+1
		if s < 0 {
			s = 0
		}
		if e > len(ops) {
			e = len(ops)
		}
		if len(spans) > 0 && s <= spans[len(spans)-1].e {
			spans[len(spans)-1].e = e
		} else {
			spans = append(spans, span{s, e})
		}
	}
	// Line numbers at the start of each op.
	ai, bi := make([]int, len(ops)+1), make([]int, len(ops)+1)
	for i, op := range ops {
		ai[i+1], bi[i+1] = ai[i], bi[i]
		if op.kind != '+' {
			ai[i+1]++
		}
		if op.kind != '-' {
			bi[i+1]++
		}
	}
	for _, sp := range spans {
		na, nb := ai[sp.e]-ai[sp.s], bi[sp.e]-bi[sp.s]
		sa, sbn := ai[sp.s]+1, bi[sp.s]+1
		if na == 0 {
			sa--
		}
		if nb == 0 {
			sbn--
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", sa, na, sbn, nb)
		for _, op := range ops[sp.s:sp.e] {
			sb.WriteByte(op.kind)
			sb.WriteString(op.line)
			if !strings.HasSuffix(op.line, "\n") {
				sb.WriteString("\n\\ No newline at end of file\n")
			}
		}
	}
	return sb.String()
}
