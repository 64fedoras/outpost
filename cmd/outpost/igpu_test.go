package main

import "testing"

func TestIsLinuxIntegrated(t *testing.T) {
	for _, c := range []struct {
		vendor   uint64
		discrete bool
		want     bool
	}{
		{0x8086, false, true},  // Intel iGPU
		{0x1002, false, true},  // AMD APU
		{0x1002, true, false},  // discrete Radeon
		{0x10de, false, false}, // NVIDIA is never integrated
		{0x1af4, false, false}, // virtio display adapter
		{0x8086, true, false},  // Intel Arc
	} {
		if got := isLinuxIntegrated(c.vendor, c.discrete); got != c.want {
			t.Errorf("%#x/%v: got %v", c.vendor, c.discrete, got)
		}
	}
}

// A desktop Ryzen's iGPU (2 GB carve-out) next to a 16 GB Radeon: the Radeon is the discrete
// GPU and the iGPU is left for linuxIntegratedGPUs, so it is listed (found on a real node).
func TestPickDiscreteDesktopRyzen(t *testing.T) {
	all := []sysGPU{{"igpu", 0x1002, 2 << 30}, {"dgpu", 0x1002, 16 << 30}, {"virt", 0x1af4, 0}}
	got := pickDiscrete(all)
	if len(got) != 1 || got[0] != "dgpu" {
		t.Fatalf("discrete = %v", got)
	}
	if !isLinuxIntegrated(0x1002, false) {
		t.Error("the left-over AMD device must count as integrated")
	}
	// Without the big card, a 2 GB AMD device stays a small discrete GPU (known gap) and a 512 MB one is integrated.
	if got := pickDiscrete([]sysGPU{{"a", 0x1002, 2 << 30}, {"b", 0x8086, 0}}); len(got) != 1 || got[0] != "a" {
		t.Errorf("no big card: %v", got)
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
