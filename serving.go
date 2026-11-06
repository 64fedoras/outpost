package main

// Model serving options in the Load dialog (ideas 19, 25, 26):
//   - a quantised KV cache (q8_0 or q4_0) and the flash attention switch, which
//     shrink the context memory so a longer context fits;
//   - a small draft model for speculative decoding (llama-server -md);
//   - 2 to 4 parallel slots, so a big node runs several calls at once. Each
//     slot gets the full context size (the node starts llama-server with
//     ctx × slots), and the dispatcher lets that many calls share the node.

import (
	"errors"
	"fmt"
)

const maxSlots = 4

// kvFactor is the KV cache size relative to the 16-bit cache.
func kvFactor(cacheType string) float64 {
	switch cacheType {
	case "q8_0":
		return 34.0 / 64 // 32 values in 34 bytes vs 64 bytes
	case "q4_0":
		return 18.0 / 64
	}
	return 1
}

// checkServing validates and normalises the serving options of a load request.
func (c *Controller) checkServing(cacheType, flash *string, draft, main string, parallel *int) error {
	switch *cacheType {
	case "", "f16":
		*cacheType = ""
	case "q8_0", "q4_0":
	default:
		return fmt.Errorf("unknown cache type %q (use q8_0 or q4_0)", *cacheType)
	}
	switch *flash {
	case "", "auto":
		*flash = ""
	case "on", "off":
	default:
		return fmt.Errorf("flash attention must be on, off or automatic")
	}
	if *cacheType != "" && *flash == "off" {
		return errors.New("a compressed context cache needs flash attention; set it to on or automatic")
	}
	switch {
	case *parallel <= 1:
		*parallel = 0
	case *parallel > maxSlots:
		*parallel = maxSlots
	}
	if draft == "" {
		return nil
	}
	if draft == main {
		return errors.New("the draft model must be a different, smaller model")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	d, m := c.models[draft], c.models[main]
	if d == nil || d.Status != "ready" {
		return fmt.Errorf("the draft model %s is not ready in the library", draft)
	}
	if m != nil && d.BytesTotal >= m.BytesTotal/2 {
		return errors.New("the draft model should be much smaller than the main model (under half its size) or it will not speed anything up")
	}
	return nil
}

// slotsLocked is how many calls a node may run at once: the parallel slots
// it was loaded with, while that model is the one loaded. c.mu must be held.
func (c *Controller) slotsLocked(id string) int {
	n := c.nodes[id]
	if n == nil || n.LoadPrefs == nil || n.LoadPrefs.Parallel <= 1 {
		return 1
	}
	if hb := c.lastHB[id]; hb == nil || hb.LoadedModel != n.LoadPrefs.Name {
		return 1
	}
	return min(n.LoadPrefs.Parallel, maxSlots)
}

// exclusive marks a reservation that takes every slot (benchmarks).
const exclusive = 1 << 20
