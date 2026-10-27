//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

// acquireNodeLock takes an exclusive lock on <data>/node.lock. Windows drops
// the lock when the process exits, however it exits.
func acquireNodeLock(dataDir string, wait time.Duration) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dataDir, "node.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		ol := new(windows.Overlapped)
		err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
		if err == nil {
			return func() { f.Close() }, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("another Outpost node is already running with %s; quit it from its tray icon first", dataDir)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
