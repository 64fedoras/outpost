package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// LoadOption is one choice in the Load model dialog for one node.
type LoadOption struct {
	Name      string `json:"name"` // library name, or catalog ID when not in the library yet
	Label     string `json:"label"`
	Bytes     int64  `json:"bytes"`
	Ctx       int    `json:"ctx"`
	InLibrary bool   `json:"in_library"`
	Direct    bool   `json:"direct"` // nodes download it from Hugging Face
	Coding    bool   `json:"coding"`
	Vision    bool   `json:"vision"`
	OnNode    bool   `json:"on_node"`
	Fit       string `json:"fit"`
	Need      int64  `json:"need_bytes"`
	Group     string `json:"group"` // recommended | library | catalog
	Rank      int    `json:"rank"`  // 1 = best pick for this node; 0 = not recommended
	Reason    string `json:"reason,omitempty"`
	// CPU-only nodes: expected generation speed and where the figure comes from.
	EstTPS    float64 `json:"est_tok_s,omitempty"`
	SpeedSrc  string  `json:"speed_source,omitempty"` // estimate | calibrated | measured
	SpeedLow  bool    `json:"speed_low,omitempty"`    // below the usable speed: show a warning
	SpeedNote string  `json:"speed_note,omitempty"`   // e.g. "Expect ~7.9 tok/s (...)", with a plain warning when slow
	shape     *ModelShape
}

// loadOptionsFor lists what a node can load: ready library models and catalog
// models that are not in the library yet, sized for this node. The best coding
// models that fit are marked as recommended, best first (same ordering as
// suggestCoding: fully in VRAM first, then bigger). A model is never
// recommended when it does not fit, when the node has no llama.cpp build, or
// when the node lacks disk space for a download. Unknown (custom) models are
// listed but not recommended because they are not known to be coding models.
// c.mu must be held.
func (c *Controller) loadOptionsFor(id string, maxRecommended int) ([]LoadOption, bool) {
	n := c.nodes[id]
	if n == nil {
		return nil, false
	}
	vram, ram, buildOK := loadableMemory(n.Hello)
	onNode := map[string]bool{}
	var free int64
	if n.Hello != nil {
		for _, m := range n.Hello.Models {
			onNode[m.Name] = true
		}
		free = int64(n.Hello.ModelsFreeBytes)
	}
	catalog := map[string]*CatalogEntry{}
	cat := catalogNow()
	for i := range cat {
		catalog[cat[i].ID] = &cat[i]
	}
	var opts []LoadOption
	add := func(o LoadOption) {
		o.OnNode = onNode[o.Name]
		o.Need = estimateModelBytes(o.Bytes, o.Ctx)
		if o.shape != nil && o.Ctx > 0 { // real KV size from the GGUF header
			o.Need = o.Bytes + o.shape.KVBytes(o.Ctx) + fitOverheadBytes
		}
		o.Fit = fitNo
		if buildOK {
			o.Fit = fitOnNode(o.Need, vram, ram)
		}
		opts = append(opts, o)
	}
	for _, m := range c.models {
		if m.Status != "ready" {
			continue
		}
		ce := catalog[m.Name]
		add(LoadOption{Name: m.Name, Label: m.Label, Bytes: m.BytesTotal, Ctx: m.Ctx, InLibrary: true,
			Direct: m.Storage == "direct", Coding: ce != nil && ce.Coding, Vision: m.Vision, shape: pickShape(m.Shape, c.catShapes[m.Name])})
		if m.Label == "" {
			opts[len(opts)-1].Label = m.Name
		}
	}
	for _, e := range cat {
		if m := c.models[e.ID]; m != nil && m.Status != "failed" {
			continue // in the library (or on its way there)
		}
		add(LoadOption{Name: e.ID, Label: e.Label, Bytes: e.Bytes, Ctx: e.Ctx, Direct: true, Coding: e.Coding, Vision: e.MMProj != "", shape: c.catShapes[e.ID]})
	}
	// Rank the candidates that may be recommended.
	var cand []int
	for i, o := range opts {
		if !o.Coding || o.Fit == fitNo {
			continue
		}
		if !o.OnNode && free > 0 && o.Bytes > free {
			continue // would not fit on the node's disk
		}
		cand = append(cand, i)
	}
	// CPU-only candidates get an expected speed, and are ranked by it.
	var bench *NodeBench
	if n.Bench != nil {
		bench = n.Bench
	}
	for _, i := range cand {
		o := &opts[i]
		if o.Fit != fitRAM || n.Hello == nil {
			continue
		}
		if !o.Coding {
			continue
		}
		var benchCost *modelCost
		if bench != nil {
			for j := range opts {
				if opts[j].Name == bench.Model {
					mc := costOf(opts[j].Bytes, opts[j].shape)
					benchCost = &mc
					break
				}
			}
		}
		sp := cpuSpeedFor(n.Hello, bench, o.Name, costOf(o.Bytes, o.shape), benchCost)
		o.EstTPS, o.SpeedSrc = sp.TPS, sp.Source
		o.SpeedLow = sp.TPS < speedUsable
	}
	sort.SliceStable(cand, func(a, b int) bool {
		oa, ob := opts[cand[a]], opts[cand[b]]
		if ra, rb := fitRank(oa.Fit), fitRank(ob.Fit); ra != rb {
			return ra < rb
		}
		if oa.Fit == fitRAM && oa.EstTPS > 0 && ob.EstTPS > 0 {
			ta, tb := speedTier(oa.EstTPS), speedTier(ob.EstTPS)
			if ta != tb {
				return ta < tb
			}
			if ta == 2 && oa.EstTPS != ob.EstTPS { // nothing is usable: the fastest wins
				return oa.EstTPS > ob.EstTPS
			}
		}
		if oa.Bytes != ob.Bytes {
			return oa.Bytes > ob.Bytes
		}
		return oa.Name < ob.Name
	})
	if maxRecommended > 0 && len(cand) > maxRecommended {
		cand = cand[:maxRecommended]
	}
	for r, i := range cand {
		o := &opts[i]
		o.Rank = r + 1
		o.Group = "recommended"
		switch o.Fit {
		case fitVRAM:
			o.Reason = fmt.Sprintf("Fits in this node's %s of VRAM (needs about %s)", humanBytes(vram), humanBytes(o.Need))
		case fitSplit:
			o.Reason = fmt.Sprintf("Biggest coding model that fits this node using VRAM plus system RAM (needs about %s); slower than all-VRAM", humanBytes(o.Need))
		default:
			o.Reason = fmt.Sprintf("Biggest coding model that fits this node's %s of RAM (needs about %s); runs on the CPU", humanBytes(ram), humanBytes(o.Need))
			if o.EstTPS > 0 {
				what := "Biggest coding model that should run at a usable speed on this node's CPU"
				if o.SpeedLow {
					what = "Fastest coding model that fits this node's CPU"
				}
				if r > 0 { // runner-ups (shown by "Pick best") are not the biggest or the fastest
					what = "Also runs at a usable speed on this node's CPU"
					if o.SpeedLow {
						what = "Fits this node's RAM, but slow on its CPU"
					}
				}
				o.Reason = fmt.Sprintf("%s (needs about %s of %s RAM)", what, humanBytes(o.Need), humanBytes(ram))
				o.SpeedNote = speedNote(o)
				continue
			}
		}
		if r == 0 {
			o.Reason += ", best coding pick that fits"
		} else if o.Fit != fitVRAM {
			o.Reason = "Also fits: " + strings.TrimPrefix(o.Reason, "Biggest coding model that fits ")
		}
	}
	for i := range opts {
		if opts[i].Group == "" {
			if opts[i].InLibrary {
				opts[i].Group = "library"
			} else {
				opts[i].Group = "catalog"
			}
		}
	}
	sort.SliceStable(opts, func(a, b int) bool {
		oa, ob := opts[a], opts[b]
		ga, gb := loadGroupOrder(oa.Group), loadGroupOrder(ob.Group)
		if ga != gb {
			return ga < gb
		}
		if oa.Group == "recommended" {
			return oa.Rank < ob.Rank
		}
		if ra, rb := fitRank(oa.Fit), fitRank(ob.Fit); ra != rb {
			return ra < rb
		}
		return oa.Label < ob.Label
	})
	return opts, buildOK
}

