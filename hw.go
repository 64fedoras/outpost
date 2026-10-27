package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Hardware struct {
	CPU      CPUInfo
	RAMBytes uint64
	GPUs     []GPUInfo
	Backends []string
}

func detectHardware() Hardware {
	hw := Hardware{CPU: CPUInfo{Threads: runtime.NumCPU(), Cores: runtime.NumCPU(), Sockets: 1}, Backends: []string{"cpu"}, GPUs: []GPUInfo{}}
	switch runtime.GOOS {
	case "linux":
		linuxCPU(&hw.CPU)
		mi := readMeminfo()
		hw.RAMBytes = mi["MemTotal"]
		hw.GPUs = linuxGPUs()
	case "darwin":
		hw.CPU.Model = strings.TrimSpace(cmdOut("sysctl", "-n", "machdep.cpu.brand_string"))
		if v, err := strconv.ParseUint(strings.TrimSpace(cmdOut("sysctl", "-n", "hw.memsize")), 10, 64); err == nil {
			hw.RAMBytes = v
		}
		if c, err := strconv.Atoi(strings.TrimSpace(cmdOut("sysctl", "-n", "hw.physicalcpu"))); err == nil && c > 0 {
			hw.CPU.Cores = c
		}
		if runtime.GOARCH == "arm64" {
			hw.GPUs = append(hw.GPUs, GPUInfo{Vendor: "apple", Name: hw.CPU.Model + " GPU", VRAMBytes: hw.RAMBytes, Backend: "metal"})
		}
	case "windows":
		hw.CPU.Model = strings.TrimSpace(os.Getenv("PROCESSOR_IDENTIFIER"))
		windowsCPU(&hw.CPU)
		hw.RAMBytes = windowsRAM()
		hw.GPUs = windowsGPUs()
	}
	seen := map[string]bool{"cpu": true}
	for _, g := range hw.GPUs {
		if g.Backend != "" && !seen[g.Backend] {
			seen[g.Backend] = true
			hw.Backends = append(hw.Backends, g.Backend)
		}
	}
	return hw
}

func cmdOut(name string, args ...string) string {
	b, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return string(b)
}

func linuxCPU(c *CPUInfo) {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return
	}
	defer f.Close()
	sockets := map[string]bool{}
	coresPer := 0
	phys := ""
	coreIDs := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "model name":
			if c.Model == "" {
				c.Model = v
			}
		case "physical id":
			phys = v
			sockets[v] = true
		case "core id":
			coreIDs[phys+"/"+v] = true
		case "cpu cores":
			coresPer, _ = strconv.Atoi(v)
		}
	}
	if len(sockets) > 0 {
		c.Sockets = len(sockets)
	}
	switch {
	case len(coreIDs) > 0:
		c.Cores = len(coreIDs)
	case coresPer > 0:
		c.Cores = coresPer * c.Sockets
	}
}

func readMeminfo() map[string]uint64 {
	out := map[string]uint64{}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(v)
		if len(fields) == 0 {
			continue
		}
		n, _ := strconv.ParseUint(fields[0], 10, 64)
		if len(fields) > 1 && fields[1] == "kB" {
			n *= 1024
		}
		out[k] = n
	}
	return out
}

func readUint(p string) uint64 {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseUint(strings.TrimSpace(string(b)), 0, 64)
	return v
}

// linuxGPUs finds discrete GPUs through /sys/class/drm. AMD exposes VRAM and
// busy % there, which is all v0 needs; names come from lspci when present.
func linuxGPUs() []GPUInfo {
	out := []GPUInfo{}
	cards, _ := filepath.Glob("/sys/class/drm/card[0-9]*")
	for _, card := range cards {
		if strings.Contains(filepath.Base(card), "-") {
			continue // connector entries like card0-DP-1
		}
		dev := filepath.Join(card, "device")
		vendor := readUint(filepath.Join(dev, "vendor"))
		g := GPUInfo{}
		switch vendor {
		case 0x1002:
			g.Vendor, g.Backend = "amd", "rocm"
			if _, err := os.Stat("/opt/rocm"); err != nil {
				g.Backend = "vulkan"
			}
		case 0x10de:
			g.Vendor, g.Backend = "nvidia", "cuda"
		case 0x8086:
			g.Vendor, g.Backend = "intel", "vulkan"
		default:
			continue
		}
		g.VRAMBytes = readUint(filepath.Join(dev, "mem_info_vram_total"))
		if g.Vendor != "nvidia" && g.VRAMBytes < 1<<30 {
			continue // integrated or virtual display adapter
		}
		g.Name = gpuName(dev)
		out = append(out, g)
	}
	return out
}

