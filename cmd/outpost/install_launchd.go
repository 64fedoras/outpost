package main

// macOS: `sudo outpost install node` registers a LaunchDaemon, a system
// service that runs as root and starts at boot. It used to be a per-user
// LaunchAgent, but macOS 15 and later block LaunchAgents from the local
// network (Local Network privacy) and, for a command-line program, never show
// the prompt that would allow it; launchd daemons are exempt. The menu bar
// item is still a per-user LaunchAgent: it only talks to the node on
// 127.0.0.1. The plist generation is plain Go and is tested on every
// platform; only launchctl is macOS-only.

import (
	"errors"
	"flag"
	"fmt"
	"html"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const launchdLabel = "io.outpost.node"
const launchdTrayLabel = "io.outpost.tray"

// launchDaemonPath is where the node's system service is defined.
const launchDaemonPath = "/Library/LaunchDaemons/" + launchdLabel + ".plist"

// trayLogDir holds the menu bar item's log; the node's data folder belongs to root.
func trayLogDir(home string) string { return filepath.Join(home, "Library", "Logs", "Outpost") }

func trayAgentPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", launchdTrayLabel+".plist")
}

// launchAgentPath is the per-user node LaunchAgent of Outpost 0.14.13 and
// earlier. Install and uninstall remove it.
func launchAgentPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

func plistArgs(b *strings.Builder, args []string) {
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range args {
		fmt.Fprintf(b, "\t\t<string>%s</string>\n", html.EscapeString(a))
	}
	b.WriteString("\t</array>\n")
}

