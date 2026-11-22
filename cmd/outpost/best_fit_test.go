package main

import (
	"strings"
	"testing"
)

const tgb = uint64(1) << 30

func bestFor(t *testing.T, h *Hello, prep func(c *Controller)) LoadOption {
	t.Helper()
	c := loadOptsCtl(map[string]*Hello{"n": h})
	c.lastHB = map[string]*Heartbeat{}
	c.catShapes = fakeCatalogShapes()
	if prep != nil {
		prep(c)
	}
	opts, _ := c.loadOptionsFor("n", 3)
	if len(opts) == 0 || opts[0].Rank != 1 {
		t.Fatalf("no pick: %+v", opts)
	}
	return opts[0]
}

func gamingNode() *Hello {
	return &Hello{OS: "windows", Arch: "amd64", MemBWGBs: 60, RAMBytes: 64 * tgb, ModelsFreeBytes: 500 * tgb, GPUs: []GPUInfo{{Vendor: "amd", VRAMBytes: 16 * tgb}}}
}

// A sparse model split over VRAM and RAM beats a smaller one that fits VRAM,
// but only when its header shows it is sparse: unknown models count as dense.
func TestBestFitSplit(t *testing.T) {
	o := bestFor(t, gamingNode(), nil)
	if o.Name != "qwen3-coder-next-q4km" || o.Fit != fitSplit || o.EstTPS < speedGood || o.SpeedSrc != "split" || o.SpeedNote == "" {
		t.Errorf("gaming node: got %s fit %s %.1f tok/s (%s)", o.Name, o.Fit, o.EstTPS, o.SpeedSrc)
	}
	o = bestFor(t, gamingNode(), func(c *Controller) { c.catShapes = nil })
	if fakeCatalogShapes()[o.Name] != nil || (o.Fit != fitVRAM && o.EstTPS < speedGood) {
		t.Errorf("without headers: got %s fit %s %.1f tok/s; a sparse model was trusted without its header", o.Name, o.Fit, o.EstTPS)
	}
	// A benchmark of the split model on this node replaces the estimate.
	o = bestFor(t, gamingNode(), func(c *Controller) {
		c.nodes["n"].Bench = &NodeBench{Model: "qwen3-coder-next-q4km", GenTPS: 1.5}
	})
	if o.Name == "qwen3-coder-next-q4km" {
		t.Errorf("a split model measured at 1.5 tok/s is still the top pick")
	}
}

// A weak GPU must not make the pick worse than the same machine without one.
func TestBestFitSmallGPU(t *testing.T) {
	cpu := &Hello{OS: "linux", Arch: "amd64", MemBWGBs: 40, RAMBytes: 128 * tgb, ModelsFreeBytes: 500 * tgb}
	for _, v := range []uint64{2, 4, 8} {
		g := *cpu
		g.VulkanAvailable, g.GPUs = true, []GPUInfo{{Vendor: "nvidia", VRAMBytes: v * tgb}}
		a, b := bestFor(t, cpu, nil), bestFor(t, &g, nil)
		if b.Bytes < a.Bytes {
			t.Errorf("%d GB GPU: picked %s, smaller than the CPU-only pick %s", v, b.Name, a.Name)
		}
		if b.Fit == fitSplit && b.EstTPS < a.EstTPS {
			t.Errorf("%d GB GPU: split estimate %.1f below the CPU-only estimate %.1f", v, b.EstTPS, a.EstTPS)
		}
	}
}

