//go:build windows

package sandbox

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsSandboxIsOffByDefault(t *testing.T) {
	t.Setenv("OUTPOST_EXPERIMENTAL_WINDOWS_SANDBOX", "")
	if got := PlatformMode(Strict); got != Off {
		t.Fatalf("Windows sandbox must be off unless the experimental switch is set, got %q", got)
	}
	t.Setenv("OUTPOST_EXPERIMENTAL_WINDOWS_SANDBOX", "1")
	if got := PlatformMode(Basic); got != Basic {
		t.Fatalf("switch ignored: %q", got)
	}
}

func TestJobLimits(t *testing.T) {
	info := jobLimits(16 << 30)
	f := info.BasicLimitInformation.LimitFlags
	if f&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE == 0 || f&windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS == 0 || f&windows.JOB_OBJECT_LIMIT_JOB_MEMORY == 0 {
		t.Fatalf("flags %#x", f)
	}
	if info.JobMemoryLimit != uintptr(12<<30) || info.BasicLimitInformation.ActiveProcessLimit != winMaxProcs {
		t.Errorf("limits: %+v", info)
	}
	if jobLimits(0).BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_JOB_MEMORY != 0 {
		t.Error("no memory limit when RAM is unknown")
	}
}

// Needs a real Windows machine: the job must die with its job object.
func TestJobObjectKillsLeftovers(t *testing.T) {
	t.Setenv("OUTPOST_EXPERIMENTAL_WINDOWS_SANDBOX", "1")
	cmd := exec.Command("cmd", "/c", "ping -n 30 127.0.0.1 > nul")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	release := Attach(cmd, Basic, 0)
	release() // closing the job kills the process
	if err := cmd.Wait(); err == nil {
		t.Error("process should have been killed by the job object")
	}
}
