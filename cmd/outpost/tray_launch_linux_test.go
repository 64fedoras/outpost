//go:build linux

package main

import "testing"

func TestUserSessionEnvNoSession(t *testing.T) {
	if _, ok := userSessionEnv(987654); ok {
		t.Fatal("a user with no /run/user bus must not count as logged in")
	}
	if msg := launchTrayNow(987654, 987654, "/nonexistent", "/nonexistent"); msg == "started now" {
		t.Fatal("must not start a tray without a session")
	}
}
