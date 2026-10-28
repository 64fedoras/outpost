package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// markModelBusy records that a model's files are being written; the returned
// func clears it. Cleanup never touches a busy model.
func (n *Node) markModelBusy(name string) func() {
	n.mu.Lock()
	if n.busyModels == nil {
		n.busyModels = map[string]int{}
	}
	n.busyModels[name]++
	n.mu.Unlock()
	return func() {
		n.mu.Lock()
		if n.busyModels[name]--; n.busyModels[name] <= 0 {
			delete(n.busyModels, name)
		}
		n.mu.Unlock()
	}
}

// protectedModels are the models a cleanup must keep: the one loaded (or
// stopped for idleness, which reloads on demand), the one the node reloads
// after a restart, and any being copied or downloaded right now.
func (n *Node) protectedModels() map[string]bool {
	p := map[string]bool{}
	if m := n.llama.loadedModel(); m != "" {
		p[m] = true
	}
	if m := n.llama.coldModel(); m != "" {
		p[m] = true
	}
	if b, err := os.ReadFile(n.autoloadPath()); err == nil {
		var ml ModelLoad
		if json.Unmarshal(b, &ml) == nil && ml.Name != "" {
			p[ml.Name] = true
		}
	}
	n.mu.Lock()
	for m := range n.busyModels {
		p[m] = true
	}
	n.mu.Unlock()
	return p
}

// dirBytes sums the regular files under dir without following symlinks.
func dirBytes(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// cleanupModels deletes the named model folders from this node's disk, except
// the protected ones. With req.Incomplete it also deletes folders that have no
// manifest (interrupted downloads). Only direct children of the models folder
// that are real directories are touched: validName rules out separators and
// "..", and a symlinked folder is skipped rather than followed.
func (n *Node) cleanupModels(req ModelCleanup) {
	prot := n.protectedModels()
	seen := map[string]bool{}
	var names []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			names = append(names, s)
		}
	}
	for _, s := range req.Names {
		add(s)
	}
	if req.Incomplete {
		entries, _ := os.ReadDir(n.modelsDir())
		for _, e := range entries {
			if e.IsDir() {
				if _, err := n.modelManifest(e.Name()); err != nil {
					add(e.Name())
				}
			}
		}
	}
	sort.Strings(names)
	var freed int64
	var removed, kept int
	var firstErr string
	for _, name := range names {
		if !validName(name) {
			continue
		}
		if prot[name] {
			kept++
			continue
		}
		dir := filepath.Join(n.modelsDir(), name)
		if st, err := os.Lstat(dir); err != nil || !st.IsDir() {
			continue // gone already, or a symlink or file: never followed
		}
		size := dirBytes(dir)
		if err := os.RemoveAll(dir); err != nil {
			if firstErr == "" {
				firstErr = fmt.Sprintf("%s: %v", name, err)
			}
			continue
		}
		freed += size
		removed++
	}
	st := OpStatus{Op: "model.cleanup", State: "done", BytesDone: freed,
		Detail: fmt.Sprintf("%d removed, %s freed", removed, humanBytes(freed))}
	if kept > 0 {
		st.Detail += fmt.Sprintf("; %d in use, kept", kept)
	}
	if firstErr != "" {
		st.State, st.Error = "failed", firstErr
	}
	_ = n.send("node.op_status", st)
	n.resendHello()
}