func plistEnv(b *strings.Builder, env [][2]string) {
	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	for _, kv := range env {
		fmt.Fprintf(b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", kv[0], html.EscapeString(kv[1]))
	}
	b.WriteString("\t</dict>\n")
}

const plistHead = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`

// launchdTrayPlist starts the menu bar item at login. It is not kept alive:
// "Close this icon only" in its menu should stay closed until the next login.
func launchdTrayPlist(bin, dataDir, logPath string) string {
	var b strings.Builder
	b.WriteString(plistHead)
	b.WriteString("\t<key>Label</key>\n\t<string>" + launchdTrayLabel + "</string>\n")
	plistArgs(&b, []string{bin, "tray", "--data", dataDir})
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n\t<key>LimitLoadToSessionType</key>\n\t<string>Aqua</string>\n\t<key>ProcessType</key>\n\t<string>Interactive</string>\n")
	plistEnv(&b, [][2]string{{"OUTPOST_LOG_FILE", logPath}})
	fmt.Fprintf(&b, "\t<key>StandardOutPath</key>\n\t<string>%[1]s</string>\n\t<key>StandardErrorPath</key>\n\t<string>%[1]s</string>\n</dict>\n</plist>\n",
		html.EscapeString(consoleLogPath(logPath)))
	return b.String()
}

// launchdDaemonPlist returns the node's LaunchDaemon. launchd runs the Outpost
// binary itself, not a shell wrapper. launchd cannot exclude an exit status
// from KeepAlive, so with OUTPOST_LAUNCHD=1 a deliberate quit exits 0 instead
// of 75 (node_control.go) and KeepAlive restarts on every other exit.
// OUTPOST_TRAY_UID names the signed-in user whose menu bar item may read the
// node's control file (empty: no menu bar item).
func launchdDaemonPlist(bin, dataDir, logPath, trayUID string) string {
	var b strings.Builder
	b.WriteString(plistHead)
	b.WriteString("\t<key>Label</key>\n\t<string>" + launchdLabel + "</string>\n")
	plistArgs(&b, []string{bin, "node", "--data", dataDir})
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n\t<key>ThrottleInterval</key>\n\t<integer>3</integer>\n\t<key>ProcessType</key>\n\t<string>Interactive</string>\n")
	env := [][2]string{{"OUTPOST_LOG_FILE", logPath}, {"OUTPOST_LAUNCHD", "1"}, {"HOME", "/var/root"}}
	if trayUID != "" {
		env = append(env, [2]string{"OUTPOST_TRAY_UID", trayUID})
	}
	plistEnv(&b, env)
	fmt.Fprintf(&b, "\t<key>StandardOutPath</key>\n\t<string>%[1]s</string>\n\t<key>StandardErrorPath</key>\n\t<string>%[1]s</string>\n</dict>\n</plist>\n",
		html.EscapeString(consoleLogPath(logPath)))
	return b.String()
}

func checkInstallableDarwin() error {
	if runtime.GOOS != "darwin" {
		return errors.New("the LaunchDaemon is for macOS")
	}
	if os.Geteuid() != 0 {
		return errors.New("run it with sudo: the node is a system service, because macOS blocks a login item from the local network")
	}
	return nil
}

// trayUser is the signed-in user who ran sudo; nil when there is none.
func trayUser() *user.User {
	name := os.Getenv("SUDO_USER")
	if name == "" || name == "root" {
		return nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		return nil
	}
	return u
}

// adoptAgentConfig moves the node identity (and stored models) of a 0.14.13 or
// older LaunchAgent install into the daemon's data folder, so re-running
// install without --join keeps the same node in the controller.
func adoptAgentConfig(oldDir, dir string) bool {
	cfg := filepath.Join(dir, "node.json")
	if _, err := os.Stat(cfg); err == nil {
		return false
	}
	b, err := os.ReadFile(filepath.Join(oldDir, "node.json"))
	if err != nil {
		return false
	}
	if err := writeFileAtomic(cfg, b, 0o600); err != nil {
		return false
	}
	// The daemon's models folder was just created and is empty; take the old one.
	if os.Remove(filepath.Join(dir, "models")) == nil {
		if os.Rename(filepath.Join(oldDir, "models"), filepath.Join(dir, "models")) != nil {
			_ = os.MkdirAll(filepath.Join(dir, "models"), 0o700)
		}
	}
	return true
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

	tu := trayUser()
	uid, gid := -1, -1
	if tu != nil {
		uid, _ = strconv.Atoi(tu.Uid)
		gid, _ = strconv.Atoi(tu.Gid)
	}
	for _, d := range []string{*data, filepath.Join(*data, "work"), filepath.Join(*data, "models"), filepath.Join(*data, "agent")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	// Stop a LaunchAgent node from an older install before its config and
	// models are moved (adoptAgentConfig): it may be using them. Its plist is
	// removed further down, once the new service's files are in place, so a
	// failed install leaves the old node to start again at the next login.
	if tu != nil {
		_ = exec.Command("launchctl", "bootout", "gui/"+tu.Uid+"/"+launchdLabel).Run()
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
	} else if tu != nil && adoptAgentConfig(filepath.Join(tu.HomeDir, ".outpost", "node"), *data) {
		fmt.Printf("Moved the node config and models from %s to %s\n", filepath.Join(tu.HomeDir, ".outpost", "node"), *data)
	} else if _, err := os.Stat(cfgPath); err != nil {
		return fmt.Errorf("no node config in %s; add --join TOKEN (from Add node in the controller UI)", *data)
	} else {
		fmt.Printf("Using the existing node config in %s\n", *data)
	}

	if err := os.MkdirAll(filepath.Dir(installedBinary), 0o755); err != nil {
		return err
	}
	if err := copySelfToBin(); err != nil {
		return err
	}

	// The menu bar item runs as the signed-in user. It may pass through the
	// data folder (not list it) and read the control file the node hands to it,
	// and it keeps its lock there; its log goes to ~/Library/Logs/Outpost. Installed copies in agent/ are
	// readable so it can switch to the newest version, as the node does.
	if tu != nil {
		_ = os.Chmod(*data, 0o711)
		if parent := filepath.Dir(*data); filepath.Base(parent) == "outpost" {
			_ = os.Chmod(parent, 0o711) // /Library/Application Support/outpost
		}
		_ = os.Chmod(filepath.Join(*data, "agent"), 0o755)
		lock := filepath.Join(*data, "tray.lock")
		if fh, err := os.OpenFile(lock, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fh.Close()
			_ = os.Chown(lock, uid, gid)
		}
	}

	// Retire the LaunchAgent of an older install (stopped above); two nodes
	// would fight over the same controller identity.
	if tu != nil {
		if err := os.Remove(launchAgentPath(tu.HomeDir)); err == nil {
			fmt.Println("Removed the old per-user LaunchAgent for the node.")
		}
	}

	logPath := filepath.Join(*data, "node.log")
	trayUID := ""
	if tu != nil {
		trayUID = tu.Uid
	}
	_ = exec.Command("launchctl", "bootout", "system/"+launchdLabel).Run()
	if err := os.WriteFile(launchDaemonPath, []byte(launchdDaemonPlist(installedBinary, *data, logPath, trayUID)), 0o644); err != nil {
		return err
	}
	_ = os.Chown(launchDaemonPath, 0, 0)
	if out, err := exec.Command("launchctl", "bootstrap", "system", launchDaemonPath).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out)))
	}
	fmt.Println("The Outpost node is a system service now: it starts at boot, without anyone signed in, and restarts if it stops.")
	fmt.Println("Logs:", logPath)

	if tu != nil {
		trayPlist := trayAgentPath(tu.HomeDir)
		_ = exec.Command("launchctl", "bootout", "gui/"+tu.Uid+"/"+launchdTrayLabel).Run()
		_ = os.MkdirAll(filepath.Dir(trayPlist), 0o755)
		logDir := trayLogDir(tu.HomeDir)
		if os.MkdirAll(logDir, 0o755) == nil {
			_ = os.Chown(logDir, uid, gid)
		}
		if err := os.WriteFile(trayPlist, []byte(launchdTrayPlist(installedBinary, *data, filepath.Join(logDir, "tray.log"))), 0o644); err != nil {
			fmt.Println("Could not write the menu bar item's LaunchAgent:", err)
		} else {
			_ = os.Chown(trayPlist, uid, gid)
			if out, err := exec.Command("launchctl", "bootstrap", "gui/"+tu.Uid, trayPlist).CombinedOutput(); err != nil {
				fmt.Printf("The menu bar item starts at your next login (%v: %s).\n", err, strings.TrimSpace(string(out)))
			} else {
				fmt.Println("A menu bar item shows the node's status (pause work, open the web UI, quit); it starts at login too.")
			}
		}
	}

	fmt.Println("Waiting for the node to connect to the controller...")
	switch st := waitNodeOnline(*data, 45*time.Second); {
	case st != nil && st.Online:
		fmt.Println("Connected. The node shows as online in the controller's web UI.")
	case st != nil && st.Problem == problemLocalNetwork:
		fmt.Println(lnpHelp)
	default:
		fmt.Println("Not connected yet; see", logPath)
	}
	return nil
}

func uninstallNodeDarwin() error {
	if err := checkInstallableDarwin(); err != nil {
		return err
	}
	if tu := trayUser(); tu != nil {
		_ = exec.Command("launchctl", "bootout", "gui/"+tu.Uid+"/"+launchdTrayLabel).Run()
		if err := os.Remove(trayAgentPath(tu.HomeDir)); err != nil && !os.IsNotExist(err) {
			return err
		}
		_ = exec.Command("launchctl", "bootout", "gui/"+tu.Uid+"/"+launchdLabel).Run()
		_ = os.Remove(launchAgentPath(tu.HomeDir))
	}
	_ = exec.Command("launchctl", "bootout", "system/"+launchdLabel).Run()
	if err := os.Remove(launchDaemonPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Println("Removed the Outpost node service. The data directory and " + installedBinary + " were kept.")
	return nil
}
