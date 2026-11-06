//go:build !linux && !windows

package main

import "os/exec"

// Sandboxing exec jobs is only built for Linux so far. Other systems run jobs
// as before and tell the controller so.
func sandboxLayers(mode string) []string { return nil }

func sandboxWrap(cmd *exec.Cmd, p *sandboxPolicy) *ErrInfo { return nil }

func runSandboxChild(args []string) {}

// sandboxAttach runs after the command started; the returned func runs when it ended.
func sandboxAttach(cmd *exec.Cmd, mode string) func() { return func() {} }

// sandboxPlatformMode is the mode this OS can enforce for the one asked for.
func sandboxPlatformMode(mode string) string { return sandboxOff }
