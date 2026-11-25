package main

import (
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// macOS has no /proc, so memory and CPU come from vm_stat, sysctl and ps.
// The parsers are plain functions so they are tested on every platform.

// parseVMStat returns the memory in use, counted like Activity Monitor's
// "Memory Used" (active + wired + compressed pages), from `vm_stat` output.
func parseVMStat(out string) uint64 {
	page := uint64(4096)
	pages := map[string]uint64{}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Mach Virtual Memory Statistics") {
			if i := strings.Index(line, "page size of "); i >= 0 {
				if f := strings.Fields(line[i+len("page size of "):]); len(f) > 0 {
					if n, err := strconv.ParseUint(f[0], 10, 64); err == nil && n > 0 {
						page = n
					}
				}
			}
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), "."), 10, 64)
		if err == nil {
			pages[strings.Trim(strings.TrimSpace(k), `"`)] = n
		}
	}
	used := pages["Pages active"] + pages["Pages wired down"] + pages["Pages occupied by compressor"]
	return used * page
}

// parseSwapUsage reads the used bytes from `sysctl -n vm.swapusage`
// ("total = 2048.00M  used = 1024.50M  free = ...").
func parseSwapUsage(out string) uint64 {
	i := strings.Index(out, "used = ")
	if i < 0 {
		return 0
	}
	f := strings.Fields(out[i+len("used = "):])
	if len(f) == 0 {
		return 0
	}
	s := f[0]
	mult := 1.0
	if n := len(s); n > 0 {
		switch s[n-1] {
		case 'K':
			mult = 1 << 10
		case 'M':
			mult = 1 << 20
		case 'G':
			mult = 1 << 30
		}
		if mult != 1 {
			s = s[:n-1]
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0
	}
	return uint64(v * mult)
}

// parsePSCPU sums the %cpu column of `ps -A -o %cpu` and divides by the
// logical CPU count, giving 0-100.
func parsePSCPU(out string, cpus int) float64 {
	if cpus < 1 {
		cpus = 1
	}
	sum := 0.0
	for _, line := range strings.Split(out, "\n") {
		if v, err := strconv.ParseFloat(strings.TrimSpace(line), 64); err == nil {
			sum += v
		}
	}
	pct := sum / float64(cpus)
	if pct > 100 {
		pct = 100
	}
	return float64(int(pct*10)) / 10
}

// sampleDarwin fills CPU %, RAM and swap for a macOS host, and GPU use on Apple Silicon. Failures leave the
// field at zero, as on the other platforms.
func (s *statSampler) sampleDarwin(hb *Heartbeat, cpus int) {
	if v, err := strconv.ParseUint(strings.TrimSpace(cmdOut("/usr/sbin/sysctl", "-n", "hw.memsize")), 10, 64); err == nil {
		hb.RAMTotalBytes = v
	}
	hb.RAMUsedBytes = parseVMStat(cmdOut("/usr/bin/vm_stat"))
	if hb.RAMTotalBytes > 0 && hb.RAMUsedBytes > hb.RAMTotalBytes {
		hb.RAMUsedBytes = hb.RAMTotalBytes
	}
	hb.SwapUsedBytes = parseSwapUsage(cmdOut("/usr/sbin/sysctl", "-n", "vm.swapusage"))
	hb.CPUPct = parsePSCPU(cmdOut("/bin/ps", "-A", "-o", "%cpu"), cpus)
	if runtime.GOARCH == "arm64" {
		// Apple Silicon: one GPU (the "apple" entry hw.go adds), so one value each.
		if util, used, ok := parseIORegGPU(cmdOut("/usr/sbin/ioreg", "-r", "-d", "1", "-w", "0", "-c", "IOAccelerator")); ok {
			hb.GPUUtilPct = append(hb.GPUUtilPct, util)
			hb.VRAMUsedBytes = append(hb.VRAMUsedBytes, used)
		}
	}
}

var (
	ioregUtilRe = regexp.MustCompile(`"Device Utilization %"=(\d+)`)
	ioregUsedRe = regexp.MustCompile(`"In use system memory"=(\d+)`)
)

// parseIORegGPU reads the GPU's busy percent and the memory it holds from the
// PerformanceStatistics of the first accelerator in
// `ioreg -r -d 1 -w 0 -c IOAccelerator` (no sudo needed). On Apple Silicon the
// memory is unified RAM the GPU has in use. ok is false when the output has no
// utilisation figure, so nothing is reported rather than a false 0%.
func parseIORegGPU(out string) (util int, used uint64, ok bool) {
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, `"PerformanceStatistics"`) {
			continue
		}
		m := ioregUtilRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		util, _ = strconv.Atoi(m[1])
		util = min(max(util, 0), 100)
		if u := ioregUsedRe.FindStringSubmatch(line); u != nil {
			used, _ = strconv.ParseUint(u[1], 10, 64)
		}
		return util, used, true
	}
	return 0, 0, false
}
