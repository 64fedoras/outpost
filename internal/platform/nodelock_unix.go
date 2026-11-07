//go:build !windows

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// AcquireNodeLock takes an exclusive lock on <data>/node.lock. The file is
// opened close-on-exec, so an in-place restart (exec) drops it and the new
// image takes it again.
func AcquireNodeLock(dataDir string, wait time.Duration) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dataDir, "node.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return func() { f.Close() }, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("another Outpost node is already running with %s (stop it first, e.g. sudo systemctl stop outpost-node)", dataDir)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
