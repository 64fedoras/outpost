package main

import (
	"os"
	"runtime"
	"strings"
)

// cpuLacksAVX2 reports whether this is an x86-64 CPU that is known to lack AVX2 (for
// example Ivy Bridge Xeons in a 2013 Mac Pro). Unknown (including Windows) counts as "has
// it", so nothing changes for machines that cannot be checked.
func cpuLacksAVX2() bool {
	if runtime.GOARCH != "amd64" {
		return false
	}
	switch runtime.GOOS {
	case "linux":
		b, err := os.ReadFile("/proc/cpuinfo")
		if err != nil {
			return false
		}
		return flagsLackAVX2(string(b), "flags")
	case "darwin":
		out := cmdOut("sysctl", "-n", "machdep.cpu.leaf7_features")
		feat := cmdOut("sysctl", "-n", "machdep.cpu.features")
		if strings.TrimSpace(feat) == "" {
			return false // could not read the CPU flags
		}
		return !strings.Contains(" "+strings.ToUpper(out)+" ", " AVX2 ")
	}
	return false
}

// flagsLackAVX2 reads /proc/cpuinfo text: true when it has a flags line and avx2 is not on it.
func flagsLackAVX2(cpuinfo, key string) bool {
	for _, line := range strings.Split(cpuinfo, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		return !strings.Contains(" "+v+" ", " avx2 ")
	}
	return false
}
