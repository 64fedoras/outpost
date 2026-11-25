package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Hardware struct {
	CPU      CPUInfo
	RAMBytes uint64
	GPUs     []GPUInfo
	// Integrated holds Intel iGPUs and AMD APUs. They are listed even next to a discrete GPU so the
	// node can show them, but only used (checkGPUs) when there is no discrete one.
	Integrated []GPUInfo
	Backends   []string
}

// checkGPUs are the GPUs the speed check and the llama.cpp build choice work with: the
// discrete ones, or the integrated ones when there are none.
func (hw Hardware) checkGPUs() []GPUInfo {
	if len(hw.GPUs) > 0 {
		return hw.GPUs
	}
	return hw.Integrated
}

func detectHardware() Hardware {
	hw := Hardware{CPU: CPUInfo{Threads: runtime.NumCPU(), Cores: runtime.NumCPU(), Sockets: 1}, Backends: []string{"cpu"}, GPUs: []GPUInfo{}}
	switch runtime.GOOS {
	case "linux":
		linuxCPU(&hw.CPU)
		mi := readMeminfo()
		hw.RAMBytes = mi["MemTotal"]
		discrete := linuxGPUDevices()
		hw.GPUs = linuxGPUs(discrete)
		hw.Integrated = linuxIntegratedGPUs(discrete)
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
		} else {
			// Intel Macs: listed so the node shows them. The Backend stays empty until the Metal
			// build is installed (hello), because the default builds for them run on the CPU only.
			hw.GPUs = parseMacGPUs([]byte(cmdOut("system_profiler", "SPDisplaysDataType", "-json")))
			if len(hw.GPUs) == 0 { // the plain-text report is the format seen on a real Mac Pro
				hw.GPUs = parseMacGPUsText(cmdOut("system_profiler", "SPDisplaysDataType"))
			}
		}
	case "windows":
		hw.CPU.Model = strings.TrimSpace(os.Getenv("PROCESSOR_IDENTIFIER"))
		windowsCPU(&hw.CPU)
		hw.RAMBytes = windowsRAM()
		hw.GPUs = windowsGPUs()
		hw.Integrated = windowsIntegratedGPUs()
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

// parseMacGPUs reads `system_profiler SPDisplaysDataType -json` and returns the discrete
// GPUs (1 GB of VRAM or more; Intel integrated graphics share system RAM and are skipped).
// Their Backend is empty: detected, not used by any installed llama.cpp build.
func parseMacGPUs(b []byte) []GPUInfo {
	var d struct {
		Items []struct {
			Model  string `json:"sppci_model"`
			Vendor string `json:"spdisplays_vendor"`
			VRAM   string `json:"spdisplays_vram"`
		} `json:"SPDisplaysDataType"`
	}
	out := []GPUInfo{}
	if json.Unmarshal(b, &d) != nil {
		return out
	}
	for _, it := range d.Items {
		f := strings.Fields(it.VRAM)
		if len(f) != 2 || it.Model == "" {
			continue
		}
		n, err := strconv.ParseFloat(f[0], 64)
		if err != nil {
			continue
		}
		switch strings.ToUpper(f[1]) {
		case "GB":
			n *= 1 << 30
		case "MB":
			n *= 1 << 20
		default:
			continue
		}
		if n < 1<<30 || strings.Contains(it.Vendor, "intel") { // Intel graphics share system RAM
			continue
		}
		v := "other"
		switch {
		case strings.Contains(it.Vendor, "amd"), strings.Contains(it.Model, "AMD"), strings.Contains(it.Model, "Radeon"):
			v = "amd"
		case strings.Contains(it.Vendor, "nvidia"):
			v = "nvidia"
		}
		out = append(out, GPUInfo{Vendor: v, Name: it.Model, VRAMBytes: uint64(n)})
	}
	return out
}

// parseMacGPUsText reads the plain `system_profiler SPDisplaysDataType` report:
// "Chipset Model: AMD FirePro D700", "VRAM (Total): 6 GB", "Vendor: AMD (0x1002)".
func parseMacGPUsText(txt string) []GPUInfo {
	out := []GPUInfo{}
	var name, vendor, vram string
	flush := func() {
		defer func() { name, vendor, vram = "", "", "" }()
		f := strings.Fields(vram)
		if name == "" || len(f) != 2 || strings.Contains(strings.ToLower(vendor), "intel") {
			return
		}
		n, err := strconv.ParseFloat(f[0], 64)
		if err != nil {
			return
		}
		switch strings.ToUpper(f[1]) {
		case "GB":
			n *= 1 << 30
		case "MB":
			n *= 1 << 20
		default:
			return
		}
		if n < 1<<30 {
			return
		}
		v := "other"
		l := strings.ToLower(vendor + " " + name)
		switch {
		case strings.Contains(l, "amd"), strings.Contains(l, "radeon"):
			v = "amd"
		case strings.Contains(l, "nvidia"):
			v = "nvidia"
		}
		out = append(out, GPUInfo{Vendor: v, Name: name, VRAMBytes: uint64(n)})
	}
	for _, line := range strings.Split(txt, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "Chipset Model":
			flush()
			name = v
		case "VRAM (Total)":
			vram = v
		case "Vendor":
			vendor = v
		}
	}
	flush()
	return out
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

// linuxGPUDevices lists the sysfs device dirs of usable GPUs, biggest VRAM
// first, so index 0 is the discrete card even when an integrated GPU (for
// example the 2 GB carve-out on a Ryzen desktop CPU) is card0. When a GPU with
// 4 GB or more exists, smaller ones are dropped as integrated.
func linuxGPUDevices() []string {
	var all []sysGPU
	smi := nvidiaSMI()
	nv := 0
	cards, _ := filepath.Glob("/sys/class/drm/card[0-9]*")
	for _, card := range cards {
		if !strings.Contains(filepath.Base(card), "-") && readUint(filepath.Join(card, "device", "vendor")) == 0x10de {
			nv++
		}
	}
	for _, card := range cards {
		if strings.Contains(filepath.Base(card), "-") {
			continue // connector entries like card0-DP-1
		}
		dev := filepath.Join(card, "device")
		vendor := readUint(filepath.Join(dev, "vendor"))
		vram := readUint(filepath.Join(dev, "mem_info_vram_total"))
		if vram == 0 && vendor == 0x10de {
			vram = nvidiaVRAMTotal(devSlot(dev), smi, nv == 1)
		}
		all = append(all, sysGPU{dev, vendor, vram})
	}
	return pickDiscrete(all)
}

// sysGPU is one /sys/class/drm graphics device.
type sysGPU struct {
	dir    string
	vendor uint64
	vram   uint64
}

// pickDiscrete chooses the discrete GPUs, biggest VRAM first: AMD, NVIDIA and Intel devices, minus
// AMD/Intel ones under 1 GB, minus everything under 4 GB when a card with 4 GB or more exists.
func pickDiscrete(all []sysGPU) []string {
	var keep []sysGPU
	for _, d := range all {
		switch d.vendor {
		case 0x1002, 0x10de, 0x8086:
		default:
			continue
		}
		if d.vendor != 0x10de && d.vram < 1<<30 {
			continue // integrated or virtual display adapter
		}
		keep = append(keep, d)
	}
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].vram > keep[j].vram })
	big := len(keep) > 0 && keep[0].vram >= 4<<30
	out := []string{}
	for _, d := range keep {
		if big && d.vram < 4<<30 {
			continue
		}
		out = append(out, d.dir)
	}
	return out
}

