//go:build windows

package sandbox

import (
	"os"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows sandbox: EXPERIMENTAL and OFF in releases. Only a Job Object so far:
// the whole process tree dies when the job ends or the node exits, at most
// winMaxProcs processes, and the tree may use at most 75% of RAM. There is no
// file or network confinement, no restricted token and no environment
// scrubbing yet (design/sandbox-exec.md, "Windows"). Untested on a real Windows
// machine. Set OUTPOST_EXPERIMENTAL_WINDOWS_SANDBOX=1 in the node's environment to try it.
const winMaxProcs = 512

func windowsSandboxOn() bool { return os.Getenv("OUTPOST_EXPERIMENTAL_WINDOWS_SANDBOX") == "1" }

func PlatformMode(mode string) string {
	if windowsSandboxOn() {
		return mode
	}
	return Off
}

func Layers(mode string) []string {
	if mode == Off {
		return nil
	}
	return []string{"job-object"}
}

// Wrap changes nothing on Windows: the limits are applied by Attach.
func Wrap(cmd *exec.Cmd, p *Policy) error { return nil }

func RunChild(args []string) {}

// jobLimits are the Job Object limits for a job on a machine with ramBytes of RAM.
func jobLimits(ramBytes uint64) windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	info.BasicLimitInformation.ActiveProcessLimit = winMaxProcs
	if ramBytes > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_JOB_MEMORY
		info.JobMemoryLimit = uintptr(ramBytes / 4 * 3)
	}
	return info
}

// Attach puts the started command in a Job Object. There is a short gap
// between the start and the assignment in which a child could escape; closing
// it needs CREATE_SUSPENDED handling that is not built yet.
func Attach(cmd *exec.Cmd, mode string, ramBytes uint64) func() {
	noop := func() {}
	if mode == Off || cmd.Process == nil {
		return noop
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return noop
	}
	info := jobLimits(ramBytes)
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return noop
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return noop
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(job)
		return noop
	}
	return func() { windows.CloseHandle(job) } // KILL_ON_JOB_CLOSE ends anything left
}
