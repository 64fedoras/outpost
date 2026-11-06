package main

import (
	"strings"
	"testing"
)

func loadOptsCtl(nodes map[string]*Hello) *Controller {
	c := &Controller{conns: map[string]*nodeConn{}, nodes: map[string]*NodeRec{}, models: map[string]*ModelRec{}, pipelines: map[string]*pipeline{}}
	for id, h := range nodes {
		c.nodes[id] = &NodeRec{ID: id, Name: id, Hello: h}
		c.conns[id] = &nodeConn{}
	}
	return c
}

func TestLoadOptionsRecommendation(t *testing.T) {
	const gb = uint64(1) << 30
	c := loadOptsCtl(map[string]*Hello{
		"gpu":    {OS: "windows", Arch: "amd64", MemBWGBs: 40, RAMBytes: 32 * gb, ModelsFreeBytes: 500 * gb, GPUs: []GPUInfo{{Vendor: "amd", VRAMBytes: 16 * gb}}},
		"novk":   {OS: "linux", Arch: "amd64", MemBWGBs: 40, RAMBytes: 32 * gb, ModelsFreeBytes: 500 * gb, GPUs: []GPUInfo{{Vendor: "amd", VRAMBytes: 16 * gb}}},
		"tiny":   {OS: "linux", Arch: "amd64", MemBWGBs: 40, RAMBytes: 4 * gb, ModelsFreeBytes: 500 * gb},
		"bigcpu": {OS: "linux", Arch: "amd64", MemBWGBs: 40, RAMBytes: 173 * gb, ModelsFreeBytes: 500 * gb},
		"nodisk": {OS: "linux", Arch: "amd64", MemBWGBs: 40, RAMBytes: 173 * gb, ModelsFreeBytes: 3 * gb},
		"weird":  {OS: "plan9", Arch: "amd64", RAMBytes: 173 * gb},
	})
	c.catShapes = fakeCatalogShapes()
	// A custom (non-catalog) library model is listed but never recommended.
	c.models["custom"] = &ModelRec{Name: "custom", Label: "custom", Status: "ready", BytesTotal: 1 << 30, Ctx: 8192}
	want := map[string]string{
		"gpu":    "qwen2.5-coder-14b-q4km",   // fits VRAM; beats bigger models that only split
		"novk":   "qwen3.6-35b-a3b-q4km", // CPU build: VRAM is not counted; the biggest model that is fast enough (its header shows 3B active)
		"tiny":   "qwen2.5-coder-3b-q4km",    // the only coding model that fits 4 GB
		"bigcpu": "qwen3-coder-next-q4km",
		"nodisk": "qwen2.5-coder-3b-q4km", // others need more than 3 GB of disk
	}
	for id, name := range want {
		opts, ok := c.loadOptionsFor(id, 3)
		if !ok || len(opts) == 0 || opts[0].Rank != 1 || opts[0].Name != name {
			t.Errorf("%s: got %+v want top %s", id, opts, name)
		}
		for _, o := range opts {
			if o.Rank > 0 && (o.Fit == fitNo || !o.Coding) {
				t.Errorf("%s: recommended %s with fit %s coding %v", id, o.Name, o.Fit, o.Coding)
			}
			if o.Name == "custom" && o.Rank != 0 {
				t.Errorf("custom model was recommended")
			}
		}
	}
	if opts, ok := c.loadOptionsFor("weird", 3); ok {
		for _, o := range opts {
			if o.Rank != 0 || o.Fit != fitNo {
				t.Errorf("node without a llama.cpp build got %+v", o)
			}
		}
	}
	// Library models are listed in the library group, catalog-only ones as catalog.
	opts, _ := c.loadOptionsFor("gpu", 3)
	groups := map[string]string{}
	for _, o := range opts {
		groups[o.Name] = o.Group
	}
	if groups["custom"] != "library" || groups["gemma-4-26b-a4b-q4km"] != "catalog" || groups["qwen2.5-coder-14b-q4km"] != "recommended" {
		t.Errorf("groups: %v", groups)
	}
}