// isLinuxIntegrated says whether a sysfs graphics device is an Intel iGPU or AMD APU: an Intel
// or AMD device that is not one of the discrete GPUs, either because it has under 1 GB of
// dedicated memory (Intel reports none) or because a card with 4 GB or more outranks it (a
// desktop Ryzen's 2 GB carve-out next to a discrete card). NVIDIA and virtual display adapters
// (other vendors) are never integrated.
func isLinuxIntegrated(vendor uint64, discrete bool) bool {
	return (vendor == 0x1002 || vendor == 0x8086) && !discrete
}

// linuxIntegratedGPUs lists integrated graphics from /sys/class/drm: the Intel and AMD devices
// that linuxGPUDevices did not take as discrete. They are only used when the machine has no
// discrete GPU (see Hardware.checkGPUs), but always listed so the node can show them.
func linuxIntegratedGPUs(discrete []string) []GPUInfo {
	out := []GPUInfo{}
	isDiscrete := map[string]bool{}
	for _, d := range discrete {
		isDiscrete[d] = true
	}
	cards, _ := filepath.Glob("/sys/class/drm/card[0-9]*")
	for _, card := range cards {
		if strings.Contains(filepath.Base(card), "-") {
			continue // connector entries like card0-DP-1
		}
		dev := filepath.Join(card, "device")
		vendor := readUint(filepath.Join(dev, "vendor"))
		if !isLinuxIntegrated(vendor, isDiscrete[dev]) {
			continue
		}
		vram := readUint(filepath.Join(dev, "mem_info_vram_total"))
		g := GPUInfo{Vendor: "amd", Backend: "vulkan", VRAMBytes: vram, Integrated: true, Name: gpuName(dev)}
		if vendor == 0x8086 {
			g.Vendor = "intel"
		}
		out = append(out, g)
	}
	return out
}

