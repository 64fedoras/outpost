package main

// `outpost install controller|node` sets Outpost up as a systemd service that
// starts at boot. `outpost uninstall controller|node` removes the service and
// keeps the data directory.

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const installedBinary = "/usr/local/bin/outpost"

func unitPath(role string) string { return "/etc/systemd/system/outpost-" + role + ".service" }

func checkInstallable() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("install currently supports Linux with systemd; on %s run `outpost node` directly for now", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		return errors.New("install needs root: run it with sudo")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("systemctl not found; install needs systemd")
	}
	return nil
}

func runInstall(args []string) error {
	if len(args) == 0 || (args[0] != "controller" && args[0] != "node") {
		return errors.New("usage: outpost install controller|node [flags]")
	}
	if runtime.GOOS == "windows" && args[0] == "node" {
		return installNodeWindows(args[1:])
	}
	if runtime.GOOS == "darwin" && args[0] == "node" {
		return installNodeDarwin(args[1:])
	}
	if err := checkInstallable(); err != nil {
		return err
	}
	if args[0] == "controller" {
		return installController(args[1:])
	}
	return installNode(args[1:])
}

func runUninstall(args []string) error {
	if len(args) == 0 || (args[0] != "controller" && args[0] != "node") {
		return errors.New("usage: outpost uninstall controller|node")
	}
	if runtime.GOOS == "windows" && args[0] == "node" {
		return uninstallNodeWindows()
	}
	if runtime.GOOS == "darwin" && args[0] == "node" {
		return uninstallNodeDarwin()
	}
	if err := checkInstallable(); err != nil {
		return err
	}
	role := args[0]
	_ = exec.Command("systemctl", "disable", "--now", "outpost-"+role).Run()
	if err := os.Remove(unitPath(role)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_ = exec.Command("systemctl", "daemon-reload").Run()
	fmt.Printf("Removed the outpost-%s service. Its data directory was kept.\n", role)
	return nil
}

// copySelfToBin installs this executable at /usr/local/bin/outpost.
func copySelfToBin() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if s, _ := filepath.EvalSymlinks(self); s == installedBinary {
		return nil
	}
	tmp := installedBinary + ".new"
	if err := copyFile(self, tmp, 0o755); err != nil {
		return fmt.Errorf("copy to %s: %w", installedBinary, err)
	}
	if err := os.Rename(tmp, installedBinary); err != nil {
		return err
	}
	fmt.Printf("Installed %s (Outpost %s)\n", installedBinary, Version)
	return nil
}

func systemdQuote(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\"'\\") {
		return strconv.Quote(s)
	}
	return s
}

