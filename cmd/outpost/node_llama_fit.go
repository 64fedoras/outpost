package main

import (
	"log"
	"os"
	"strings"

	"outpost/internal/gguf"
)

// kvFitShare is how much of a discrete GPU's memory the KV cache may take. The
// rest is for the weights that stay on the GPU and llama.cpp's working buffers.
const kvFitShare = 0.5

// kvRAMShare is how much of the machine's RAM the KV cache may take when it
// stays in RAM.
const kvRAMShare = 0.4

// kvCacheFactor scales the 16-bit KV estimate for a quantised cache.
func kvCacheFactor(cacheType string) float64 {
	switch cacheType {
	case "q8_0":
		return 0.53
	case "q4_0", "q4_1":
		return 0.3
	}
	return 1
}

// fitKV decides where the KV cache goes so a saved context size that fit a CPU
// run does not break once a GPU appears (a 256k context at 16 bit is about
// 25 GB, which a Vulkan or CUDA llama-server puts on the GPU even with experts
// on the CPU, and then cannot create its context). It returns whether to keep
// the cache in RAM (--no-kv-offload) and the context per slot to use (0 = as
// asked). vram is the total GPU memory, ram the machine's.
func fitKV(shape *gguf.Shape, ml ModelLoad, vram, ram uint64) (noOffload bool, ctx int) {
	if shape == nil || ml.Ctx <= 0 || vram == 0 || ml.GPULayers == "0" {
		return false, 0
	}
	for _, a := range ml.ExtraArgs {
		if a == "--no-kv-offload" || a == "-nkvo" {
			return false, 0
		}
	}
	slots := max(ml.Parallel, 1)
	kvAt := func(c int) float64 { return float64(shape.KVBytes(c*slots)) * kvCacheFactor(ml.CacheType) }
	if kvAt(ml.Ctx) <= float64(vram)*kvFitShare {
		return false, 0
	}
	if kvAt(ml.Ctx) <= float64(ram)*kvRAMShare {
		return true, 0
	}
	// Too big even for RAM: shrink the context until it fits in RAM.
	budget := float64(ram) * kvRAMShare
	if ram == 0 {
		budget = float64(vram) * kvFitShare
	}
	c := ml.Ctx
	for c > 2048 && kvAt(c) > budget {
		c = c / 2 / 256 * 256
	}
	return ram > 0, max(c, 2048)
}

// applyKVFit adjusts a load for the GPU and RAM this node has.
func (n *Node) applyKVFit(ml *ModelLoad, modelPath string) {
	h := n.hello()
	var vram uint64
	for _, g := range h.GPUs {
		if g.Backend == "metal" {
			return // unified memory: nothing to move
		}
		vram += g.VRAMBytes
	}
	if vram == 0 || ml.Ctx <= 0 {
		return
	}
	f, err := os.Open(modelPath)
	if err != nil {
		return
	}
	defer f.Close()
	shape, err := gguf.ReadShape(f)
	if err != nil {
		return
	}
	noOff, ctx := fitKV(shape, *ml, vram, h.RAMBytes)
	if noOff {
		ml.ExtraArgs = append(ml.ExtraArgs, "--no-kv-offload")
	}
	if ctx > 0 {
		ml.Ctx = ctx
	}
	if noOff || ctx > 0 {
		log.Printf("load %s: the KV cache for the saved context does not fit the GPU; %s", ml.Name,
			strings.TrimSpace(map[bool]string{true: "keeping it in RAM. ", false: ""}[noOff]+map[bool]string{true: "context lowered to fit.", false: ""}[ctx > 0]))
	}
}
