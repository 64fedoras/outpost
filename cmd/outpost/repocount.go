package main

// The pushed state (state_stream.go) shows each project's file count and is
// rebuilt every second while a browser tab is open. Walking every project's
// folder that often is wasted work on a large repository (node_modules, a
// vendor folder), so the count is cached for a few seconds.

import (
	"io/fs"
	"path/filepath"
	"sync"
	"time"
)

const repoCountTTL = 10 * time.Second

type repoCount struct {
	n  int
	at time.Time
}

var repoCounts = struct {
	mu sync.Mutex
	m  map[string]repoCount
}{m: map[string]repoCount{}}

// countRepoFiles is len(listRepo(dir)), at most repoCountTTL old.
func countRepoFiles(dir string) int {
	now := time.Now()
	repoCounts.mu.Lock()
	rc, ok := repoCounts.m[dir]
	repoCounts.mu.Unlock()
	if ok && now.Sub(rc.at) < repoCountTTL {
		return rc.n
	}
	n := 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	repoCounts.mu.Lock()
	repoCounts.m[dir] = repoCount{n, now}
	repoCounts.mu.Unlock()
	return n
}
