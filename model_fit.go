package main

import (
	"sort"
	"strings"
)

// Fit levels for a model on one node.
const (
	fitVRAM  = "vram"  // weights, KV cache and buffers fit in GPU memory: fast
	fitSplit = "split" // spills from GPU memory into system RAM: slower
	fitRAM   = "ram"   // node has no GPU; runs from system RAM on the CPU
	fitNo    = "no"    // does not fit
)

// NodeFit is the estimate for one model on one node.
type NodeFit struct {
	NodeID  string `json:"node_id"`
	Name    string `json:"name"`
	Fit     string `json:"fit"`
	Online  bool   `json:"online"`
	CanLoad bool   `json:"can_load"` // online and free to start a load (not busy, not lending its memory)
	Need    int64  `json:"need_bytes"`
}

const (
	fitOverheadBytes = 600 << 20 // compute buffers and runtime overhead
	kvBytesPerGBTok  = 12 << 10  // rough KV cache bytes per token per GB of weights
)

// estimateModelBytes estimates the memory a model needs when loaded: the
// weights plus an approximate KV cache at the given context size. The KV
// size is scaled from the weights because the GGUF header is not read, so
// it is a rule of thumb, not an exact figure.
func estimateModelBytes(weights int64, ctx int) int64 {
	if ctx <= 0 {
		ctx = 8192
	}
	kv := int64(float64(weights) / (1 << 30) * float64(kvBytesPerGBTok) * float64(ctx))
	return weights + kv + fitOverheadBytes
}

// nodeMemory returns the usable GPU memory and system RAM of a node. Apple
// Silicon GPUs share system RAM, so they count as RAM only.
func nodeMemory(h *Hello) (vram, ram int64) {
	if h == nil {
		return 0, 0
	}
	for _, g := range h.GPUs {
		if g.Vendor != "apple" {
			vram += int64(g.VRAMBytes)
		}
	}
	return vram, int64(h.RAMBytes)
}

// fitOnNode classifies a model needing `need` bytes on a node with the given
// memory. Headroom is left for the OS and the desktop.
func fitOnNode(need, vram, ram int64) string {
	usableVRAM := vram * 92 / 100
	usableRAM := ram * 75 / 100
	switch {
	case vram > 0 && need <= usableVRAM:
		return fitVRAM
	case vram > 0 && need <= usableVRAM+usableRAM:
		return fitSplit
	case vram == 0 && need <= usableRAM:
		return fitRAM
	}
	return fitNo
}

// fitRank orders fit levels from best to worst.
func fitRank(f string) int {
	switch f {
	case fitVRAM:
		return 0
	case fitSplit:
		return 1
	case fitRAM:
		return 2
	}
	return 3
}

// nodeFitsLocked estimates one model (weights in bytes) on every node. c.mu must be held.
func (c *Controller) nodeFitsLocked(weights int64, ctx int) []NodeFit {
	need := estimateModelBytes(weights, ctx)
	out := []NodeFit{}
	for id, n := range c.nodes {
		vram, ram := nodeMemory(n.Hello)
		online := c.conns[id] != nil
		out = append(out, NodeFit{
			NodeID: id, Name: displayName(n), Fit: fitOnNode(need, vram, ram), Online: online, Need: need,
			CanLoad: online && n.Hello != nil && c.pipelines[id] == nil && c.lendingToLocked(id) == "",
		})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// suggestion is a catalog entry ranked for this cluster.
type suggestion struct {
	ID   string    `json:"id"`
	Fits []NodeFit `json:"fits"`
	Best string    `json:"best"` // best fit level over all nodes
}

// suggestCoding ranks the coding models in the catalog by how well they fit
// the cluster: models that run fully in VRAM somewhere first, bigger models
// ahead of smaller ones within a level. Models that fit nowhere are left out.
// fits maps catalog ID to its per-node estimate.
func suggestCoding(catalog []CatalogEntry, fits map[string][]NodeFit, limit int) []suggestion {
	var out []suggestion
	for _, e := range catalog {
		if !e.Coding {
			continue
		}
		best := fitNo
		for _, f := range fits[e.ID] {
			if fitRank(f.Fit) < fitRank(best) {
				best = f.Fit
			}
		}
		if best != fitNo {
			out = append(out, suggestion{ID: e.ID, Fits: fits[e.ID], Best: best})
		}
	}
	size := map[string]int64{}
	for _, e := range catalog {
		size[e.ID] = e.Bytes
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := fitRank(out[i].Best), fitRank(out[j].Best); ri != rj {
			return ri < rj
		}
		return size[out[i].ID] > size[out[j].ID]
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
