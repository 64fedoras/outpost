//go:build !windows

package main

import "errors"

func installNodeWindows([]string) error { return nil }

func runService([]string) error   { return errors.New("the service command is Windows-only") }
func uninstallNodeWindows() error { return nil }
