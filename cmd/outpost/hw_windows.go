//go:build windows

package main

// Windows hardware detection without cgo or extra modules:
//   - CPU name and core/socket counts from the registry and
//     GetLogicalProcessorInformation
//   - GPUs (name, vendor, dedicated VRAM, LUID) from DXGI
//   - live CPU % from GetSystemTimes, RAM and page file from GlobalMemoryStatusEx
//   - live GPU busy % and VRAM in use from the "GPU Engine" and
//     "GPU Adapter Memory" performance counters (what Task Manager shows)

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"outpost/internal/platform"
)

var (
	procGetSystemTimes                 = kernel32.NewProc("GetSystemTimes")
	procGetLogicalProcessorInformation = kernel32.NewProc("GetLogicalProcessorInformation")

	dxgi                   = syscall.NewLazyDLL("dxgi.dll")
	procCreateDXGIFactory1 = dxgi.NewProc("CreateDXGIFactory1")

	pdh                             = syscall.NewLazyDLL("pdh.dll")
	procPdhOpenQueryW               = pdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounterW       = pdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData         = pdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounterArray = pdh.NewProc("PdhGetFormattedCounterArrayW")
)

// ---------- CPU ----------

func windowsCPU(c *CPUInfo) {
	c.Threads = runtime.NumCPU()
	if name := regString(syscall.HKEY_LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, "ProcessorNameString"); name != "" {
		c.Model = strings.TrimSpace(name)
	}
	// SYSTEM_LOGICAL_PROCESSOR_INFORMATION is 32 bytes on 64-bit Windows:
	// ULONG_PTR mask, DWORD relationship, 4 bytes padding, 16-byte union.
	const entry = 32
	var size uint32
	procGetLogicalProcessorInformation.Call(0, uintptr(unsafe.Pointer(&size)))
	if size == 0 || size > 1<<20 || unsafe.Sizeof(uintptr(0)) != 8 {
		return
	}
	buf := make([]byte, size)
	if r, _, _ := procGetLogicalProcessorInformation.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r == 0 {
		return
	}
	cores, sockets := 0, 0
	for off := 0; off+entry <= int(size); off += entry {
		switch *(*uint32)(unsafe.Pointer(&buf[off+8])) {
		case 0: // RelationProcessorCore
			cores++
		case 3: // RelationProcessorPackage
			sockets++
		}
	}
	if cores > 0 {
		c.Cores = cores
	}
	if sockets > 0 {
		c.Sockets = sockets
	}
}