// linuxGPUs finds discrete GPUs through /sys/class/drm. AMD exposes VRAM and
// busy % there, which is all v0 needs; names come from lspci when present.
func linuxGPUs(devs []string) []GPUInfo {
	out := []GPUInfo{}
	smi := nvidiaSMI()
	for _, dev := range devs {
		g := GPUInfo{}
		switch readUint(filepath.Join(dev, "vendor")) {
		case 0x1002:
			g.Vendor, g.Backend = "amd", "rocm"
			if _, err := os.Stat("/opt/rocm"); err != nil {
				g.Backend = "vulkan"
			}
		case 0x10de:
			g.Vendor, g.Backend = "nvidia", "vulkan" // Outpost's Linux GPU build is Vulkan
		case 0x8086:
			g.Vendor, g.Backend = "intel", "vulkan"
		}
		g.VRAMBytes = readUint(filepath.Join(dev, "mem_info_vram_total"))
		if g.Vendor == "nvidia" && g.VRAMBytes == 0 {
			g.VRAMBytes = nvidiaVRAMTotal(devSlot(dev), smi, len(devs) == 1)
		}
		g.Name = gpuName(dev)
		out = append(out, g)
	}
	return out
}

// devSlot is the PCI slot (like 0000:01:00.0) of a sysfs device dir.
func devSlot(dev string) string {
	link, err := os.Readlink(dev)
	if err != nil {
		return ""
	}
	return strings.ToLower(filepath.Base(link))
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
		s.gpuDevices = linuxGPUDevices()
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
	if runtime.GOOS == "darwin" {
		s.sampleDarwin(&hb, runtime.NumCPU())
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
	var smi map[string]nvidiaStat
	for _, d := range s.gpuDevices {
		if readUint(filepath.Join(d, "vendor")) == 0x10de {
			if smi == nil {
				smi = nvidiaSMI()
			}
			r := smi[devSlot(d)]
			hb.GPUUtilPct = append(hb.GPUUtilPct, r.UtilPct)
			hb.VRAMUsedBytes = append(hb.VRAMUsedBytes, r.UsedB)
			hb.GPUTempC = append(hb.GPUTempC, r.TempC)
			continue
		}
		hb.GPUUtilPct = append(hb.GPUUtilPct, int(readUint(filepath.Join(d, "gpu_busy_percent"))))
		hb.VRAMUsedBytes = append(hb.VRAMUsedBytes, readUint(filepath.Join(d, "mem_info_vram_used")))
		hb.GPUTempC = append(hb.GPUTempC, gpuTempC(d))
	}
	return hb
}

// gpuTempC reads a Linux GPU's temperature from its hwmon sensor (amdgpu and
// i915/xe expose one; the NVIDIA driver does not), in °C, or 0 if there is none.
// "edge" (temp1) is the sensor every amdgpu card has.
func gpuTempC(dev string) int {
	ins, _ := filepath.Glob(filepath.Join(dev, "hwmon", "hwmon*", "temp1_input"))
	for _, p := range ins {
		if v := readUint(p); v > 0 {
			return int(v / 1000)
		}
	}
	return 0
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
		fmt.Printf("GPU %d now: %d%% busy, %.2f GB VRAM in use", i, hb.GPUUtilPct[i], float64(hb.VRAMUsedBytes[i])/(1<<30))
		if i < len(hb.GPUTempC) && hb.GPUTempC[i] > 0 {
			fmt.Printf(", %d °C", hb.GPUTempC[i])
		}
		fmt.Println()
	}
}
