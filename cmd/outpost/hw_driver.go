package main

import (
	"path/filepath"
	"runtime"
	"strings"
)

// vulkanICDDirs are where Linux Vulkan drivers register themselves (one *.json file each).
var vulkanICDDirs = []string{"/usr/share/vulkan/icd.d", "/etc/vulkan/icd.d", "/usr/local/share/vulkan/icd.d", "/usr/local/etc/vulkan/icd.d"}

// icdFileNames lists the installed Vulkan driver files, lower-cased.
func icdFileNames() []string {
	var out []string
	for _, d := range vulkanICDDirs {
		files, _ := filepath.Glob(filepath.Join(d, "*.json"))
		for _, f := range files {
			out = append(out, strings.ToLower(filepath.Base(f)))
		}
	}
	return out
}

// vendorHasICD says whether one of the driver files belongs to the GPU maker (Mesa's RADV is
// radeon_icd*.json, AMDVLK amd_icd*.json, Mesa's Intel driver intel_icd*.json, NVIDIA nvidia_icd.json).
func vendorHasICD(vendor string, icds []string) bool {
	var keys []string
	switch vendor {
	case "amd":
		keys = []string{"radeon", "amd"}
	case "intel":
		keys = []string{"intel"}
	case "nvidia":
		keys = []string{"nvidia"}
	default:
		return true // unknown maker: say nothing
	}
	for _, f := range icds {
		for _, k := range keys {
			if strings.Contains(f, k) {
				return true
			}
		}
	}
	return false
}

// gpusWithoutVulkanDriver returns the GPUs that llama.cpp would reach through Vulkan but whose
// maker has no Vulkan driver installed, so they sit unused. Integrated GPUs count even when a
// discrete card exists. Only meaningful on Linux (Windows graphics drivers include Vulkan).
func gpusWithoutVulkanDriver(gpus, integrated []GPUInfo, icds []string) []GPUInfo {
	out := []GPUInfo{}
	for _, g := range append(append([]GPUInfo{}, gpus...), integrated...) {
		if g.Backend != "vulkan" || vendorHasICD(g.Vendor, icds) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// detectMissingDrivers lists GPUs this machine has but cannot use for lack of a Vulkan driver.
// It stays quiet when the Vulkan loader itself is missing: the node card already says that.
func detectMissingDrivers(hw Hardware) []GPUInfo {
	if runtime.GOOS != "linux" || !vulkanAvailable() {
		return nil
	}
	return gpusWithoutVulkanDriver(hw.GPUs, hw.Integrated, icdFileNames())
}
