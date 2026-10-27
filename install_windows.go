//go:build windows

package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// installNodeWindows sets Outpost up for the signed-in user: the exe lives in
// C:\ProgramData\outpost (one folder to exclude from antivirus), and the tray
// icon starts at login and runs the node in the background.
func installNodeWindows(args []string) error {
	fsf := flag.NewFlagSet("install node", flag.ExitOnError)
	join := fsf.String("join", "", "join token from the controller UI (not needed if this machine already joined)")
	data := fsf.String("data", defaultDataDir("node"), "data directory")
	name := fsf.String("name", "", "display name to register with (default: computer name)")
	_ = fsf.Parse(args)

	for _, d := range []string{*data, filepath.Join(*data, "work"), filepath.Join(*data, "models"), filepath.Join(*data, "agent")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	cfgPath := filepath.Join(*data, "node.json")
	if *join != "" {
		cfg, err := doJoin(*join, *name)
		if err != nil {
			return err
		}
		if err := writeNodeConfig(cfgPath, cfg); err != nil {
			return err
		}
		fmt.Printf("Joined %s as %s\n", cfg.Controller, cfg.NodeID)
	} else if _, err := os.Stat(cfgPath); err != nil {
		return fmt.Errorf("no node config in %s; add --join TOKEN (from Add node in the controller UI)", *data)
	} else {
		fmt.Printf("Using the existing node config in %s\n", *data)
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}
	exe := filepath.Join(filepath.Dir(*data), "outpost.exe")
	if !strings.EqualFold(filepath.Clean(self), filepath.Clean(exe)) {
		tmp := exe + ".new"
		if err := copyFile(self, tmp, 0o755); err != nil {
			return fmt.Errorf("copy to %s: %w", exe, err)
		}
		_ = os.Remove(exe)
		if err := os.Rename(tmp, exe); err != nil {
			return fmt.Errorf("%s is in use; quit Outpost from its tray icon (or close its window) and run install again", exe)
		}
		fmt.Println("Installed", exe)
	}

	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("adding Outpost to sign-in: %w", err)
	}
	cmdline := fmt.Sprintf(`"%s" tray --data "%s"`, exe, *data)
	err = k.SetStringValue("Outpost", cmdline)
	k.Close()
	if err != nil {
		return fmt.Errorf("adding Outpost to sign-in: %w", err)
	}

	// Start the tray now (it starts the node), detached from this window.
	cmd := exec.Command(exe, "tray", "--data", *data)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow | syscall.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the tray icon: %w", err)
	}
	_ = cmd.Process.Release()
	time.Sleep(time.Second)

	fmt.Println("Outpost runs in the background and starts when you sign in to Windows.")
	fmt.Println("Look for the tent icon in the notification area (it may be under the ^ arrow).")
	fmt.Println("If you started `outpost.exe node` in another window, close that window.")
	fmt.Printf("Logs: %s\n", nodeLogPath(*data))
	fmt.Printf("If Microsoft Defender blocks updates, in an admin PowerShell run:\n  Add-MpPreference -ExclusionPath '%s'\n", filepath.Dir(*data))
	return nil
}
