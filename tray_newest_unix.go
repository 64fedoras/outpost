//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
)

// traySwitchToNewest makes the tray run the same Outpost version as the node.
// The login entry starts the binary that `install` copied (/usr/local/bin on
// Linux), but web-UI updates go to <data>/agent, so without this the tray kept
// running the version from the first install.
func traySwitchToNewest(dataDir string, args []string) {
	c, ok := selectTarget(filepath.Join(dataDir, "agent"))
	if !ok {
		return
	}
	if self, _ := os.Executable(); c.path == self {
		return
	}
	execTo(c.path, append([]string{"tray"}, args...))
}

// startNodeService is only used by the Windows tray (`tray --service`).
func startNodeService(dataDir string) error { return startNodeProcess(dataDir) }
