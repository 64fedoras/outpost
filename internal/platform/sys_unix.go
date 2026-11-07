//go:build !windows

package platform

import (
	"os"
	"os/exec"
	"syscall"
)

func DiskFree(path string) uint64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return uint64(st.Bavail) * uint64(st.Bsize)
}

// DiskTotal is the size of the file system holding path.
func DiskTotal(path string) uint64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return uint64(st.Blocks) * uint64(st.Bsize)
}

// SetProcGroup runs the command in its own process group so cancelling a job
// also stops anything it spawned (for example the children of sh -c).
func SetProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		return nil
	}
}

func WindowsRAM() uint64 { return 0 }

func OSInterrupt() os.Signal { return syscall.SIGTERM }

// KillProcGroup kills whatever is left of the command's process group.
func KillProcGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
