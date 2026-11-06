package main

import (
	"os/exec"
	"testing"
)

// The sd-server command must start: on Unix setProcGroup sets Cancel, which Go refuses
// for a command not made with CommandContext (the "load_failed" a node showed).
func TestNewSDCmdStarts(t *testing.T) {
	bin, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no true binary")
	}
	cmd := newSDCmd(bin, nil)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	// and the process is stoppable the way stop() does it
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	c2 := newSDCmd(sleep, []string{"30"})
	if err := c2.Start(); err != nil {
		t.Fatal(err)
	}
	killProcGroup(c2)
	_ = c2.Process.Kill()
	_ = c2.Wait()
}
