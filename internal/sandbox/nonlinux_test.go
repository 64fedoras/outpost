//go:build !linux && !windows

package sandbox

import "testing"

func TestSandboxOffOutsideLinux(t *testing.T) {
	if PlatformMode(Strict) != Off || Layers(Basic) != nil {
		t.Fatal("only Linux (and, experimentally, Windows) sandboxes")
	}
}
