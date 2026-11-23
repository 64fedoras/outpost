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

func TestNodeVariantMetalOptIn(t *testing.T) {
	h := &Hello{OS: "darwin", Arch: "amd64", NoAVX2: true, GPUs: []GPUInfo{{Vendor: "amd", Name: "D700", VRAMBytes: 6 << 30}}}
	if v, _ := nodeVariant(h); v != "macos-x64-compat" {
		t.Errorf("without opt-in: %s", v)
	}
	h.MetalOptIn = true
	if v, _ := nodeVariant(h); v != "macos-x64-compat-metal" {
		t.Errorf("with opt-in: %s", v)
	}
	h.GPUs = nil
	if v, _ := nodeVariant(h); v != "macos-x64-compat" {
		t.Errorf("opt-in but no GPU: %s", v)
	}
	if runtimeAssets["macos-x64-compat-metal"] == runtimeAssets["macos-x64-compat"] {
		t.Error("asset suffixes must differ")
	}
}

func TestParseMacGPUsText(t *testing.T) {
	txt := `Graphics/Displays:

    AMD FirePro D700:

      Chipset Model: AMD FirePro D700
      Type: GPU
      VRAM (Total): 6 GB
      Vendor: AMD (0x1002)
      Metal Support: Metal 2

    AMD FirePro D700:

      Chipset Model: AMD FirePro D700
      VRAM (Total): 6 GB
      Vendor: AMD (0x1002)
`
	g := parseMacGPUsText(txt)
	if len(g) != 2 || g[0].Vendor != "amd" || g[1].VRAMBytes != 6<<30 || g[0].Name != "AMD FirePro D700" {
		t.Fatalf("got %+v", g)
	}
}