func gpuName(dev string) string {
	link, err := os.Readlink(dev)
	if err == nil {
		slot := filepath.Base(link)
		if s := strings.TrimSpace(cmdOut("lspci", "-s", slot)); s != "" {
			if _, after, ok := strings.Cut(s, ": "); ok {
				return after
			}
			return s
		}
	}
	return "GPU"
}

// statSampler computes CPU % between samples (Linux) and fills memory stats.
type statSampler struct {
	mu         sync.Mutex
	prevIdle   uint64
	prevTotal  uint64
	gpuDevices []string
	win        winStats
}

func newStatSampler() *statSampler {
	s := &statSampler{}
	if runtime.GOOS == "linux" {
		cards, _ := filepath.Glob("/sys/class/drm/card[0-9]*/device/mem_info_vram_total")
		for _, p := range cards {
			if readUint(p) >= 1<<30 {
				s.gpuDevices = append(s.gpuDevices, filepath.Dir(p))
			}
		}
	}
	s.sample() // first sample primes CPU % (and GPU counters on Windows)
	return s
}

func (s *statSampler) sample() Heartbeat {
	hb := Heartbeat{GPUUtilPct: []int{}, VRAMUsedBytes: []uint64{}, RunningJobs: []string{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	if runtime.GOOS == "windows" {
		s.sampleWindows(&hb)
		return hb
	}
	if runtime.GOOS != "linux" {
		return hb
	}
	if b, err := os.ReadFile("/proc/stat"); err == nil {
		line, _, _ := strings.Cut(string(b), "\n")
		fields := strings.Fields(line)
		var total, idle uint64
		for i, f := range fields[1:] {
			v, _ := strconv.ParseUint(f, 10, 64)
			total += v
			if i == 3 || i == 4 { // idle + iowait
				idle += v
			}
		}
		switch {
		case s.prevTotal == 0:
			s.prevTotal, s.prevIdle = total, idle
		case total-s.prevTotal >= 50: // need ~0.5 s of jiffies for a meaningful %
			dt, di := total-s.prevTotal, idle-s.prevIdle
			hb.CPUPct = float64(int(1000*(1-float64(di)/float64(dt)))) / 10
			s.prevTotal, s.prevIdle = total, idle
		}
	}
	mi := readMeminfo()
	hb.RAMTotalBytes = mi["MemTotal"]
	if mi["MemTotal"] > mi["MemAvailable"] {
		hb.RAMUsedBytes = mi["MemTotal"] - mi["MemAvailable"]
	}
	if mi["SwapTotal"] > mi["SwapFree"] {
		hb.SwapUsedBytes = mi["SwapTotal"] - mi["SwapFree"]
	}
	for _, d := range s.gpuDevices {
		hb.GPUUtilPct = append(hb.GPUUtilPct, int(readUint(filepath.Join(d, "gpu_busy_percent"))))
		hb.VRAMUsedBytes = append(hb.VRAMUsedBytes, readUint(filepath.Join(d, "mem_info_vram_used")))
	}
	return hb
}

// runHWInfo prints what this machine reports to the controller, for checking
// detection without joining (`outpost hwinfo`).
func runHWInfo() {
	hw := detectHardware()
	fmt.Printf("CPU: %s (%d cores, %d threads, %d socket(s))\n", hw.CPU.Model, hw.CPU.Cores, hw.CPU.Threads, hw.CPU.Sockets)
	fmt.Printf("RAM: %.1f GB\n", float64(hw.RAMBytes)/(1<<30))
	if len(hw.GPUs) == 0 {
		fmt.Println("GPUs: none found (integrated graphics under 1 GB are ignored)")
	}
	for i, g := range hw.GPUs {
		fmt.Printf("GPU %d: %s (%s, %.1f GB VRAM, llama.cpp backend %s)\n", i, g.Name, g.Vendor, float64(g.VRAMBytes)/(1<<30), g.Backend)
	}
	fmt.Println("Vulkan loader:", vulkanAvailable())
	s := newStatSampler()
	time.Sleep(2 * time.Second)
	hb := s.sample()
	fmt.Printf("Now: CPU %.1f%%, RAM %.1f / %.1f GB, swap/page file %.1f GB\n", hb.CPUPct,
		float64(hb.RAMUsedBytes)/(1<<30), float64(hb.RAMTotalBytes)/(1<<30), float64(hb.SwapUsedBytes)/(1<<30))
	for i := range hb.GPUUtilPct {
		fmt.Printf("GPU %d now: %d%% busy, %.2f GB VRAM in use\n", i, hb.GPUUtilPct[i], float64(hb.VRAMUsedBytes[i])/(1<<30))
	}
}
