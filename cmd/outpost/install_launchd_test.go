package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchdDaemonPlist(t *testing.T) {
	p := launchdDaemonPlist("/usr/local/bin/out&post", "/Library/Application Support/outpost/node", "/tmp/node.log", "501")
	for _, want := range []string{
		"<string>io.outpost.node</string>",
		"<array>\n\t\t<string>/usr/local/bin/out&amp;post</string>\n\t\t<string>node</string>",
		"<string>/Library/Application Support/outpost/node</string>",
		"<key>RunAtLoad</key>",
		"<key>SuccessfulExit</key>",
		"<key>OUTPOST_LAUNCHD</key>\n\t\t<string>1</string>",
		"<key>OUTPOST_TRAY_UID</key>\n\t\t<string>501</string>",
		"<string>/tmp/node.log</string>",
		"<string>/tmp/node-console.log</string>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "out&post") {
		t.Error("unescaped & in plist")
	}
	// launchd must start the Outpost binary itself, never through a shell, and
	// a daemon has no user session to limit itself to.
	for _, bad := range []string{"/bin/sh", "UserName", "LimitLoadToSessionType"} {
		if strings.Contains(p, bad) {
			t.Errorf("daemon plist contains %q", bad)
		}
	}
	if strings.Contains(launchdDaemonPlist("/b", "/d", "/l", ""), "OUTPOST_TRAY_UID") {
		t.Error("no tray user, but OUTPOST_TRAY_UID is set")
	}
}

func TestAdoptAgentConfig(t *testing.T) {
	old, dir := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(old, "models"), 0o700)
	os.WriteFile(filepath.Join(old, "models", "m.gguf"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(old, "node.json"), []byte(`{"node_id":"nd_1"}`), 0o600)
	os.MkdirAll(filepath.Join(dir, "models"), 0o700)
	if !adoptAgentConfig(old, dir) {
		t.Fatal("did not adopt")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "node.json")); string(b) != `{"node_id":"nd_1"}` {
		t.Fatalf("node.json = %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "models", "m.gguf")); err != nil {
		t.Fatal("models not moved:", err)
	}
	if adoptAgentConfig(old, dir) {
		t.Fatal("adopted over an existing config")
	}
}

func TestLaunchAgentPath(t *testing.T) {
	if got := launchAgentPath("/Users/x"); got != "/Users/x/Library/LaunchAgents/io.outpost.node.plist" {
		t.Fatal(got)
	}
}

func TestLaunchdTrayPlist(t *testing.T) {
	p := launchdTrayPlist("/Users/a&b/.outpost/outpost", "/Users/a&b/.outpost/node", "/tmp/tray.log")
	for _, want := range []string{"<string>io.outpost.tray</string>", "<string>tray</string>", "<string>/Users/a&amp;b/.outpost/outpost</string>", "<key>RunAtLoad</key>", "<string>Aqua</string>", "<string>/tmp/tray.log</string>"} {
		if !strings.Contains(p, want) {
			t.Errorf("tray plist missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "KeepAlive") {
		t.Error("the tray must not be kept alive")
	}
	if got := trayAgentPath("/Users/x"); got != "/Users/x/Library/LaunchAgents/io.outpost.tray.plist" {
		t.Fatal(got)
	}
}
