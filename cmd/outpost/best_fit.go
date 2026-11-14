package main

import (
	"fmt"
	"sort"
)

// The "best model for this node" ranking behind Recommended and Pick best.
//
// The pick is the biggest coding model that should still run fast on the node:
//
//   - A model that fits fully in GPU memory counts as fast.
//   - A model split between GPU memory and system RAM gets a speed estimate, so
//     a sparse (mixture-of-experts) model that reads little per token can beat a
//     smaller model that fits in VRAM, while a dense one that would crawl cannot.
//   - CPU-only nodes are ranked by the memory-bandwidth estimate (cpu_speed.go).
//   - Apple Silicon runs the model on its GPU from unified memory: the same
//     estimate with a factor, which is a guess until the node is benchmarked.
//
// Sizes are checked against what is free right now (other programs' memory is
// left alone) and against the node's storage limit, not only its free disk.

const (
	splitGPUSpeedup  = 8.0 // the part of a split model in VRAM is taken to be this much faster than the CPU part
	appleGPUFactor   = 1.5 // Apple Silicon GPU over the CPU bandwidth estimate; a guess, not measured
	otherMemHeadroom = 4   // percent of a memory kept free on top of what other programs hold
)

func isAppleSilicon(h *Hello) bool { return h != nil && h.OS == "darwin" && h.Arch == "arm64" }

// usableNow returns the GPU memory and system RAM a new model may take on a
// node right now: the usual share of the totals, less what programs other than
// the node's own loaded model are holding. otherV and otherR are those amounts
// (zero when the node has not reported, or its own share cannot be told apart).
// c.mu must be held.
func (c *Controller) usableNow(id string, n *NodeRec, opts []LoadOption, vram, ram int64) (uv, ur, otherV, otherR int64) {
	uv, ur = vram*92/100, ram*75/100
	hb := c.lastHB[id]
	if hb == nil || c.conns[id] == nil || hb.RPCEndpoint != "" {
		return uv, ur, 0, 0
	}
	var usedV int64
	if vram > 0 {
		for _, b := range hb.VRAMUsedBytes {
			usedV += int64(b)
		}
	}
	usedR := int64(hb.RAMUsedBytes)
	// Take the node's own model out of the figures: loading another one frees it.
	if hb.LoadedModel != "" && !hb.IdleUnloaded && !hb.GPUYielding {
		own := int64(-1)
		for i := range opts {
			if opts[i].Name != hb.LoadedModel {
				continue
			}
			own = opts[i].Need
			if lp := n.LoadPrefs; lp != nil && lp.Name == hb.LoadedModel && lp.Ctx > 0 && lp.Ctx != opts[i].Ctx {
				own = estimateModelBytes(opts[i].Bytes, lp.Ctx)
				if opts[i].shape != nil {
					own = opts[i].Bytes + opts[i].shape.KVBytes(lp.Ctx) + fitOverheadBytes
				}
			}
		}
		if own < 0 {
			return uv, ur, 0, 0 // an unknown model is loaded: its share cannot be told from other programs'
		}
		ownV := min(own, usedV)
		usedV -= ownV
		usedR -= min(own-ownV, usedR)
	}
	if left := vram - usedV - vram*otherMemHeadroom/100; vram > 0 && left < uv {
		uv, otherV = max(left, 0), usedV
	}
	if left := ram - usedR - ram*otherMemHeadroom/100; left < ur {
		ur, otherR = max(left, 0), usedR
	}
	return uv, ur, otherV, otherR
}

// storageRoom says how much a node may still store under its storage limit:
// room without removing anything, and extra that removing older models would
// free (zero when the node refuses instead). limited is false when the node has
// no limit or has not reported its use yet. c.mu must be held.
func (c *Controller) storageRoom(id string, n *NodeRec) (room, extra int64, limited bool) {
	hb := c.lastHB[id]
	if n.StorageLimitGB <= 0 || hb == nil || hb.Storage == nil {
		return 0, 0, false
	}
	room = max(int64(n.StorageLimitGB*(1<<30))-hb.Storage.Total, 0)
	if n.StorageFull != "refuse" {
		_, _, extra = c.reclaimableLocked(n, "")
	}
	return room, extra, true
}

