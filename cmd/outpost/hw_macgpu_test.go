package main

import "testing"

func TestParseMacGPUs(t *testing.T) {
	js := `{"SPDisplaysDataType":[
	{"sppci_model":"AMD FirePro D700","spdisplays_vendor":"sppci_vendor_amd","spdisplays_vram":"6 GB"},
	{"sppci_model":"AMD FirePro D700","spdisplays_vendor":"sppci_vendor_amd","spdisplays_vram":"6 GB"},
	{"sppci_model":"Intel Iris","spdisplays_vendor":"sppci_vendor_intel","spdisplays_vram":"1536 MB"},
	{"sppci_model":"Tiny","spdisplays_vram":"512 MB"}]}`
	g := parseMacGPUs([]byte(js))
	if len(g) != 2 {
		t.Fatalf("got %d GPUs: %+v", len(g), g)
	}
	if g[0].Vendor != "amd" || g[0].VRAMBytes != 6<<30 || g[0].Backend != "" || g[1].Name != "AMD FirePro D700" {
		t.Errorf("bad GPU: %+v", g[0])
	}
	if len(parseMacGPUs([]byte("garbage"))) != 0 {
		t.Error("garbage should give none")
	}
}

func TestNodeVariantIntelMacGPU(t *testing.T) {
	h := &Hello{OS: "darwin", Arch: "amd64", NoAVX2: true, GPUs: []GPUInfo{{Vendor: "amd", Name: "D700", VRAMBytes: 6 << 30}}}
	if v, _ := nodeVariant(h); v != "macos-x64-compat-metal" {
		t.Errorf("with a GPU: %s", v)
	}
	h.MetalFailed = "did not start"
	if v, _ := nodeVariant(h); v != "macos-x64-compat" {
		t.Errorf("after the Metal build failed: %s", v)
	}
	h.MetalFailed, h.GPUs = "", nil
	if v, _ := nodeVariant(h); v != "macos-x64-compat" {
		t.Errorf("no GPU: %s", v)
	}
	if runtimeAssets["macos-x64-compat-metal"] == runtimeAssets["macos-x64-compat"] {
		t.Error("asset suffixes must differ")
	}
}

func TestLoadableMemoryFollowsGPUCheck(t *testing.T) {
	mac := &Hello{OS: "darwin", Arch: "amd64", NoAVX2: true, RAMBytes: 32 << 30,
		GPUs: []GPUInfo{{Vendor: "amd", VRAMBytes: 6 << 30}, {Vendor: "amd", VRAMBytes: 6 << 30}}}
	if v, _, _ := loadableMemory(mac); v != 0 {
		t.Errorf("Mac before a check: vram %d", v)
	}
	mac.GPUCheck = &GPUCheck{GPUTPS: 3.9, CPUTPS: 22.5, Slower: true}
	if v, _, _ := loadableMemory(mac); v != 0 {
		t.Errorf("Mac with a slower GPU: vram %d", v)
	}
	mac.GPUCheck = &GPUCheck{GPUTPS: 60, CPUTPS: 20}
	if v, _, _ := loadableMemory(mac); v != 6<<30 {
		t.Errorf("Mac with a faster GPU counts one card: vram %d", v)
	}
	lin := &Hello{OS: "linux", Arch: "amd64", VulkanAvailable: true, RAMBytes: 64 << 30, GPUs: []GPUInfo{{Vendor: "nvidia", VRAMBytes: 8 << 30}}}
	if v, _, _ := loadableMemory(lin); v != 8<<30 {
		t.Errorf("Linux Vulkan before a check keeps its VRAM: %d", v)
	}
	lin.GPUCheck = &GPUCheck{GPUTPS: 2, CPUTPS: 10, Slower: true}
	if v, _, _ := loadableMemory(lin); v != 0 {
		t.Errorf("Linux with a slower GPU: %d", v)
	}
}