func regString(root syscall.Handle, path, name string) string {
	var k syscall.Handle
	p, _ := syscall.UTF16PtrFromString(path)
	if syscall.RegOpenKeyEx(root, p, 0, syscall.KEY_READ, &k) != nil {
		return ""
	}
	defer syscall.RegCloseKey(k)
	n, _ := syscall.UTF16PtrFromString(name)
	var typ, size uint32
	if syscall.RegQueryValueEx(k, n, nil, &typ, nil, &size) != nil || size == 0 || size > 4096 {
		return ""
	}
	buf := make([]uint16, size/2+1)
	if syscall.RegQueryValueEx(k, n, nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &size) != nil {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

// ---------- GPUs via DXGI ----------

type winGPU struct {
	GPUInfo
	luid string // "0x%08X_0x%08X" (high_low), as the performance counters name it
}

var (
	gpuOnce sync.Once
	gpuList []winGPU
)

// IID_IDXGIFactory1 {770aae78-f26f-4dba-a829-253c83d1b387}
var iidDXGIFactory1 = syscall.GUID{Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba, Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}

// dxgiAdapterDesc1 mirrors DXGI_ADAPTER_DESC1.
type dxgiAdapterDesc1 struct {
	Description           [128]uint16
	VendorID              uint32
	DeviceID              uint32
	SubSysID              uint32
	Revision              uint32
	DedicatedVideoMemory  uintptr
	DedicatedSystemMemory uintptr
	SharedSystemMemory    uintptr
	LuidLow               uint32
	LuidHigh              int32
	Flags                 uint32
}

// comCall calls method index i of a COM object's vtable.
func comCall(obj uintptr, i int, args ...uintptr) uintptr {
	// A COM object starts with a pointer to its table of method pointers.
	com := *(**struct{ vtbl *[64]uintptr })(unsafe.Pointer(&obj))
	fn := com.vtbl[i]
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

func windowsGPUList() []winGPU {
	gpuOnce.Do(func() {
		defer func() { recover() }()
		if procCreateDXGIFactory1.Find() != nil {
			return
		}
		var factory uintptr
		if hr, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidDXGIFactory1)), uintptr(unsafe.Pointer(&factory))); hr != 0 || factory == 0 {
			return
		}
		defer comCall(factory, 2) // Release
		seen := map[string]bool{}
		for i := 0; i < 16; i++ {
			var adapter uintptr
			// IDXGIFactory1::EnumAdapters1 is vtable slot 12.
			if hr := comCall(factory, 12, uintptr(i), uintptr(unsafe.Pointer(&adapter))); hr != 0 || adapter == 0 {
				break // DXGI_ERROR_NOT_FOUND: no more adapters
			}
			var d dxgiAdapterDesc1
			hr := comCall(adapter, 10, uintptr(unsafe.Pointer(&d))) // IDXGIAdapter1::GetDesc1
			comCall(adapter, 2)
			if hr != 0 || d.Flags&2 != 0 { // DXGI_ADAPTER_FLAG_SOFTWARE (Basic Render Driver)
				continue
			}
			g := winGPU{GPUInfo: GPUInfo{Name: strings.TrimSpace(syscall.UTF16ToString(d.Description[:])), VRAMBytes: uint64(d.DedicatedVideoMemory)},
				luid: fmt.Sprintf("0x%08X_0x%08X", uint32(d.LuidHigh), d.LuidLow)}
			switch d.VendorID {
			case 0x1002:
				g.Vendor, g.Backend = "amd", "vulkan"
			case 0x10de:
				g.Vendor, g.Backend = "nvidia", "cuda"
			case 0x8086:
				g.Vendor, g.Backend = "intel", "vulkan"
			default:
				continue
			}
			if g.VRAMBytes < 1<<30 || seen[g.luid] {
				continue // integrated graphics or a duplicate entry
			}
			seen[g.luid] = true
			gpuList = append(gpuList, g)
		}
	})
	return gpuList
}

func windowsGPUs() []GPUInfo {
	out := []GPUInfo{}
	for _, g := range windowsGPUList() {
		out = append(out, g.GPUInfo)
	}
	return out
}

// ---------- live stats ----------

type winStats struct {
	prevIdle, prevTotal uint64
	query               uintptr
	engine, memory      uintptr // counter handles
	pdhOK               bool
}

func filetime(ft syscall.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

func (s *statSampler) sampleWindows(hb *Heartbeat) {
	// CPU %: kernel time includes idle time.
	var idle, kern, user syscall.Filetime
	if r, _, _ := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kern)), uintptr(unsafe.Pointer(&user))); r != 0 {
		i, t := filetime(idle), filetime(kern)+filetime(user)
		w := &s.win
		if w.prevTotal != 0 && t > w.prevTotal {
			hb.CPUPct = float64(int(1000*(1-float64(i-w.prevIdle)/float64(t-w.prevTotal)))) / 10
		}
		w.prevIdle, w.prevTotal = i, t
	}
	if m, ok := platform.MemoryStatus(); ok {
		hb.RAMTotalBytes = m.TotalPhys
		hb.RAMUsedBytes = m.TotalPhys - m.AvailPhys
		// Page file in use beyond physical memory, the closest match to swap.
		commit := m.TotalPageFile - m.AvailPageFile
		if commit > hb.RAMUsedBytes {
			hb.SwapUsedBytes = commit - hb.RAMUsedBytes
		}
	}
	gpus := windowsGPUList()
	if len(gpus) == 0 {
		return
	}
	busy, used := s.win.gpuCounters()
	for _, g := range gpus {
		hb.GPUUtilPct = append(hb.GPUUtilPct, int(busy[g.luid]+0.5))
		hb.VRAMUsedBytes = append(hb.VRAMUsedBytes, used[g.luid])
	}
}