func writeUnit(role, description, userName, home string, execArgs []string) error {
	parts := []string{installedBinary}
	for _, a := range execArgs {
		parts = append(parts, systemdQuote(a))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=%s\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\n", description)
	if userName != "" && userName != "root" {
		fmt.Fprintf(&b, "User=%s\nEnvironment=HOME=%s\n", userName, home)
	}
	// Exit code 75 is a deliberate quit from the tray icon: stay stopped.
	fmt.Fprintf(&b, "ExecStart=%s\nRestart=always\nRestartSec=3\nRestartPreventExitStatus=%d\n\n[Install]\nWantedBy=multi-user.target\n", strings.Join(parts, " "), exitQuit)
	if err := os.WriteFile(unitPath(role), []byte(b.String()), 0o644); err != nil {
		return err
	}
	for _, c := range [][]string{{"daemon-reload"}, {"enable", "outpost-" + role}, {"restart", "outpost-" + role}} {
		if out, err := exec.Command("systemctl", c...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", strings.Join(c, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func installController(args []string) error {
	fsf := flag.NewFlagSet("install controller", flag.ExitOnError)
	listen := fsf.String("listen", ":8443", "address to listen on")
	data := fsf.String("data", "/var/lib/outpost/controller", "data directory")
	advertise := fsf.String("advertise", "", "URL nodes use to reach the controller, e.g. https://192.0.2.10:8443")
	restore := fsf.String("restore", "", "restore this backup file (.opb) into the data directory first")
	force := fsf.Bool("force", false, "with --restore: overwrite an existing controller's data")
	_ = fsf.Parse(args)

	if err := copySelfToBin(); err != nil {
		return err
	}
	if err := os.MkdirAll(*data, 0o700); err != nil {
		return err
	}
	if *restore != "" {
		if err := runRestore(*restore, *data, *force); err != nil {
			return err
		}
	}
	if _, _, err := loadOrCreateCert(*data); err != nil {
		return err
	}
	c := &Controller{dataDir: *data}
	pw, err := c.loadOrCreateAdmin(false)
	if err != nil {
		return err
	}
	execArgs := []string{"controller", "--data", *data, "--listen", *listen}
	if *advertise != "" {
		execArgs = append(execArgs, "--advertise", *advertise)
	}
	if err := writeUnit("controller", "Outpost controller", "root", "", execArgs); err != nil {
		return err
	}
	fmt.Println("The outpost-controller service is running and starts at boot.")
	url := *advertise
	if url == "" {
		url = "https://<this-machine's-IP>" + *listen
	}
	fmt.Println("Web UI:", url)
	if pw != "" {
		fmt.Println("ADMIN PASSWORD:", pw, " (shown once; `sudo outpost controller --reset-password` makes a new one)")
	} else {
		fmt.Println("Admin password: unchanged from before.")
	}
	fmt.Println("Logs: journalctl -u outpost-controller -f")
	return nil
}

func installNode(args []string) error {
	fsf := flag.NewFlagSet("install node", flag.ExitOnError)
	join := fsf.String("join", "", "join token from the controller UI (not needed if this machine already joined)")
	userName := fsf.String("user", "", "user the node runs as (default: the user who ran sudo)")
	data := fsf.String("data", "", "data directory (default: an existing node config, else ~USER/.outpost/node)")
	name := fsf.String("name", "", "display name to register with (default: hostname)")
	_ = fsf.Parse(args)

	if *userName == "" {
		*userName = os.Getenv("SUDO_USER")
	}
	if *userName == "" {
		*userName = "root"
	}
	u, err := user.Lookup(*userName)
	if err != nil {
		return fmt.Errorf("user %q: %w", *userName, err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)

	dir := *data
	if dir == "" {
		candidates := []string{filepath.Join(u.HomeDir, ".outpost", "node"), "/var/lib/outpost/node"}
		dir = candidates[0]
		for _, c := range candidates {
			if _, err := os.Stat(filepath.Join(c, "node.json")); err == nil {
				dir = c
				break
			}
		}
	}
	for _, d := range []string{dir, filepath.Join(dir, "work"), filepath.Join(dir, "models"), filepath.Join(dir, "agent")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	cfgPath := filepath.Join(dir, "node.json")
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
		return fmt.Errorf("no node config in %s; add --join TOKEN (from Add node in the controller UI)", dir)
	} else {
		fmt.Printf("Using the existing node config in %s\n", dir)
	}
	// The service user owns its data so it can install updates without root.
	_ = filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err == nil {
			_ = os.Lchown(p, uid, gid)
		}
		return nil
	})
	// Directories created between the user's home and the data dir (e.g. ~/.outpost)
	// must belong to the user too, or the service cannot reach its data.
	if rel, err := filepath.Rel(u.HomeDir, dir); err == nil && !strings.HasPrefix(rel, "..") {
		for p := filepath.Dir(dir); p != u.HomeDir && len(p) > len(u.HomeDir); p = filepath.Dir(p) {
			_ = os.Lchown(p, uid, gid)
		}
	}
	if err := copySelfToBin(); err != nil {
		return err
	}
	if err := writeUnit("node", "Outpost node", u.Username, u.HomeDir, []string{"node", "--data", dir}); err != nil {
		return err
	}
	fmt.Printf("The outpost-node service is running as %s and starts at boot.\n", u.Username)
	if hasDesktop() {
		if err := installTrayAutostart(u, uid, gid, dir); err != nil {
			fmt.Println("Tray icon not set up:", err)
		} else {
			fmt.Println("Tray icon: starts when", u.Username, "logs in to the desktop (or now: outpost tray &, from a desktop terminal).")
		}
	}
	fmt.Println("If you also started `outpost node` by hand, stop that copy (Ctrl+C) so only the service runs.")
	fmt.Println("Logs: journalctl -u outpost-node -f")
	return nil
}

// runResetPassword sets a new admin password and restarts the service so it
// takes effect. Existing browser sessions are signed out.
func runResetPassword(args []string) error {
	fsf := flag.NewFlagSet("reset-password", flag.ExitOnError)
	data := fsf.String("data", defaultDataDir("controller"), "controller data directory")
	generate := fsf.Bool("generate", false, "generate a random password instead of asking")
	_ = fsf.Parse(args)
	if _, err := os.Stat(filepath.Join(*data, "admin.json")); err != nil {
		return fmt.Errorf("no controller data in %s (use --data DIR, or run with sudo for /var/lib/outpost/controller)", *data)
	}
	c := &Controller{dataDir: *data}
	pw := ""
	if !*generate {
		pw = readPassword("New admin password (leave blank to generate one): ")
		if pw != "" {
			if err := validNewPassword(pw); err != nil {
				return err
			}
			if again := readPassword("Repeat it: "); again != pw {
				return errors.New("the passwords do not match; nothing changed")
			}
		}
	}
	if pw == "" {
		pw = randHex(8)
		defer fmt.Println("NEW ADMIN PASSWORD:", pw)
	}
	if err := c.setAdminPassword(pw); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(*data, "sessions.json"))
	fmt.Println("Admin password changed.")
	if runtime.GOOS == "linux" && exec.Command("systemctl", "is-active", "--quiet", "outpost-controller").Run() == nil {
		if out, err := exec.Command("systemctl", "restart", "outpost-controller").CombinedOutput(); err != nil {
			return fmt.Errorf("password saved, but restarting the service failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
		fmt.Println("Restarted outpost-controller; sign in with the new password.")
	} else {
		fmt.Println("If a controller is running by hand, restart it so the new password takes effect.")
	}
	return nil
}

// hasDesktop reports whether this Linux machine has a graphical session type
// installed, so a tray icon makes sense.
func hasDesktop() bool {
	for _, d := range []string{"/usr/share/xsessions", "/usr/share/wayland-sessions"} {
		if es, err := os.ReadDir(d); err == nil && len(es) > 0 {
			return true
		}
	}
	return false
}

// installTrayAutostart adds the tray icon to the user's desktop login and
// app menu (freedesktop autostart), with Outpost's icon.
func installTrayAutostart(u *user.User, uid, gid int, dataDir string) error {
	icon := filepath.Join(u.HomeDir, ".local/share/icons/hicolor/64x64/apps/outpost.png")
	entry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Outpost node
Comment=Status icon for this Outpost node: pause work, join another controller, quit
Exec=%s tray --data %s
Icon=%s
Terminal=false
Categories=Utility;System;
X-GNOME-Autostart-enabled=true
`, installedBinary, systemdQuote(dataDir), icon)
	files := map[string][]byte{
		filepath.Join(u.HomeDir, ".config/autostart/outpost-tray.desktop"):         []byte(entry),
		filepath.Join(u.HomeDir, ".local/share/applications/outpost-tray.desktop"): []byte(entry),
		icon: trayPNG(trayOnline),
	}
	for p, b := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
		// Every folder created under the home directory belongs to the user.
		for d := p; d != u.HomeDir && len(d) > len(u.HomeDir); d = filepath.Dir(d) {
			_ = os.Lchown(d, uid, gid)
		}
	}
	return nil
}
