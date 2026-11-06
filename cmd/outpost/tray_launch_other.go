//go:build !linux

package main

func launchTrayNow(uid, gid int, home, dataDir string) string { return "" }
