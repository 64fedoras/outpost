//go:build windows

package main

import (
	"errors"
	"log"
	"os"
	"os/exec"
	"syscall"
)

// Windows cannot replace a running process, so the first process acts as a
// small launcher: it runs the newest copy as a child and starts it again
// whenever the child exits with relaunchExitCode after installing an update.

func isChild() bool { return os.Getenv("OUTPOST_CHILD") == "1" }

func launchNewest(agentDir string, args []string) {
	if isChild() {
		return
	}
	if _, ok := newestCopy(agentDir); !ok {
		ensureSelfCopy(agentDir)
		adoptInstalledBinary(agentDir)
		return
	}
	runChildLoop(agentDir, args)
}

func runChildLoop(agentDir string, args []string) {
	for {
		path, _ := os.Executable()
		if c, ok := newestCopy(agentDir); ok {
			path = c.path
		}
		log.Printf("launcher: starting %s", path)
		cmd := exec.Command(path, stripJoin(args)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Env = append(os.Environ(), "OUTPOST_CHILD=1")
		err := cmd.Run()
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == relaunchExitCode {
			continue
		}
		if errors.As(err, &ee) && ee.ExitCode() == exitQuit {
			os.Exit(exitQuit) // quit from the tray icon
		}
		if err != nil {
			log.Printf("launcher: child exited: %v", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
}

func relaunch(agentDir string, args []string) {
	if isChild() {
		os.Exit(relaunchExitCode)
	}
	// Not running under a launcher (the node started with no newer copy to
	// switch to). Running the child loop here would leave this process's
	// connection, control port and timers alive next to the new version, and
	// the two would keep replacing each other at the controller. Start a fresh
	// process instead (it becomes the launcher for the newest copy) and exit.
	self, err := os.Executable()
	if err != nil {
		log.Printf("relaunch: %v", err)
		return
	}
	cmd := exec.Command(self, stripJoin(args)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		log.Printf("relaunch: starting %s: %v", self, err)
		return
	}
	log.Printf("relaunch: started %s (pid %d); this process exits", self, cmd.Process.Pid)
	os.Exit(0)
}

// restartInto restarts into the pinned copy (rollback); the launcher picks it.
func restartInto(path string, args []string) { relaunch("", args) }
