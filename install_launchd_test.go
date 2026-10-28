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
