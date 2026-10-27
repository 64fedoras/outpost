//go:build !windows && !linux

package main

import "errors"

func runTray([]string) error {
	return errors.New("the tray icon is available on Windows and Linux desktops; on macOS it needs a build with cgo, which is not in this release")
}
