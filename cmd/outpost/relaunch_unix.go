//go:build !windows

package main

import (
	"log"
	"os"
	"syscall"
)

// launchNewest replaces this process with the target installed copy (see
// selectTarget), if it is not this binary. systemd keeps the same PID, so the
// service stays "active" across the switch. When the process stays, it keeps a
// copy of itself so a later update can be rolled back to it.
func launchNewest(agentDir string, args []string) {
	c, ok := selectTarget(agentDir)
	if ok {
		self, _ := os.Executable()
		if c.path != self {
			log.Printf("switching to installed copy %s (running %s)", c.version, Version)
			execTo(c.path, args)
		}
	}
	ensureSelfCopy(agentDir)
	adoptInstalledBinary(agentDir)
}

func execTo(path string, args []string) {
	argv := append([]string{path}, stripJoin(args)...)
	if err := syscall.Exec(path, argv, os.Environ()); err != nil {
		log.Printf("could not start %s: %v; continuing with %s", path, err, Version)
	}
}

// relaunch restarts into the target installed copy after an update or rollback.
func relaunch(agentDir string, args []string) {
	if c, ok := selectTarget(agentDir); ok {
		execTo(c.path, args)
	}
}

// restartInto replaces this process with a specific copy (rollback).
func restartInto(path string, args []string) { execTo(path, args) }