// rankBestFit marks the best coding models for a node in opts (Rank, Group,
// Reason and the speed fields), best first. c.mu must be held.
func (c *Controller) rankBestFit(id string, n *NodeRec, opts []LoadOption, vram, ram, diskFree int64, maxRecommended int) {
	if n.Hello == nil {
		return
	}
	h := n.Hello
	uv, ur, otherV, otherR := c.usableNow(id, n, opts, vram, ram)
	room, extra, limited := c.storageRoom(id, n)
	apple := isAppleSilicon(h) && vram == 0
	var benchCost *modelCost
	if n.Bench != nil {
		for j := range opts {
			if opts[j].Name == n.Bench.Model {
				mc := costOf(opts[j].Bytes, opts[j].shape)
				benchCost = &mc
				break
			}
		}
	}
	var cand []int
	for i := range opts {
		o := &opts[i]
		if !o.Coding || o.Fit == fitNo {
			continue
		}
		fit := fitWith(o.Need, vram, uv, ur)
		if fit == fitNo {
			continue // fits the machine, but not beside what other programs hold right now
		}
		if !o.OnNode {
			if diskFree > 0 && o.Bytes > diskFree {
				continue // would not fit on the node's disk
			}
			if limited && o.Bytes > room+extra {
				continue // would pass the node's storage limit
			}
			o.Evicts = limited && o.Bytes > room
		}
		o.Fit = fit
		cost := costOf(o.Bytes, o.shape)
		switch fit {
		case fitRAM:
			sp := cpuSpeedFor(h, n.Bench, o.Name, cost, benchCost)
			if apple && (sp.Source == "estimate" || sp.Source == "bandwidth") {
				sp.TPS, sp.Source = sp.TPS*appleGPUFactor, "apple"
			}
			o.EstTPS, o.SpeedSrc, o.Unified = sp.TPS, sp.Source, apple
		case fitSplit:
			// The share in VRAM runs fast and the rest at the CPU's pace. A benchmark of
			// this very model on this node replaces the estimate.
			cpu := estimateCPUTokPerSec(h, cost)
			inV := float64(uv) / float64(o.Need)
			o.EstTPS, o.SpeedSrc = cpu/((1-inV)+inV/splitGPUSpeedup), "split"
			if b := n.Bench; b != nil && b.Model == o.Name && max(b.GenTPS, b.TokPerSec) > 0 {
				o.EstTPS, o.SpeedSrc = b.GenTPS, "measured"
				if o.EstTPS <= 0 {
					o.EstTPS = b.TokPerSec
				}
			}
		}
		o.SpeedLow = o.EstTPS > 0 && o.EstTPS < speedUsable
		cand = append(cand, i)
	}
	tier := func(o LoadOption) int {
		if o.Fit == fitVRAM || o.EstTPS <= 0 {
			return 0
		}
		return speedTier(o.EstTPS)
	}
	sort.SliceStable(cand, func(a, b int) bool {
		oa, ob := opts[cand[a]], opts[cand[b]]
		if ta, tb := tier(oa), tier(ob); ta != tb {
			return ta < tb
		}
		if tier(oa) == 2 && oa.EstTPS != ob.EstTPS { // nothing is usable: the fastest wins
			return oa.EstTPS > ob.EstTPS
		}
		if oa.Bytes != ob.Bytes {
			return oa.Bytes > ob.Bytes
		}
		if ra, rb := fitRank(oa.Fit), fitRank(ob.Fit); ra != rb {
			return ra < rb
		}
		return oa.Name < ob.Name
	})
	if maxRecommended > 0 && len(cand) > maxRecommended {
		cand = cand[:maxRecommended]
	}
	for r, i := range cand {
		o := &opts[i]
		o.Rank, o.Group = r+1, "recommended"
		o.Reason = bestFitReason(o, r == 0, vram, ram, uv, apple)
		if o.EstTPS > 0 {
			o.SpeedNote = speedNote(o)
		}
		switch {
		case o.Fit != fitRAM && otherV > 0:
			o.Reason += fmt.Sprintf("; other programs hold %s of VRAM right now", humanBytes(otherV))
		case o.Fit != fitVRAM && otherR > 0:
			o.Reason += fmt.Sprintf("; other programs hold %s of RAM right now", humanBytes(otherR))
		}
		if o.Evicts {
			o.Reason += "; older models on this node are removed to stay under its storage limit"
		}
	}
}

