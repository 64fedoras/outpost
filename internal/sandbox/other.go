//go:build !linux && !windows

package sandbox

import "os/exec"

// Sandboxing exec jobs is only built for Linux so far. Other systems run jobs
// as before and tell the controller so.
func Layers(mode string) []string { return nil }

func Wrap(cmd *exec.Cmd, p *Policy) error { return nil }

func RunChild(args []string) {}

// Attach runs after the command started; the returned func runs when it ended.
func Attach(cmd *exec.Cmd, mode string, ramBytes uint64) func() { return func() {} }

// PlatformMode is the mode this OS can enforce for the one asked for.
func PlatformMode(mode string) string { return Off }
