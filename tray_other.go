//go:build !windows && !linux && !darwin

package main

import "errors"

func runTray([]string) error {
	return errors.New("the tray icon is available on Windows, Linux and macOS desktops")
}
