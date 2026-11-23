package main

import (
	"strconv"
	"strings"
)

// nvidiaStat is one row of nvidia-smi's query output. NVIDIA's Linux driver
// exposes no memory, load or temperature in sysfs, so these come from the
// nvidia-smi tool when it is installed (it ships with the driver).
type nvidiaStat struct {
	Slot    string // PCI slot like 0000:01:00.0, as in the sysfs device link
	TotalB  uint64
	UsedB   uint64
	UtilPct int
	TempC   int
}

// parseNvidiaSMI parses the output of
// nvidia-smi --query-gpu=pci.bus_id,memory.total,memory.used,utilization.gpu,temperature.gpu --format=csv,noheader,nounits
// ("[N/A]" and unparsable fields count as 0).
func parseNvidiaSMI(out string) []nvidiaStat {
	var res []nvidiaStat
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(line), ",")
		if len(f) < 5 {
			continue
		}
		num := func(s string) uint64 {
			v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if v < 0 {
				return 0
			}
			return uint64(v)
		}
		slot := strings.ToLower(strings.TrimSpace(f[0]))
		// nvidia-smi prints an 8-digit PCI domain ("00000000:01:00.0"); sysfs uses 4.
		if i := strings.Index(slot, ":"); i > 4 {
			slot = slot[i-4:]
		}
		res = append(res, nvidiaStat{
			Slot: slot, TotalB: num(f[1]) << 20, UsedB: num(f[2]) << 20,
			UtilPct: int(num(f[3])), TempC: int(num(f[4])),
		})
	}
	return res
}

// nvidiaSMI runs nvidia-smi and returns its rows keyed by PCI slot; nil when
// the tool is missing or fails (the node then falls back to Vulkan).
func nvidiaSMI() map[string]nvidiaStat {
	out := cmdOut("nvidia-smi", "--query-gpu=pci.bus_id,memory.total,memory.used,utilization.gpu,temperature.gpu", "--format=csv,noheader,nounits")
	rows := parseNvidiaSMI(out)
	if len(rows) == 0 {
		return nil
	}
	m := map[string]nvidiaStat{}
	for _, r := range rows {
		m[r.Slot] = r
	}
	return m
}

// parseVulkanHeap returns the size of the largest device-local memory heap in
// `vulkaninfo` output, or 0 if none is found. Used as the VRAM total of an
// NVIDIA card when nvidia-smi is not installed.
func parseVulkanHeap(out string) uint64 {
	var best, size uint64
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "size") && strings.Contains(l, "=") {
			rest := strings.TrimSpace(l[strings.Index(l, "=")+1:])
			if i := strings.IndexAny(rest, " ("); i > 0 {
				rest = rest[:i]
			}
			size, _ = strconv.ParseUint(rest, 10, 64)
		}
		if strings.Contains(l, "MEMORY_HEAP_DEVICE_LOCAL_BIT") && size > best {
			best = size
		}
	}
	return best
}

// nvidiaVRAMTotal is the VRAM of the NVIDIA card in sysfs dir dev: nvidia-smi
// if present, else the Vulkan heap size when there is a single NVIDIA card.
func nvidiaVRAMTotal(slot string, smi map[string]nvidiaStat, single bool) uint64 {
	if r, ok := smi[slot]; ok && r.TotalB > 0 {
		return r.TotalB
	}
	if single {
		return parseVulkanHeap(cmdOut("vulkaninfo"))
	}
	return 0
}
