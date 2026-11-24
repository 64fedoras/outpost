package main

import "testing"

func TestIsLinuxIntegrated(t *testing.T) {
	for _, c := range []struct {
		vendor, vram uint64
		want         bool
	}{
		{0x8086, 0, true},         // Intel iGPU reports no dedicated memory
		{0x1002, 512 << 20, true}, // AMD APU with a small carve-out
		{0x1002, 8 << 30, false},  // discrete Radeon
		{0x10de, 0, false},        // NVIDIA is never integrated
		{0x1af4, 0, false},        // virtio display adapter
		{0x8086, 12 << 30, false}, // Intel Arc
	} {
		if got := isLinuxIntegrated(c.vendor, c.vram); got != c.want {
			t.Errorf("%#x/%d: got %v", c.vendor, c.vram, got)
		}
	}
}

func TestCheckGPUsPrefersDiscrete(t *testing.T) {
	d := GPUInfo{Vendor: "amd", Name: "RX", VRAMBytes: 8 << 30}
	i := GPUInfo{Vendor: "intel", Name: "UHD", Integrated: true}
	if got := (Hardware{GPUs: []GPUInfo{d}, Integrated: []GPUInfo{i}}).checkGPUs(); len(got) != 1 || got[0].Integrated {
		t.Errorf("discrete present: %+v", got)
	}
	if got := (Hardware{Integrated: []GPUInfo{i}}).checkGPUs(); len(got) != 1 || !got[0].Integrated {
		t.Errorf("integrated only: %+v", got)
	}
}

func TestGPUCheckLimit(t *testing.T) {
	if got := gpuCheckLimit([]GPUInfo{{Vendor: "amd", VRAMBytes: 10 << 30}}, 64<<30); got != 6<<30 {
		t.Errorf("discrete limit = %d", got)
	}
	if got := gpuCheckLimit([]GPUInfo{{Vendor: "intel", Integrated: true}}, 16<<30); got != 4<<30 {
		t.Errorf("integrated limit = %d", got)
	}
	if !integratedOnly([]GPUInfo{{Integrated: true}}) || integratedOnly(nil) || integratedOnly([]GPUInfo{{Integrated: true}, {}}) {
		t.Error("integratedOnly")
	}
}

func TestNodeVariantIntegratedGPU(t *testing.T) {
	igpu := []GPUInfo{{Vendor: "intel", Name: "UHD 770", Integrated: true}}
	if v, _ := nodeVariant(&Hello{OS: "linux", Arch: "amd64", IntegratedGPUs: igpu, VulkanAvailable: true}); v != "linux-x64-vulkan" {
		t.Errorf("linux iGPU with Vulkan: %s", v)
	}
	if v, note := nodeVariant(&Hello{OS: "linux", Arch: "amd64", IntegratedGPUs: igpu}); v != "linux-x64-cpu" || note == "" {
		t.Errorf("linux iGPU without Vulkan: %s %q", v, note)
	}
	if v, _ := nodeVariant(&Hello{OS: "windows", Arch: "amd64", IntegratedGPUs: igpu}); v != "win-x64-vulkan" {
		t.Errorf("windows iGPU: %s", v)
	}
	if v, _ := nodeVariant(&Hello{OS: "linux", Arch: "amd64", VulkanAvailable: true}); v != "linux-x64-cpu" {
		t.Errorf("no GPU: %s", v)
	}
}

func TestIntegratedGPUNeverCountsAsVRAM(t *testing.T) {
	h := &Hello{OS: "linux", Arch: "amd64", RAMBytes: 32 << 30, VulkanAvailable: true,
		IntegratedGPUs: []GPUInfo{{Vendor: "amd", VRAMBytes: 512 << 20, Integrated: true}}}
	vram, ram, ok := loadableMemory(h)
	if !ok || vram != 0 || ram != 32<<30 {
		t.Errorf("vram=%d ram=%d ok=%v", vram, ram, ok)
	}
}

func TestIntegratedSpeedup(t *testing.T) {
	ig := []GPUInfo{{Integrated: true}}
	cases := []struct {
		name string
		h    *Hello
		want float64
	}{
		{"faster", &Hello{IntegratedGPUs: ig, GPUCheck: &GPUCheck{GPUTPS: 12, CPUTPS: 8}}, 1.5},
		{"capped at 3", &Hello{IntegratedGPUs: ig, GPUCheck: &GPUCheck{GPUTPS: 90, CPUTPS: 8}}, 3},
		{"slower", &Hello{IntegratedGPUs: ig, GPUCheck: &GPUCheck{GPUTPS: 5, CPUTPS: 8, Slower: true}}, 1},
		{"not measured", &Hello{IntegratedGPUs: ig}, 1},
		{"discrete present", &Hello{GPUs: []GPUInfo{{Vendor: "amd"}}, IntegratedGPUs: ig, GPUCheck: &GPUCheck{GPUTPS: 12, CPUTPS: 8}}, 1},
	}
	for _, c := range cases {
		if got := integratedSpeedup(c.h); got != c.want {
			t.Errorf("%s: %v want %v", c.name, got, c.want)
		}
	}
}
