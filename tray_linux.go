//go:build linux

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
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

// trayDetachConsole sends the tray's log (including the systray library's
// D-Bus errors) to tray.log, since an autostarted tray has no terminal, and
// warns there when no desktop is listening for tray icons.
func trayDetachConsole(dataDir string) {
	if f, err := os.OpenFile(filepath.Join(dataDir, "tray.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		log.SetOutput(f)
	}
	log.Printf("tray: Outpost %s starting (pid %d)", Version, os.Getpid())
	go func() {
		time.Sleep(10 * time.Second)
		if !trayWatcherPresent() {
			log.Printf("tray: nothing on the desktop is showing tray icons (no StatusNotifierWatcher). On GNOME, install and enable the AppIndicator extension (Ubuntu: gnome-shell-extension-appindicator), then log out and in.")
		}
	}()
}

func trayWatcherPresent() bool {
	conn, err := dbus.SessionBus()
	if err != nil {
		log.Printf("tray: no desktop session bus: %v", err)
		return false
	}
	var has bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has)
	return err == nil && has
}

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
