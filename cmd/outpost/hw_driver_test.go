package main

import "testing"

func TestGPUsWithoutVulkanDriver(t *testing.T) {
	ig := []GPUInfo{{Vendor: "amd", Name: "Granite Ridge", Backend: "vulkan", Integrated: true}}
	dgpu := []GPUInfo{{Vendor: "amd", Name: "9070 XT", Backend: "vulkan"}}
	if got := gpusWithoutVulkanDriver(dgpu, ig, nil); len(got) != 2 {
		t.Errorf("no drivers: %+v", got)
	}
	if got := gpusWithoutVulkanDriver(dgpu, ig, []string{"radeon_icd.x86_64.json"}); len(got) != 0 {
		t.Errorf("RADV installed: %+v", got)
	}
	if got := gpusWithoutVulkanDriver(nil, []GPUInfo{{Vendor: "intel", Backend: "vulkan", Integrated: true}}, []string{"radeon_icd.json"}); len(got) != 1 {
		t.Errorf("intel needs its own driver: %+v", got)
	}
	if got := gpusWithoutVulkanDriver([]GPUInfo{{Vendor: "amd", Backend: "rocm"}, {Vendor: "apple"}}, nil, nil); len(got) != 0 {
		t.Errorf("rocm and apple are not Vulkan: %+v", got)
	}
}