// fakeCatalogShapes stands in for the headers read from Hugging Face: the
// mixture-of-experts models read only a few percent of their weights per token.
func fakeCatalogShapes() map[string]*ModelShape {
	return map[string]*ModelShape{
		"qwen3-coder-30b-a3b-q4km": {ActiveFrac: 0.11},
		"qwen3-coder-next-q4km":    {ActiveFrac: 0.04},
		"qwen3.6-35b-a3b-q4km":     {ActiveFrac: 0.09},
	}
}

func recName(opts []LoadOption) string {
	for _, o := range opts {
		if o.Rank == 1 {
			return o.Name
		}
	}
	return ""
}

func TestCPURecommendationFollowsSpeed(t *testing.T) {
	const gb = uint64(1) << 30
	c := loadOptsCtl(map[string]*Hello{
		"fast": {OS: "linux", Arch: "amd64", MemBWGBs: 40, RAMBytes: 128 * gb, ModelsFreeBytes: 500 * gb},
		"slow": {OS: "linux", Arch: "amd64", MemBWGBs: 6, RAMBytes: 128 * gb, ModelsFreeBytes: 500 * gb},
		"mid":  {OS: "linux", Arch: "amd64", MemBWGBs: 25, RAMBytes: 128 * gb, ModelsFreeBytes: 500 * gb},
	})
	c.catShapes = fakeCatalogShapes()
	byName := func(id, name string) LoadOption {
		opts, _ := c.loadOptionsFor(id, 3)
		for _, o := range opts {
			if o.Name == name {
				return o
			}
		}
		t.Fatalf("%s missing", name)
		return LoadOption{}
	}
	// Plenty of bandwidth: the big mixture-of-experts model is fast enough.
	opts, _ := c.loadOptionsFor("fast", 3)
	if recName(opts) != "qwen3-coder-next-q4km" {
		t.Errorf("fast: %s", recName(opts))
	}
	// A 27B dense model reads its whole 17.7 GB per token and is slow even on the fast node.
	if o := byName("fast", "qwen3.6-27b-q4km"); !o.SpeedLow || o.EstTPS > 2 {
		t.Errorf("dense 27B should be slow: %+v", o)
	}
	// Little bandwidth: nothing reaches a usable speed, the fastest model wins and says so.
	opts, _ = c.loadOptionsFor("slow", 3)
	if recName(opts) != "qwen2.5-coder-3b-q4km" {
		t.Errorf("slow: %s", recName(opts))
	}
	o := opts[0]
	if !o.SpeedLow || o.EstTPS <= 0 || o.SpeedSrc != "bandwidth" || !strings.Contains(o.SpeedNote, "tok/s") || !strings.Contains(o.SpeedNote, "slow") {
		t.Errorf("slow pick lacks an honest speed note: %+v", o)
	}
	// Middle: usable (>= 4 tok/s) MoE beats the small dense model.
	opts, _ = c.loadOptionsFor("mid", 3)
	if o := opts[0]; o.Rank != 1 || o.SpeedLow || o.EstTPS < speedUsable {
		t.Errorf("mid: %+v", o)
	}
	// A measured benchmark of the 3B model that is half the estimate halves the others too.
	est := byName("mid", "qwen3-coder-30b-a3b-q4km").EstTPS
	b3 := byName("mid", "qwen2.5-coder-3b-q4km").EstTPS
	c.nodes["mid"].Bench = &NodeBench{Model: "qwen2.5-coder-3b-q4km", GenTPS: b3 / 2}
	if got := byName("mid", "qwen3-coder-30b-a3b-q4km"); got.SpeedSrc != "calibrated" || got.EstTPS < est/2*0.99 || got.EstTPS > est/2*1.01 {
		t.Errorf("calibration: est %.2f -> %+v", est, got)
	}
	if got := byName("mid", "qwen2.5-coder-3b-q4km"); got.SpeedSrc != "measured" || got.EstTPS != b3/2 {
		t.Errorf("measured: %+v", got)
	}
}
