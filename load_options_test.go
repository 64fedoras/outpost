package main

import "testing"

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
		"gpu":    {OS: "windows", Arch: "amd64", RAMBytes: 32 * gb, ModelsFreeBytes: 500 * gb, GPUs: []GPUInfo{{Vendor: "amd", VRAMBytes: 16 * gb}}},
		"novk":   {OS: "linux", Arch: "amd64", RAMBytes: 32 * gb, ModelsFreeBytes: 500 * gb, GPUs: []GPUInfo{{Vendor: "amd", VRAMBytes: 16 * gb}}},
		"tiny":   {OS: "linux", Arch: "amd64", RAMBytes: 4 * gb, ModelsFreeBytes: 500 * gb},
		"bigcpu": {OS: "linux", Arch: "amd64", RAMBytes: 173 * gb, ModelsFreeBytes: 500 * gb},
		"nodisk": {OS: "linux", Arch: "amd64", RAMBytes: 173 * gb, ModelsFreeBytes: 3 * gb},
		"weird":  {OS: "plan9", Arch: "amd64", RAMBytes: 173 * gb},
	})
	// A custom (non-catalog) library model is listed but never recommended.
	c.models["custom"] = &ModelRec{Name: "custom", Label: "custom", Status: "ready", BytesTotal: 1 << 30, Ctx: 8192}
	want := map[string]string{
		"gpu":    "qwen2.5-coder-14b-q4km",   // fits VRAM; beats bigger models that only split
		"novk":   "qwen3-coder-30b-a3b-q4km", // CPU build: VRAM is not counted, so RAM-only ranking applies
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
