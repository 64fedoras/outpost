package main

import "testing"

func TestFitOnNode(t *testing.T) {
	const gb = int64(1) << 30
	need := estimateModelBytes(9*gb, 16384) // ~14B Q4
	cases := []struct {
		name      string
		vram, ram int64
		want      string
	}{
		{"16GB card", 16 * gb, 32 * gb, fitVRAM},
		{"8GB card spills", 8 * gb, 32 * gb, fitSplit},
		{"cpu only, big ram", 0, 173 * gb, fitRAM},
		{"4GB vm", 0, 4 * gb, fitNo},
		{"small card, small ram", 4 * gb, 4 * gb, fitNo},
	}
	for _, c := range cases {
		if got := fitOnNode(need, c.vram, c.ram); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestEstimateGrowsWithContext(t *testing.T) {
	w := int64(8) << 30
	if estimateModelBytes(w, 32768) <= estimateModelBytes(w, 4096) {
		t.Fatal("larger context should need more memory")
	}
	if estimateModelBytes(w, 4096) <= w {
		t.Fatal("estimate must exceed the weights")
	}
}

func TestNodeMemoryAppleIsRAMOnly(t *testing.T) {
	h := &Hello{RAMBytes: 32 << 30, GPUs: []GPUInfo{{Vendor: "apple", VRAMBytes: 24 << 30}, {Vendor: "amd", VRAMBytes: 16 << 30}}}
	v, r := nodeMemory(h)
	if v != 16<<30 || r != 32<<30 {
		t.Fatalf("got vram %d ram %d", v, r)
	}
}

func TestSuggestCoding(t *testing.T) {
	cat := []CatalogEntry{
		{ID: "small", Coding: true, Bytes: 2},
		{ID: "big", Coding: true, Bytes: 9},
		{ID: "huge", Coding: true, Bytes: 99},
		{ID: "chat", Coding: false, Bytes: 5},
		{ID: "slow", Coding: true, Bytes: 50},
	}
	fits := map[string][]NodeFit{
		"small": {{Fit: fitVRAM}}, "big": {{Fit: fitNo}, {Fit: fitVRAM}}, "huge": {{Fit: fitNo}},
		"chat": {{Fit: fitVRAM}}, "slow": {{Fit: fitSplit}},
	}
	got := suggestCoding(cat, fits, 0)
	want := []string{"big", "small", "slow"}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, w := range want {
		if got[i].ID != w {
			t.Fatalf("pos %d: got %s want %s", i, got[i].ID, w)
		}
	}
	if len(suggestCoding(cat, fits, 2)) != 2 {
		t.Fatal("limit not applied")
	}
}
