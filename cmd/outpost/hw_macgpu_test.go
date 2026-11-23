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
