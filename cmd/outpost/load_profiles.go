package main

// Named load settings per node ("Qwen 32B, 32K, experts in RAM"): saved from
// the Load dialog, picked from a list there. They are kept in nodes.json with
// the node and survive reconnects. Pool helpers are not part of a profile.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

const maxLoadProfiles = 20

type LoadProfile struct {
	Name string    `json:"name"`
	Load ModelLoad `json:"load"`
}

func (c *Controller) handleSaveLoadProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name      string `json:"name"`
		ModelName string `json:"model"`
		Ctx       int    `json:"ctx"`
		GPULayers string `json:"gpu_layers"`
		Threads   int    `json:"threads"`
		ExtraArgs string `json:"extra_args"`
		CacheType string `json:"cache_type"`
		FlashAttn string `json:"flash_attn"`
		Draft     string `json:"draft"`
		Parallel  int    `json:"parallel"`
		CPUMoe    string `json:"cpu_moe"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 60 || req.ModelName == "" {
		writeJSON(w, 400, map[string]string{"error": "give the settings a name (up to 60 characters)"})
		return
	}
	if !validCPUMoe(req.CPUMoe) {
		writeJSON(w, 400, map[string]string{"error": "experts in RAM must be off, all, or a number of layers"})
		return
	}
	p := LoadProfile{Name: name, Load: ModelLoad{Name: req.ModelName, Ctx: req.Ctx, GPULayers: req.GPULayers, Threads: req.Threads,
		ExtraArgs: strings.Fields(req.ExtraArgs), CacheType: req.CacheType, FlashAttn: req.FlashAttn, Draft: req.Draft, Parallel: req.Parallel, CPUMoe: req.CPUMoe}}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.nodes[id]
	if n == nil {
		writeJSON(w, 404, map[string]string{"error": "unknown node"})
		return
	}
	replaced := false
	for i := range n.Profiles {
		if strings.EqualFold(n.Profiles[i].Name, name) {
			n.Profiles[i], replaced = p, true
		}
	}
	if !replaced {
		if len(n.Profiles) >= maxLoadProfiles {
			writeJSON(w, 409, map[string]string{"error": "this node already has 20 saved settings; delete one first"})
			return
		}
		n.Profiles = append(n.Profiles, p)
	}
	sort.Slice(n.Profiles, func(i, j int) bool { return strings.ToLower(n.Profiles[i].Name) < strings.ToLower(n.Profiles[j].Name) })
	c.saveNodesLocked()
	writeJSON(w, 200, map[string]any{"profiles": n.Profiles})
}

func (c *Controller) handleDeleteLoadProfile(w http.ResponseWriter, r *http.Request) {
	id, name := r.PathValue("id"), r.PathValue("name")
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.nodes[id]
	if n == nil {
		writeJSON(w, 404, map[string]string{"error": "unknown node"})
		return
	}
	out := n.Profiles[:0:0]
	for _, p := range n.Profiles {
		if !strings.EqualFold(p.Name, name) {
			out = append(out, p)
		}
	}
	n.Profiles = out
	c.saveNodesLocked()
	writeJSON(w, 200, map[string]any{"profiles": n.Profiles})
}