func TestBestFitStorageLimit(t *testing.T) {
	use := func(total int64) *Heartbeat { return &Heartbeat{Storage: &StorageUse{Total: total}} }
	// 10 GB limit, 8 GB used, set to refuse: only a model of 2 GB or less may be picked.
	o := bestFor(t, gamingNode(), func(c *Controller) {
		c.nodes["n"].StorageLimitGB, c.nodes["n"].StorageFull = 10, "refuse"
		c.lastHB["n"] = use(8 << 30)
	})
	if o.Bytes > 2<<30 || o.Evicts {
		t.Errorf("refuse: picked %s (%d bytes, evicts %v)", o.Name, o.Bytes, o.Evicts)
	}
	// Same node set to remove old models, with a restorable 7 GB model on it: 9 GB of room.
	o = bestFor(t, gamingNode(), func(c *Controller) {
		n := c.nodes["n"]
		n.StorageLimitGB = 10
		n.Hello.Models = []ModelInfo{{Name: "old", Bytes: 7 << 30}}
		c.models["old"] = &ModelRec{Name: "old", Status: "ready", BytesTotal: 7 << 30, Files: []ModelFile{{}}}
		c.lastHB["n"] = use(8 << 30)
	})
	if o.Name != "qwen2.5-coder-14b-q4km" || !o.Evicts || !strings.Contains(o.Reason, "storage limit") {
		t.Errorf("remove old: picked %s evicts %v reason %q", o.Name, o.Evicts, o.Reason)
	}
	// A model already on the node needs no room.
	o = bestFor(t, gamingNode(), func(c *Controller) {
		n := c.nodes["n"]
		n.StorageLimitGB, n.StorageFull = 10, "refuse"
		n.Hello.Models = []ModelInfo{{Name: "qwen2.5-coder-14b-q4km", Bytes: 9 << 30}}
		c.lastHB["n"] = use(10 << 30)
	})
	if o.Name != "qwen2.5-coder-14b-q4km" || o.Evicts {
		t.Errorf("on node: picked %s evicts %v", o.Name, o.Evicts)
	}
}

func TestBestFitFreeMemory(t *testing.T) {
	// Another program holds 8 GB of the 16 GB of VRAM: nothing may claim more than is left.
	o := bestFor(t, gamingNode(), func(c *Controller) {
		c.catShapes = nil
		c.lastHB["n"] = &Heartbeat{VRAMUsedBytes: []uint64{8 * tgb}, RAMUsedBytes: 6 * tgb}
	})
	if o.Fit == fitVRAM && o.Need > int64(8*tgb) {
		t.Errorf("picked %s needing %d as all-VRAM with 8 GB taken", o.Name, o.Need)
	}
	if !strings.Contains(o.Reason, "other programs hold") {
		t.Errorf("reason does not mention other programs: %q", o.Reason)
	}
	// The node's own loaded model is not "another program": the pick does not change while it is loaded.
	base := bestFor(t, gamingNode(), func(c *Controller) { c.catShapes = nil })
	o = bestFor(t, gamingNode(), func(c *Controller) {
		c.catShapes = nil
		c.lastHB["n"] = &Heartbeat{LoadedModel: base.Name, VRAMUsedBytes: []uint64{uint64(base.Need)}, RAMUsedBytes: 6 * tgb}
	})
	if o.Name != base.Name || o.Fit != base.Fit || strings.Contains(o.Reason, "other programs") {
		t.Errorf("own model loaded: got %s fit %s (%q), want %s", o.Name, o.Fit, o.Reason, base.Name)
	}
	// Other programs fill the RAM of a CPU node: a model that no longer fits is not picked.
	cpu := &Hello{OS: "linux", Arch: "amd64", MemBWGBs: 40, RAMBytes: 32 * tgb, ModelsFreeBytes: 500 * tgb}
	o = bestFor(t, cpu, func(c *Controller) { c.lastHB["n"] = &Heartbeat{RAMUsedBytes: 26 * tgb} })
	if o.Need > int64(5*tgb) {
		t.Errorf("CPU node with 26 of 32 GB in use: picked %s needing %d", o.Name, o.Need)
	}
}

