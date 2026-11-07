//go:build windows

package platform

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceExW  = kernel32.NewProc("GetDiskFreeSpaceExW")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

func DiskFree(path string) uint64 {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var free, total, totalFree uint64
	r, _, _ := procGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&free)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree)))
	if r == 0 {
		return 0
	}
	return free
}

// DiskTotal is the size of the volume holding path.
func DiskTotal(path string) uint64 {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var free, total, totalFree uint64
	r, _, _ := procGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&free)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree)))
	if r == 0 {
		return 0
	}
	return total
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// Memory is the machine's physical memory and page file, in bytes.
type Memory struct {
	TotalPhys, AvailPhys, TotalPageFile, AvailPageFile uint64
}

// MemoryStatus reads GlobalMemoryStatusEx. ok is false when the call fails.
func MemoryStatus() (m Memory, ok bool) {
	var x memoryStatusEx
	x.Length = uint32(unsafe.Sizeof(x))
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&x)))
	if r == 0 {
		return Memory{}, false
	}
	return Memory{x.TotalPhys, x.AvailPhys, x.TotalPageFile, x.AvailPageFile}, true
}

func WindowsRAM() uint64 {
	m, ok := MemoryStatus()
	if !ok {
		return 0
	}
	return m.TotalPhys
}

func SetProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// Windows cannot deliver SIGTERM; Kill stops llama-server.
func OSInterrupt() os.Signal { return os.Kill }

// KillProcGroup is a no-op on Windows for now (job objects come with the Windows sandbox).
func KillProcGroup(cmd *exec.Cmd) {}
