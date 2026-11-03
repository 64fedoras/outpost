//go:build !linux

package main

import "os/exec"

// Sandboxing exec jobs is only built for Linux so far. Other systems run jobs
// as before and tell the controller so.
func sandboxLayers(mode string) []string { return nil }

func sandboxWrap(cmd *exec.Cmd, p *sandboxPolicy) *ErrInfo { return nil }

func runSandboxChild(args []string) {}
