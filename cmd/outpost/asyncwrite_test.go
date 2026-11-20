package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Background saves of one file land in order: after a flush the file holds
// the newest content, and its .bak a previous one.
func TestAsyncFilesNewestWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.json")
	var a asyncFiles
	var wg sync.WaitGroup
	var order sync.Mutex
	n := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 25; k++ {
				order.Lock() // the callers' c.mu: contents are handed over in order
				n++
				a.queue(path, []byte(fmt.Sprintf(`{"n":%d}`, n)), 0o600, true)
				order.Unlock()
			}
		}()
	}
	wg.Wait()
	a.flush()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != `{"n":200}` {
		t.Fatalf("file %q, %v", got, err)
	}
	if bak, err := os.ReadFile(path + ".bak"); err != nil || len(bak) == 0 {
		t.Fatalf("no backup copy: %v", err)
	}
	a.mu.Lock()
	left := len(a.pend) + len(a.busy)
	a.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d writes still pending after flush", left)
	}
	// A zero asyncFiles flushes at once.
	var z asyncFiles
	z.flush()
}
