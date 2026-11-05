package main

// Per-round file history. Before a round writes a file for the first time, the
// controller keeps the file's previous content in tasks/<id>/rounds/<n>/, so the
// web UI can show what the round changed and put the files back (Undo a round).

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
)

// snapMu serialises snapshots: subtasks of one round write files in parallel.
var snapMu sync.Mutex

type roundManifest struct {
	Files map[string]string `json:"files"` // path -> "modified" | "added"
}

func (t *Task) roundsDir() string { return filepath.Join(t.dir, "rounds") }

func (t *Task) roundDir(n int) string { return filepath.Join(t.roundsDir(), strconv.Itoa(n)) }

func readManifest(dir string) roundManifest {
	m := roundManifest{Files: map[string]string{}}
	if b, err := os.ReadFile(filepath.Join(dir, "manifest.json")); err == nil {
		_ = json.Unmarshal(b, &m)
		if m.Files == nil {
			m.Files = map[string]string{}
		}
	}
	return m
}

// snapshot keeps the current content of rel the first time the current round
// writes it.
func (t *Task) snapshot(rel string) error {
	t.mu.Lock()
	n := t.Round
	t.mu.Unlock()
	if n == 0 {
		n = 1
	}
	dir := t.roundDir(n)
	cur, err := safeJoin(t.repoDir(), rel)
	if err != nil {
		return err
	}
	snapMu.Lock()
	defer snapMu.Unlock()
	m := readManifest(dir)
	if _, done := m.Files[rel]; done {
		return nil
	}
	old, err := os.ReadFile(cur)
	switch {
	case err == nil:
		keep := filepath.Join(dir, "orig", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(keep), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(keep, old, 0o644); err != nil {
			return err
		}
		m.Files[rel] = "modified"
	case os.IsNotExist(err):
		m.Files[rel] = "added"
	default:
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(m)
	return writeFileAtomic(filepath.Join(dir, "manifest.json"), b, 0o644)
}

// writeFile writes a project file, keeping its previous content for the round.
func (t *Task) writeFile(rel, content string) error {
	if err := t.snapshot(rel); err != nil {
		return err
	}
	return writeRepoFile(t.repoDir(), rel, content)
}

// roundNumbers lists the rounds that have a kept copy, ascending.
func (t *Task) roundNumbers() []int {
	ents, _ := os.ReadDir(t.roundsDir())
	var out []int
	for _, e := range ents {
		if n, err := strconv.Atoi(e.Name()); err == nil && e.IsDir() {
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

func (t *Task) idle() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch t.Status {
	case "done", "failed", "cancelled", "interrupted":
		return true
	}
	return false
}

type roundInfo struct {
	Round    int  `json:"round"`
	Files    int  `json:"files"`
	Undoable bool `json:"undoable"`
}

func (c *Controller) handleTaskRounds(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	nums := t.roundNumbers()
	idle := t.idle()
	out := []roundInfo{}
	for i, n := range nums {
		m := readManifest(t.roundDir(n))
		out = append(out, roundInfo{Round: n, Files: len(m.Files), Undoable: idle && i == len(nums)-1 && len(m.Files) > 0})
	}
	writeJSON(w, 200, map[string]any{"rounds": out})
}

type fileChange struct {
	Path   string `json:"path"`
	Status string `json:"status"` // modified | added
	Before string `json:"before"`
	After  string `json:"after"` // the file now
	Gone   bool   `json:"gone,omitempty"`
}

func readCapped(p string) (string, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	if len(b) > maxRepoFileSize {
		b = b[:maxRepoFileSize]
	}
	return string(b), true
}

func (c *Controller) handleTaskChanges(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	n, err := strconv.Atoi(r.URL.Query().Get("round"))
	if err != nil || n < 1 {
		writeJSON(w, 400, map[string]string{"error": "bad round"})
		return
	}
	dir := t.roundDir(n)
	if _, err := os.Stat(dir); err != nil {
		writeJSON(w, 404, map[string]string{"error": "no changes were kept for that round"})
		return
	}
	m := readManifest(dir)
	paths := make([]string, 0, len(m.Files))
	for p := range m.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := []fileChange{}
	for _, p := range paths {
		fc := fileChange{Path: p, Status: m.Files[p]}
		if fc.Status == "modified" {
			fc.Before, _ = readCapped(filepath.Join(dir, "orig", filepath.FromSlash(p)))
		}
		if cur, err := safeJoin(t.repoDir(), p); err == nil {
			var ok bool
			if fc.After, ok = readCapped(cur); !ok {
				fc.Gone = true
			}
		}
		out = append(out, fc)
	}
	nums := t.roundNumbers()
	writeJSON(w, 200, map[string]any{"round": n, "files": out,
		"undoable": t.idle() && len(nums) > 0 && nums[len(nums)-1] == n && len(m.Files) > 0})
}

// handleUndoRound puts back the files the latest round changed.
func (c *Controller) handleUndoRound(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	n, err := strconv.Atoi(r.URL.Query().Get("round"))
	if err != nil || n < 1 {
		writeJSON(w, 400, map[string]string{"error": "bad round"})
		return
	}
	if !t.idle() {
		writeJSON(w, 409, map[string]string{"error": "wait until the task has stopped"})
		return
	}
	if nums := t.roundNumbers(); len(nums) == 0 || nums[len(nums)-1] != n {
		writeJSON(w, 409, map[string]string{"error": "only the latest round with changes can be undone"})
		return
	}
	restored, err := t.undoRound(n)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	t.mu.Lock()
	t.Touched = nil
	t.addMsgLocked(TaskMessage{Role: "event", Kind: "event", Text: "Undid request " + strconv.Itoa(n) + ": " + strconv.Itoa(restored) + " file(s) put back as they were before it."})
	t.ResultBytes = 0
	t.mu.Unlock()
	t.logf("info", "Undid request %d (%d files).", n, restored)
	writeJSON(w, 200, map[string]any{"ok": true, "files": restored})
}

// undoRound restores round n's files and removes its kept copy.
func (t *Task) undoRound(n int) (int, error) {
	dir := t.roundDir(n)
	snapMu.Lock()
	defer snapMu.Unlock()
	m := readManifest(dir)
	count := 0
	for p, status := range m.Files {
		cur, err := safeJoin(t.repoDir(), p)
		if err != nil {
			continue
		}
		if status == "added" {
			if err := os.Remove(cur); err != nil && !os.IsNotExist(err) {
				return count, err
			}
			pruneEmptyDirs(filepath.Dir(cur), t.repoDir())
		} else {
			b, err := os.ReadFile(filepath.Join(dir, "orig", filepath.FromSlash(p)))
			if err != nil {
				return count, err
			}
			if err := os.MkdirAll(filepath.Dir(cur), 0o755); err != nil {
				return count, err
			}
			if err := os.WriteFile(cur, b, 0o644); err != nil {
				return count, err
			}
		}
		count++
	}
	return count, os.RemoveAll(dir)
}

func pruneEmptyDirs(dir, root string) {
	for dir != root && len(dir) > len(root) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
