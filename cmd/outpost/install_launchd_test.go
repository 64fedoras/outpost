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
		"-eq 75",
		"<string>/tmp/node.log</string>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "a&b") {
		t.Error("unescaped & in plist")
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