func TestBestFitAppleSilicon(t *testing.T) {
	m := &Hello{OS: "darwin", Arch: "arm64", MemBWGBs: 60, RAMBytes: 16 * tgb, ModelsFreeBytes: 500 * tgb, GPUs: []GPUInfo{{Vendor: "apple", VRAMBytes: 16 * tgb}}}
	o := bestFor(t, m, nil)
	if !o.Unified || o.SpeedSrc != "apple" || o.Name != "qwen2.5-coder-7b-q4km" || !strings.Contains(o.SpeedNote, "guess") {
		t.Errorf("M-series 16 GB: got %s unified %v src %s note %q", o.Name, o.Unified, o.SpeedSrc, o.SpeedNote)
	}
	// A benchmark on the Mac is used as it is, without the factor.
	o = bestFor(t, m, func(c *Controller) { c.nodes["n"].Bench = &NodeBench{Model: "qwen2.5-coder-7b-q4km", GenTPS: 30} })
	if o.SpeedSrc == "apple" {
		t.Errorf("benchmarked Mac still shows a guess for %s", o.Name)
	}
	intel := &Hello{OS: "darwin", Arch: "amd64", MemBWGBs: 20, RAMBytes: 16 * tgb, ModelsFreeBytes: 500 * tgb}
	if o := bestFor(t, intel, nil); o.Unified || o.SpeedSrc == "apple" {
		t.Errorf("Intel Mac treated as Apple Silicon: %+v", o)
	}
}

// A higher target never picks a bigger model, a lower one never a smaller one,
// and a target nothing reaches falls back to the fastest model.
func TestBestFitTargetSpeed(t *testing.T) {
	cpu := &Hello{OS: "linux", Arch: "amd64", MemBWGBs: 40, RAMBytes: 128 * tgb, ModelsFreeBytes: 500 * tgb}
	pick := func(target float64) LoadOption {
		return bestFor(t, cpu, func(c *Controller) { c.nodes["n"].TargetTPS = target })
	}
	def, low, high, huge := pick(0), pick(2), pick(11), pick(100)
	if pick(8).Name != def.Name {
		t.Errorf("target 8 differs from the default: %s vs %s", pick(8).Name, def.Name)
	}
	if low.Bytes < def.Bytes || high.Bytes >= def.Bytes {
		t.Errorf("sizes by target: 2=%d default=%d 11=%d", low.Bytes, def.Bytes, high.Bytes)
	}
	if high.EstTPS < 11 {
		t.Errorf("target 11 picked %s at %.1f tok/s", high.Name, high.EstTPS)
	}
	if huge.EstTPS < high.EstTPS {
		t.Errorf("unreachable target did not fall back to the fastest: %.1f < %.1f", huge.EstTPS, high.EstTPS)
	}
	// The request override wins over the saved value.
	c := loadOptsCtl(map[string]*Hello{"n": cpu})
	c.lastHB, c.catShapes = map[string]*Heartbeat{}, fakeCatalogShapes()
	c.nodes["n"].TargetTPS = 2
	opts, _ := c.loadOptionsTarget("n", 3, 11)
	if len(opts) == 0 || opts[0].EstTPS < 11 {
		t.Errorf("override ignored: %+v", opts[0])
	}
}

func TestSpeedTierTarget(t *testing.T) {
	for _, c := range []struct {
		tps, target float64
		want        int
	}{{9, 0, 0}, {5, 0, 1}, {3, 0, 2}, {20, 24, 1}, {11, 24, 2}, {12, 24, 1}, {1, 1, 0}} {
		if got := speedTier(c.tps, c.target); got != c.want {
			t.Errorf("speedTier(%v, %v) = %d, want %d", c.tps, c.target, got, c.want)
		}
	}
}

// A CPU-only node whose models all run under 5 tok/s gets no recommendation, unless
// the owner lowered the node's target below that.
func TestBestFitMinimumSpeed(t *testing.T) {
	slow := &Hello{OS: "linux", Arch: "amd64", MemBWGBs: 6, RAMBytes: 128 * tgb, ModelsFreeBytes: 500 * tgb}
	c := loadOptsCtl(map[string]*Hello{"n": slow})
	c.lastHB, c.catShapes = map[string]*Heartbeat{}, fakeCatalogShapes()
	opts, _ := c.loadOptionsFor("n", 3)
	for _, o := range opts {
		if o.Rank > 0 {
			t.Fatalf("recommended %s at %.1f tok/s", o.Name, o.EstTPS)
		}
	}
	c.nodes["n"].TargetTPS = 1
	opts, _ = c.loadOptionsFor("n", 3)
	if len(opts) == 0 || opts[0].Rank != 1 {
		t.Errorf("target 1 should recommend something: %+v", opts)
	}
}
