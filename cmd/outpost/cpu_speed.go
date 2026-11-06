package main

import "strings"

// Expected speed of a model on a CPU-only node, for the "Recommended" pick.
//
// Generating a token on a CPU is limited by memory bandwidth: each token reads
// the active weights once. So tokens/s is about (the node's measured memory
// bandwidth x an efficiency factor) / (bytes one token reads). Both inputs are
// measured, not tabulated: the node times a memory read test (node_membw.go)
// and the controller reads each model's GGUF header, where a mixture-of-experts
// model shows how few experts a token uses. Older nodes that report no
// bandwidth get a cautious guess from their core count. A benchmark of a model
// on the node (NodeBench) overrides the estimate for that model and rescales
// it for the others.

const (
	cpuEfficiency     = 0.6       // llama.cpp reaches less than a plain read test; a safety margin
	cpuMoEFactor      = 0.7       // experts are scattered through a big file: worse cache and page behaviour
	fallbackBWBase    = 4.0       // GB/s guess when the node reported no measurement
	fallbackBWCore    = 2.0       // GB/s added per physical core in that guess
	fallbackBWCap     = 30.0      // and its ceiling
	fallbackVMFactor  = 0.5       // virtual machines in that guess
	speedGood         = 8.0       // tok/s: comfortable
	speedUsable       = 4.0       // tok/s: workable for background tasks
	minCalibrateBytes = 500 << 20 // smallest model whose benchmark rescales the others
	calibrateMinimum  = 0.2       // clamp for rescaling by a measured benchmark
	calibrateMaximum  = 5.0
)

// modelCost is what a model asks of memory per generated token.
type modelCost struct {
	Active int64 // bytes read per token
	Sparse bool  // mixture of experts
}

// costOf derives the cost from the file size and GGUF header. Without a header
// the model counts as dense: the whole file is read per token (slow side).
func costOf(fileBytes int64, s *ModelShape) modelCost {
	return modelCost{Active: s.ActiveBytes(fileBytes), Sparse: s != nil && s.ActiveFrac > 0 && s.ActiveFrac < 1}
}

func isVirtualCPU(model string) bool {
	m := strings.ToLower(model)
	for _, k := range []string{"qemu", "virtual", "kvm", "hypervisor", "vmware", "xen"} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}

// nodeBandwidthGBs is the usable memory bandwidth in GB/s and whether it was measured.
func nodeBandwidthGBs(h *Hello) (float64, bool) {
	if h.MemBWGBs > 0 {
		return h.MemBWGBs, true
	}
	cores := h.CPU.Cores
	if cores <= 0 {
		cores = h.CPU.Threads / 2
	}
	if cores <= 0 {
		cores = 2
	}
	bw := fallbackBWBase + fallbackBWCore*float64(cores)
	if bw > fallbackBWCap {
		bw = fallbackBWCap
	}
	if isVirtualCPU(h.CPU.Model) {
		bw *= fallbackVMFactor
	}
	return bw, false
}

// estimateCPUTokPerSec is the uncalibrated, conservative speed guess.
func estimateCPUTokPerSec(h *Hello, m modelCost) float64 {
	if h == nil || m.Active <= 0 {
		return 0
	}
	bw, _ := nodeBandwidthGBs(h)
	bw *= cpuEfficiency
	if m.Sparse {
		bw *= cpuMoEFactor
	}
	return bw * 1e9 / float64(m.Active)
}

// cpuSpeed is the speed shown for one model on one node, and where it came from.
type cpuSpeed struct {
	TPS    float64
	Source string // estimate | calibrated | measured
}

// cpuSpeedFor returns the expected tok/s of a model on a CPU node. benchName and
// benchCost describe the model the node was benchmarked with (benchCost is nil
// when that model is unknown to the controller).
func cpuSpeedFor(h *Hello, bench *NodeBench, name string, m modelCost, benchCost *modelCost) cpuSpeed {
	est := estimateCPUTokPerSec(h, m)
	src := "estimate"
	if _, measured := nodeBandwidthGBs(h); measured {
		src = "bandwidth"
	}
	if bench == nil {
		return cpuSpeed{est, src}
	}
	got := bench.GenTPS
	if got <= 0 {
		got = bench.TokPerSec
	}
	if got <= 0 {
		return cpuSpeed{est, src}
	}
	if bench.Model == name {
		return cpuSpeed{got, "measured"}
	}
	// Tiny models sit in the CPU cache and say nothing about the speed of big ones.
	if benchCost == nil || benchCost.Active < minCalibrateBytes {
		return cpuSpeed{est, src}
	}
	base := estimateCPUTokPerSec(h, *benchCost)
	if base <= 0 {
		return cpuSpeed{est, src}
	}
	f := got / base
	if f < calibrateMinimum {
		f = calibrateMinimum
	} else if f > calibrateMaximum {
		f = calibrateMaximum
	}
	return cpuSpeed{est * f, "calibrated"}
}

// speedTier: 0 good, 1 usable, 2 slow.
func speedTier(tps float64) int {
	switch {
	case tps >= speedGood:
		return 0
	case tps >= speedUsable:
		return 1
	}
	return 2
}
