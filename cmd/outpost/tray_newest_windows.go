//go:build windows

package main

// The Windows tray already follows updates through its own launcher.
func traySwitchToNewest(string, []string) {}
