//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// trayLock keeps one menu bar item per node data folder.
func trayLock(dataDir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dataDir, "tray.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("the Outpost menu bar item is already running")
	}
	return func() { f.Close() }, nil
}

func trayDetachConsole(string) {}

// startNodeProcess asks launchd to start the node's LaunchAgent; without one
// (the node was started by hand) it runs the node in the background.
func startNodeProcess(dataDir string) error {
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), launchdLabel)
	if exec.Command("launchctl", "print", target).Run() == nil {
		// bootstrap may be needed after a deliberate quit stopped the job
		if exec.Command("launchctl", "kickstart", target).Run() == nil {
			return nil
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if _, err := os.Stat(launchAgentPath(home)); err == nil {
			if exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), launchAgentPath(home)).Run() == nil {
				return nil
			}
		}
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	logf, err := openNodeLog(dataDir)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(self, "node", "--data", dataDir)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func openURL(u string) {
	cmd := exec.Command("open", u)
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}
