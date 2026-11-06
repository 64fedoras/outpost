package main

// Plans can delete and rename files (idea 16). The plan lists them; they are
// shown on the plan card before approval and applied when the round starts,
// before any subtask runs. Each one is recorded in the round's kept copy, so
// Changes shows it and Undo puts the files back.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type PlanRename struct {
	From string `json:"from"`
	To   string `json:"to"`
}

const maxFileOps = 50

func cleanRel(p string) string { return strings.TrimPrefix(strings.TrimSpace(p), "./") }

// validateFileOps cleans the delete and rename lists. owner maps the files the
// subtasks write to their subtask.
func validateFileOps(p *Plan, owner map[string]string) error {
	if len(p.Delete)+len(p.Rename) > maxFileOps {
		return fmt.Errorf("the plan deletes or renames more than %d files", maxFileOps)
	}
	seen := map[string]bool{}
	var del []string
	for _, f := range p.Delete {
		f = cleanRel(f)
		if f == "" {
			continue
		}
		if _, err := safeJoin("/repo", f); err != nil {
			return fmt.Errorf("delete has a bad file path %q", f)
		}
		if o, ok := owner[f]; ok {
			return fmt.Errorf("file %s is both deleted and written by %s", f, o)
		}
		if !seen[f] {
			seen[f] = true
			del = append(del, f)
		}
	}
	var ren []PlanRename
	for _, r := range p.Rename {
		r.From, r.To = cleanRel(r.From), cleanRel(r.To)
		if r.From == "" && r.To == "" {
			continue
		}
		if _, err := safeJoin("/repo", r.From); err != nil || r.From == "" {
			return fmt.Errorf("rename has a bad file path %q", r.From)
		}
		if _, err := safeJoin("/repo", r.To); err != nil || r.To == "" {
			return fmt.Errorf("rename has a bad file path %q", r.To)
		}
		if r.From == r.To {
			continue
		}
		if seen[r.From] {
			return fmt.Errorf("file %s appears in more than one delete or rename", r.From)
		}
		if seen[r.To] {
			return fmt.Errorf("file %s appears in more than one delete or rename", r.To)
		}
		if o, ok := owner[r.From]; ok {
			return fmt.Errorf("file %s is renamed away but written by %s; have %s write %s instead", r.From, o, o, r.To)
		}
		seen[r.From], seen[r.To] = true, true
		ren = append(ren, r)
	}
	p.Delete, p.Rename = del, ren
	return nil
}

// applyFileOps runs the plan's renames, then its deletions. It can run again
// on a resumed round: a rename whose source is gone and a deletion of a file
// that is gone are skipped.
func (c *Controller) applyFileOps(t *Task, plan *Plan) error {
	if len(plan.Delete) == 0 && len(plan.Rename) == 0 {
		return nil
	}
	repo := t.repoDir()
	var did []string
	for _, r := range plan.Rename {
		from, err := safeJoin(repo, r.From)
		if err != nil {
			return err
		}
		to, err := safeJoin(repo, r.To)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(from)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("renaming %s: %w", r.From, err)
		}
		if _, err := os.Stat(to); err == nil {
			return fmt.Errorf("cannot rename %s to %s: %s already exists", r.From, r.To, r.To)
		}
		if err := t.writeFile(r.To, string(b)); err != nil {
			return fmt.Errorf("renaming %s: %w", r.From, err)
		}
		if err := t.removeFile(r.From); err != nil {
			return fmt.Errorf("renaming %s: %w", r.From, err)
		}
		did = append(did, r.From+" → "+r.To)
	}
	var gone []string
	for _, f := range plan.Delete {
		p, err := safeJoin(repo, f)
		if err != nil {
			return err
		}
		if _, err := os.Stat(p); os.IsNotExist(err) {
			continue
		}
		if err := t.removeFile(f); err != nil {
			return fmt.Errorf("deleting %s: %w", f, err)
		}
		gone = append(gone, f)
	}
	if len(did) > 0 {
		t.logf("info", "Renamed %s.", strings.Join(did, ", "))
	}
	if len(gone) > 0 {
		t.logf("info", "Deleted %s.", strings.Join(gone, ", "))
	}
	t.mu.Lock()
	for _, r := range plan.Rename {
		t.Touched = appendUnique(t.Touched, r.To)
	}
	t.mu.Unlock()
	return nil
}

// removeFile deletes a project file, keeping its content for the round so Undo
// can put it back.
func (t *Task) removeFile(rel string) error {
	if err := t.snapshot(rel); err != nil {
		return err
	}
	t.markDeleted(rel)
	p, err := safeJoin(t.repoDir(), rel)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	pruneEmptyDirs(filepath.Dir(p), t.repoDir())
	return nil
}

// markDeleted turns the round's "modified" entry for rel into "deleted".
func (t *Task) markDeleted(rel string) {
	t.mu.Lock()
	n := t.Round
	t.mu.Unlock()
	if n == 0 {
		n = 1
	}
	dir := t.roundDir(n)
	snapMu.Lock()
	defer snapMu.Unlock()
	m := readManifest(dir)
	if m.Files[rel] == "modified" {
		m.Files[rel] = "deleted"
		writeManifest(dir, m)
	} else if m.Files[rel] == "added" {
		delete(m.Files, rel) // added and removed in the same round: nothing to undo
		writeManifest(dir, m)
	}
}

func writeManifest(dir string, m roundManifest) {
	b, _ := json.Marshal(m)
	_ = writeFileAtomic(filepath.Join(dir, "manifest.json"), b, 0o644)
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}
