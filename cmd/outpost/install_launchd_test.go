package main

import (
	"strings"
	"testing"
)

func TestLaunchdPlist(t *testing.T) {
	p := launchdPlist("/Users/a&b/.outpost/outpost", "/Users/a&b/.outpost/node", "/tmp/node.log")
	for _, want := range []string{
		"<string>io.outpost.node</string>",
		"<string>/Users/a&amp;b/.outpost/outpost</string>",
		"<string>--data</string>",
		"<key>RunAtLoad</key>",
		"<key>SuccessfulExit</key>",
		"<key>OUTPOST_LAUNCHD</key>",
		"<string>/tmp/node.log</string>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "a&b") {
		t.Error("unescaped & in plist")
	}
	// launchd must start the Outpost binary itself: macOS Local Network
	// privacy cannot allow a shell script.
	if strings.Contains(p, "/bin/sh") {
		t.Error("the node LaunchAgent must not run through a shell")
	}
	if !strings.Contains(p, "<array>\n\t\t<string>/Users/a&amp;b/.outpost/outpost</string>\n\t\t<string>node</string>") {
		t.Error("the binary must be the first program argument")
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
