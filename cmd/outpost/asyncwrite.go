package main

// State files saved while c.mu is held (nodes, sessions, API tokens) used to
// be written and flushed to disk inside the lock. On a slow or busy disk one
// fsync can take a second or more, and every web request and node message
// waited for c.mu meanwhile. Callers now encode under the lock (so the saved
// copy is consistent) and hand the bytes to asyncFiles, which writes them
// outside the lock: one writer per file, newest content wins, so saves never
// land out of order. flush waits for every pending write; it runs before a
// backup, a restart and shutdown. Model records are still written in place:
// a model's folder can be deleted, and a late write would bring it back.

import (
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// asyncStateWrites is false in tests, which write in place (see TestMain).
var asyncStateWrites = true

type asyncFiles struct {
	mu   sync.Mutex
	pend map[string]*pendingWrite // newest content not yet written, by path
	busy map[string]bool          // a writer goroutine is running for this path
}

type pendingWrite struct {
	b      []byte
	mode   os.FileMode
	backup bool // keep the previous file as .bak (writeFileAtomicBackup)
}

// write saves b to path in the background, replacing any save of the same
// path that has not started yet.
func (a *asyncFiles) write(path string, b []byte, mode os.FileMode, backup bool) {
	if !asyncStateWrites {
		writePending(path, &pendingWrite{b, mode, backup})
		return
	}
	a.queue(path, b, mode, backup)
}

func (a *asyncFiles) queue(path string, b []byte, mode os.FileMode, backup bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pend == nil {
		a.pend, a.busy = map[string]*pendingWrite{}, map[string]bool{}
	}
	a.pend[path] = &pendingWrite{b, mode, backup}
	if !a.busy[path] {
		a.busy[path] = true
		go a.run(path)
	}
}

func (a *asyncFiles) run(path string) {
	for {
		a.mu.Lock()
		p := a.pend[path]
		delete(a.pend, path)
		if p == nil {
			delete(a.busy, path)
			a.mu.Unlock()
			return
		}
		a.mu.Unlock()
		writePending(path, p)
	}
}

func writePending(path string, p *pendingWrite) {
	var err error
	if p.backup {
		err = writeFileAtomicBackup(path, p.b, p.mode)
	} else {
		err = writeFileAtomic(path, p.b, p.mode)
	}
	if err != nil {
		log.Printf("saving %s failed (the last saved copy is kept): %v", filepath.Base(path), err)
	}
}

// flush returns once every write handed over so far is on disk (or failed).
func (a *asyncFiles) flush() {
	for {
		a.mu.Lock()
		n := len(a.busy)
		a.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}
