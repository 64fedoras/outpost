package main

import (
	"fmt"
)

// updatePreflight lists reasons a node would probably fail to install rel.
// An update downloads the binary, keeps a verified copy and keeps the previous
// version for rollback, so it needs about three times the release size free
// where the node keeps its files. A node that has not reported free space
// (0) is not blocked. The node's OS version and C library are not reported,
// so those are not checked.
func updatePreflight(h *Hello, rel *Release) []string {
	var problems []string
	if h == nil || rel == nil {
		return nil
	}
	need := uint64(rel.Bytes) * 3
	if h.WorkFreeBytes > 0 && need > 0 && h.WorkFreeBytes < need {
		problems = append(problems, fmt.Sprintf("only %s free on the node, an update needs about %s", humanBytes(int64(h.WorkFreeBytes)), humanBytes(int64(need))))
	}
	return problems
}
