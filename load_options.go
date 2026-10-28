package main

import (
	"fmt"
	"net/http"
	"sort"
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
	for i := range modelCatalog {
		catalog[modelCatalog[i].ID] = &modelCatalog[i]
	}
	var opts []LoadOption
	add := func(o LoadOption) {
		o.OnNode = onNode[o.Name]
		o.Need = estimateModelBytes(o.Bytes, o.Ctx)
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
			Direct: m.Storage == "direct", Coding: ce != nil && ce.Coding, Vision: m.Vision})
		if m.Label == "" {
			opts[len(opts)-1].Label = m.Name
		}
	}
	for _, e := range modelCatalog {
		if m := c.models[e.ID]; m != nil && m.Status != "failed" {
			continue // in the library (or on its way there)
		}
		add(LoadOption{Name: e.ID, Label: e.Label, Bytes: e.Bytes, Ctx: e.Ctx, Direct: true, Coding: e.Coding, Vision: e.MMProj != ""})
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
	sort.SliceStable(cand, func(a, b int) bool {
		oa, ob := opts[cand[a]], opts[cand[b]]
		if ra, rb := fitRank(oa.Fit), fitRank(ob.Fit); ra != rb {
			return ra < rb
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
		}
		if r == 0 {
			o.Reason += ", best coding pick that fits"
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