// gpuCounters returns busy % and dedicated VRAM in use per adapter LUID.
// Busy % is the busiest engine type (3D, Compute, Copy, ...) summed over
// processes, which is how Task Manager computes it.
func (w *winStats) gpuCounters() (map[string]float64, map[string]uint64) {
	busy, used := map[string]float64{}, map[string]uint64{}
	if procPdhOpenQueryW.Find() != nil {
		return busy, used
	}
	if w.query == 0 {
		if r, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&w.query))); r != 0 {
			w.query = 0
			return busy, used
		}
		add := func(path string, h *uintptr) {
			p, _ := syscall.UTF16PtrFromString(path)
			procPdhAddEnglishCounterW.Call(w.query, uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(h)))
		}
		add(`\GPU Engine(*)\Utilization Percentage`, &w.engine)
		add(`\GPU Adapter Memory(*)\Dedicated Usage`, &w.memory)
		procPdhCollectQueryData.Call(w.query) // rate counters need two samples
		return busy, used
	}
	if r, _, _ := procPdhCollectQueryData.Call(w.query); r != 0 {
		return busy, used
	}
	perType := map[string]float64{}                         // luid|engtype -> sum over processes
	for name, v := range pdhArray(w.engine, 0x200|0x8000) { // PDH_FMT_DOUBLE | PDH_FMT_NOCAP100
		luid := luidOf(name)
		_, typ, _ := strings.Cut(name, "_engtype_")
		perType[luid+"|"+typ] += v
	}
	for k, v := range perType {
		luid, _, _ := strings.Cut(k, "|")
		if v > 100 {
			v = 100
		}
		if v > busy[luid] {
			busy[luid] = v
		}
	}
	for name, v := range pdhArray(w.memory, 0x200) {
		used[luidOf(name)] += uint64(v)
	}
	return busy, used
}

// luidOf pulls "0x00000000_0x0000D1A5" out of an instance name such as
// "pid_1234_luid_0x00000000_0x0000D1A5_phys_0_eng_0_engtype_3D".
func luidOf(name string) string {
	_, rest, ok := strings.Cut(strings.ToUpper(name), "LUID_")
	if !ok {
		return ""
	}
	parts := strings.SplitN(rest, "_", 3)
	if len(parts) < 2 {
		return ""
	}
	return strings.Replace(parts[0], "0X", "0x", 1) + "_" + strings.Replace(parts[1], "0X", "0x", 1)
}

// pdhArray reads every instance of a wildcard counter as doubles.
func pdhArray(counter uintptr, format uint32) map[string]float64 {
	out := map[string]float64{}
	if counter == 0 {
		return out
	}
	var size, count uint32
	const pdhMoreData = 0x800007D2
	r, _, _ := procPdhGetFormattedCounterArray.Call(counter, uintptr(format), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
	if uint32(r) != pdhMoreData || size == 0 || size > 16<<20 {
		return out
	}
	buf := make([]byte, size)
	r, _, _ = procPdhGetFormattedCounterArray.Call(counter, uintptr(format), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&buf[0])))
	if r != 0 {
		return out
	}
	// PDH_FMT_COUNTERVALUE_ITEM_W: LPWSTR name; { DWORD status; (pad); double value }
	type item struct {
		Name   *uint16
		Status uint32
		_      uint32
		Value  float64
	}
	items := unsafe.Slice((*item)(unsafe.Pointer(&buf[0])), count)
	for _, it := range items {
		if it.Status != 0 && it.Status != 1 { // PDH_CSTATUS_VALID_DATA / NEW_DATA
			continue
		}
		out[utf16PtrString(it.Name)] += it.Value
	}
	return out
}

func utf16PtrString(p *uint16) string {
	if p == nil {
		return ""
	}
	var s []uint16
	for ptr := unsafe.Pointer(p); ; ptr = unsafe.Add(ptr, 2) {
		c := *(*uint16)(ptr)
		if c == 0 || len(s) > 1024 {
			break
		}
		s = append(s, c)
	}
	return syscall.UTF16ToString(s)
}