func loadGroupOrder(g string) int {
	switch g {
	case "recommended":
		return 0
	case "library":
		return 1
	}
	return 2
}

// handleLoadOptions answers GET /api/nodes/{id}/load-options for the Load model dialog.
func (c *Controller) handleLoadOptions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c.mu.Lock()
	opts, buildOK := c.loadOptionsFor(id, 3)
	c.mu.Unlock()
	if opts == nil && !buildOK {
		c.mu.Lock()
		_, known := c.nodes[id]
		c.mu.Unlock()
		if !known {
			writeJSON(w, 404, map[string]string{"error": "unknown node"})
			return
		}
	}
	if opts == nil {
		opts = []LoadOption{}
	}
	writeJSON(w, 200, map[string]any{"options": opts, "build_ok": buildOK})
}

// speedNote is the speed hint shown with a CPU recommendation.
func speedNote(o *LoadOption) string {
	tps := fmt.Sprintf("%.0f", o.EstTPS)
	if o.EstTPS < 10 {
		tps = fmt.Sprintf("%.1f", o.EstTPS)
	}
	how := map[string]string{"measured": "measured on this node", "calibrated": "scaled from this node's benchmark", "bandwidth": "from this node's memory speed, not yet benchmarked"}[o.SpeedSrc]
	if how == "" {
		how = "rough guess; this node has not reported its memory speed yet"
	}
	s := fmt.Sprintf("Expect ~%s tok/s (%s)", tps, how)
	switch {
	case o.EstTPS < 2:
		s += ". That is very slow: this node's memory is a poor fit for local models, and a GPU node would do better"
	case o.EstTPS < speedUsable:
		s += ". That is slow; it suits background tasks, not waiting on replies"
	}
	return s
}

func pickShape(a, b *ModelShape) *ModelShape {
	if a != nil && a.ActiveFrac > 0 {
		return a
	}
	if b != nil {
		return b
	}
	return a
}
