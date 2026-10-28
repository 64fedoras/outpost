//go:build windows

package main

import (
	"flag"
	"fmt"
	"net/http"
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
	noService := fsf.Bool("no-service", false, "run as a per-user background app instead of a Windows service")
	noDefender := fsf.Bool("no-defender-exclusion", false, "do not add the Microsoft Defender exclusion")
	_ = fsf.Parse(args)
	asService := !*noService && isElevated()
	if asService {
		// An update from a previous install may be running; free the exe.
		stopInstalledService()
	}

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

	if asService {
		if !*noDefender {
			addDefenderExclusion(filepath.Dir(*data))
		}
		quitUserNode(*data) // a node from an earlier per-user install holds the node lock
		if err := installService(exe, *data); err != nil {
			return err
		}
		if err := setRunKey(fmt.Sprintf(`"%s" tray --data "%s" --service`, exe, *data)); err != nil {
			return err
		}
		startTrayDetached(exe, *data, "--service")
		fmt.Println("The Outpost Node service is installed. It starts at boot, before anyone signs in, and restarts if it stops.")
		fmt.Println("Look for the tent icon in the notification area after you sign in (it may be under the ^ arrow).")
		fmt.Println("Stop or remove it: `outpost.exe uninstall node` (admin), or `sc stop OutpostNode`.")
		fmt.Printf("Logs: %s\n", nodeLogPath(*data))
		return nil
	}

	if err := setRunKey(fmt.Sprintf(`"%s" tray --data "%s"`, exe, *data)); err != nil {
		return err
	}
	startTrayDetached(exe, *data)
	fmt.Println("Outpost runs in the background and starts when you sign in to Windows.")
	fmt.Println("To run it as a Windows service (starts at boot, no sign-in needed), open an Administrator PowerShell and run this install command again.")
	fmt.Println("Look for the tent icon in the notification area (it may be under the ^ arrow).")
	fmt.Println("If you started `outpost.exe node` in another window, close that window.")
	fmt.Printf("Logs: %s\n", nodeLogPath(*data))
	fmt.Printf("If Microsoft Defender blocks updates, in an admin PowerShell run:\n  Add-MpPreference -ExclusionPath '%s'\n", filepath.Dir(*data))
	return nil
}

func setRunKey(cmdline string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("adding Outpost to sign-in: %w", err)
	}
	defer k.Close()
	if err := k.SetStringValue("Outpost", cmdline); err != nil {
		return fmt.Errorf("adding Outpost to sign-in: %w", err)
	}
	return nil
}

func removeRunKey() {
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE); err == nil {
		_ = k.DeleteValue("Outpost")
		k.Close()
	}
}

// startTrayDetached starts the tray icon now, detached from this window.
func startTrayDetached(exe, data string, extra ...string) {
	cmd := exec.Command(exe, append([]string{"tray", "--data", data}, extra...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow | syscall.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		fmt.Println("Could not start the tray icon:", err)
		return
	}
	_ = cmd.Process.Release()
	time.Sleep(time.Second)
}

// quitUserNode asks a node running from an earlier per-user install to quit.
func quitUserNode(data string) {
	t := &trayApp{dataDir: data, client: &http.Client{Timeout: 4 * time.Second}}
	if _, err := t.call("POST", "/quit"); err == nil {
		time.Sleep(3 * time.Second)
	}
}