// bestFitReason is the one-line "why this one" of a ranked pick. Only the top
// pick may be called the biggest or the fastest.
func bestFitReason(o *LoadOption, top bool, vram, ram, usableVRAM int64, apple bool) string {
	need := humanBytes(o.Need)
	switch o.Fit {
	case fitVRAM:
		s := fmt.Sprintf("Fits in this node's %s of VRAM (needs about %s)", humanBytes(vram), need)
		if top {
			s += ", best coding pick that fits"
		}
		return s
	case fitSplit:
		how := fmt.Sprintf("(needs about %s: %s in VRAM, the rest in system RAM)", need, humanBytes(min(usableVRAM, o.Need)))
		switch {
		case top && !o.SpeedLow:
			return "Biggest coding model that should stay fast on this node with part of it in system RAM " + how
		case top:
			return "Fastest coding model that fits this node, and only by spilling into system RAM " + how
		case !o.SpeedLow:
			return "Also runs at a usable speed split between VRAM and system RAM " + how
		}
		return "Fits VRAM plus system RAM, but slow " + how
	}
	where, mem := "this node's CPU", fmt.Sprintf("(needs about %s of %s RAM)", need, humanBytes(ram))
	if apple {
		where, mem = "this Mac's GPU", fmt.Sprintf("(needs about %s of %s unified memory)", need, humanBytes(ram))
	}
	switch {
	case top && !o.SpeedLow:
		return fmt.Sprintf("Biggest coding model that should run at a usable speed on %s %s", where, mem)
	case top:
		return fmt.Sprintf("Fastest coding model that fits %s %s", where, mem)
	case !o.SpeedLow:
		return fmt.Sprintf("Also runs at a usable speed on %s %s", where, mem)
	}
	return fmt.Sprintf("Fits this node's memory, but slow on %s %s", map[bool]string{true: "its GPU", false: "its CPU"}[apple], mem)
}

// speedNote is the speed hint shown with a recommendation that has an estimate.
func speedNote(o *LoadOption) string {
	tps := fmt.Sprintf("%.0f", o.EstTPS)
	if o.EstTPS < 10 {
		tps = fmt.Sprintf("%.1f", o.EstTPS)
	}
	how := map[string]string{
		"measured":   "measured on this node",
		"calibrated": "scaled from this node's benchmark",
		"bandwidth":  "from this node's memory speed, not yet benchmarked",
		"split":      "a rough estimate for a model split between VRAM and system RAM, not yet benchmarked",
		"apple":      "a guess for Apple Silicon, not yet benchmarked on this Mac",
	}[o.SpeedSrc]
	if how == "" {
		how = "rough guess; this node has not reported its memory speed yet"
	}
	s := fmt.Sprintf("Expect ~%s tok/s (%s)", tps, how)
	switch {
	case o.EstTPS < 2 && o.Fit == fitRAM && !o.Unified:
		s += ". That is very slow: this node's memory is a poor fit for local models, and a GPU node would do better"
	case o.EstTPS < 2:
		s += ". That is very slow"
	case o.EstTPS < speedUsable:
		s += ". That is slow; it suits background tasks, not waiting on replies"
	}
	return s
}
