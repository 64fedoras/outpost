//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// trayLock keeps one tray per node data folder.
func trayLock(dataDir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dataDir, "tray.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("the Outpost tray icon is already running")
	}
	return func() { f.Close() }, nil
}

func trayDetachConsole(string) {}

// startNodeProcess starts the systemd service (asking for the admin password
// through the desktop's polkit prompt) or, without a service, runs the node
// in the background as this user.
func startNodeProcess(dataDir string) error {
	if _, err := os.Stat(unitPath("node")); err == nil {
		cmd := exec.Command("pkexec", "systemctl", "start", "outpost-node")
		if _, err := exec.LookPath("pkexec"); err != nil {
			return fmt.Errorf("run: sudo systemctl start outpost-node")
		}
		go func() { _ = cmd.Run() }()
		return nil
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
	cmd := exec.Command("xdg-open", u)
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}
