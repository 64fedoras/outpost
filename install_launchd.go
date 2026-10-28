package main

// macOS: `outpost install node` registers a per-user LaunchAgent so the node
// starts at login. It has to be per-user, not a system daemon, so llama.cpp
// can use the GPU through Metal. The plist generation is plain Go and is tested
// on every platform; only launchctl is macOS-only.

import (
	"errors"
	"flag"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const launchdLabel = "io.outpost.node"

func launchAgentPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

// launchdPlist returns the LaunchAgent for a node. launchd cannot exclude an
// exit status from KeepAlive, so a small shell wrapper turns the deliberate
// quit code (75) into a clean exit; KeepAlive then restarts on every other exit.
func launchdPlist(bin, dataDir, logPath string) string {
	script := fmt.Sprintf(`"$0" "$@"; rc=$?; if [ $rc -eq %d ]; then exit 0; fi; exit $rc`, exitQuit)
	args := []string{"/bin/sh", "-c", script, bin, "node", "--data", dataDir}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range args {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", html.EscapeString(a))
	}
	fmt.Fprintf(&b, `	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>3</integer>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, html.EscapeString(logPath), html.EscapeString(logPath))
	return b.String()
}

func checkInstallableDarwin() error {
	if runtime.GOOS != "darwin" {
		return errors.New("the LaunchAgent is for macOS")
	}
	if os.Geteuid() == 0 {
		return errors.New("on macOS run install without sudo: the node is a per-user LaunchAgent so llama.cpp can use the GPU")
	}
	return nil
}

func installNodeDarwin(args []string) error {
	if err := checkInstallableDarwin(); err != nil {
		return err
	}
	fsf := flag.NewFlagSet("install node", flag.ExitOnError)
	join := fsf.String("join", "", "join token from the controller UI (not needed if this machine already joined)")
	data := fsf.String("data", defaultDataDir("node"), "data directory")
	name := fsf.String("name", "", "display name to register with (default: hostname)")
	_ = fsf.Parse(args)

	home, err := os.UserHomeDir()
	if err != nil {
		return err
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
	bin := filepath.Join(home, ".outpost", "outpost")
	if s, _ := filepath.EvalSymlinks(self); s != bin {
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			return err
		}
		if err := copyFile(self, bin+".new", 0o755); err != nil {
			return fmt.Errorf("copy to %s: %w", bin, err)
		}
		if err := os.Rename(bin+".new", bin); err != nil {
			return err
		}
		fmt.Printf("Installed %s (Outpost %s)\n", bin, Version)
	}

	logPath := filepath.Join(*data, "node.log")
	plist := launchAgentPath(home)
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return err
	}
	// Unload a previous copy first so the new plist and binary take effect.
	uid := fmt.Sprint(os.Getuid())
	_ = exec.Command("launchctl", "bootout", "gui/"+uid+"/"+launchdLabel).Run()
	if err := os.WriteFile(plist, []byte(launchdPlist(bin, *data, logPath)), 0o644); err != nil {
		return err
	}
	if out, err := exec.Command("launchctl", "bootstrap", "gui/"+uid, plist).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s (no desktop login? sign in on this Mac and run it again)", err, strings.TrimSpace(string(out)))
	}
	fmt.Println("The Outpost node is a LaunchAgent now: it starts when you log in to this Mac and restarts if it stops.")
	fmt.Println("Logs:", logPath)
	return nil
}

func uninstallNodeDarwin() error {
	if err := checkInstallableDarwin(); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	_ = exec.Command("launchctl", "bootout", "gui/"+fmt.Sprint(os.Getuid())+"/"+launchdLabel).Run()
	if err := os.Remove(launchAgentPath(home)); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Println("Removed the Outpost LaunchAgent. The data directory and ~/.outpost/outpost were kept.")
	return nil
}
