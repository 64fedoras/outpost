//go:build !linux && !windows

package main

import "testing"

func TestSandboxOffOutsideLinux(t *testing.T) {
	if sandboxPlatformMode(sandboxStrict) != sandboxOff || sandboxLayers(sandboxBasic) != nil {
		t.Fatal("only Linux (and, experimentally, Windows) sandboxes")
	}
}
