package main

// HTTP routes for image generation. The web UI's New image page and, later, the
// OpenAI-style /v1/images routes sit on top of newGen in controller_images.go.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

func (c *Controller) imageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/images/models", c.auth(c.handleImageModels))
	mux.HandleFunc("POST /api/images/models/{id}/accept", c.auth(c.handleImageAccept))
	mux.HandleFunc("POST /api/images/models/{id}/install", c.auth(c.handleImageInstall))
	mux.HandleFunc("GET /api/images/settings", c.auth(c.handleImageSettings))
	mux.HandleFunc("POST /api/images/settings", c.auth(c.handleImageSettings))
	mux.HandleFunc("GET /api/images", c.auth(c.handleListGens))
	mux.HandleFunc("POST /api/images", c.auth(c.handleCreateGen))
	mux.HandleFunc("GET /api/images/{id}", c.auth(c.handleGetGen))
	mux.HandleFunc("GET /api/images/{id}/files/{index}", c.auth(c.handleGenFile))
	mux.HandleFunc("POST /api/images/{id}/cancel", c.auth(c.handleCancelGen))
	mux.HandleFunc("POST /api/images/{id}/retry", c.auth(c.handleRetryGen))
	mux.HandleFunc("POST /api/images/{id}/keep", c.auth(c.handleKeepGen))
	mux.HandleFunc("DELETE /api/images/{id}", c.auth(c.handleDeleteGen))
}

// writeGenJSON marshals while holding the generation lock, so a picture finishing
// at that moment cannot race the encoder.
func (c *Controller) writeGenJSON(w http.ResponseWriter, code int, v func() any) {
	c.gens.mu.Lock()
	b, err := json.Marshal(v())
	c.gens.mu.Unlock()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}

type imageModelView struct {
	ImageModelDef
	Bytes    int64     `json:"bytes"`
	AuxBytes int64     `json:"aux_bytes"`
	Accepted bool      `json:"accepted"`
	Nodes    []GenNode `json:"nodes"`
}

func (c *Controller) handleImageModels(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.gens.mu.Lock()
	views := make([]imageModelView, 0, len(imageCatalog))
	for i := range imageCatalog {
		d := &imageCatalog[i]
		total, aux := d.Bytes()
		views = append(views, imageModelView{ImageModelDef: *d, Bytes: total, AuxBytes: aux,
			Accepted: !d.Restricted || !c.gens.settings.Accepted[d.ID].IsZero(), Nodes: c.genNodesLocked(d, d.Size, d.Size)})
	}
	set := c.gens.settings
	c.gens.mu.Unlock()
	c.mu.Unlock()
	writeJSON(w, 200, map[string]any{"models": views, "settings": map[string]int{"retention_days": set.RetentionDays, "max_gb": set.MaxGB}})
}

func (c *Controller) handleImageAccept(w http.ResponseWriter, r *http.Request) {
	d := imageModel(r.PathValue("id"))
	if d == nil {
		writeJSON(w, 404, map[string]string{"error": "unknown model"})
		return
	}
	c.gens.mu.Lock()
	if c.gens.settings.Accepted == nil {
		c.gens.settings.Accepted = map[string]time.Time{}
	}
	c.gens.settings.Accepted[d.ID] = time.Now()
	c.saveGenSettingsLocked()
	c.gens.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleImageInstall(w http.ResponseWriter, r *http.Request) {
	d := imageModel(r.PathValue("id"))
	if d == nil {
		writeJSON(w, 404, map[string]string{"error": "unknown model"})
		return
	}
	var req struct {
		Node string `json:"node"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req)
	c.mu.Lock()
	online := c.conns[req.Node] != nil
	c.mu.Unlock()
	if !online {
		writeJSON(w, 409, map[string]string{"error": "that node is offline"})
		return
	}
	go func() {
		if err := c.ensureSDOnNode(context.Background(), req.Node, d); err != nil {
			c.setNodeOp(req.Node, OpStatus{Op: "model.fetch", Target: d.Dir(), State: "failed", Error: err.Error()})
		}
	}()
	writeJSON(w, 202, map[string]bool{"ok": true})
}

func (c *Controller) handleImageSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			RetentionDays *int `json:"retention_days"`
			MaxGB         *int `json:"max_gb"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad request"})
			return
		}
		if (req.RetentionDays != nil && (*req.RetentionDays < 0 || *req.RetentionDays > 3650)) || (req.MaxGB != nil && (*req.MaxGB < 0 || *req.MaxGB > 100000)) {
			writeJSON(w, 400, map[string]string{"error": "days must be 0 to 3650 and the size 0 to 100000 GB (0 = no limit)"})
			return
		}
		c.gens.mu.Lock()
		if req.RetentionDays != nil {
			c.gens.settings.RetentionDays = *req.RetentionDays
		}
		if req.MaxGB != nil {
			c.gens.settings.MaxGB = *req.MaxGB
		}
		c.saveGenSettingsLocked()
		c.gens.mu.Unlock()
		go c.pruneGens()
	}
	c.gens.mu.Lock()
	set := c.gens.settings
	c.gens.mu.Unlock()
	used := dirBytes(c.genDir())
	writeJSON(w, 200, map[string]any{"retention_days": set.RetentionDays, "max_gb": set.MaxGB, "api_enabled": set.APIEnabled, "used_bytes": used})
}

func (c *Controller) handleListGens(w http.ResponseWriter, r *http.Request) {
	limit := 60
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	c.writeGenJSON(w, 200, func() any {
		recs := make([]*GenRec, 0, len(c.gens.recs))
		for _, g := range c.gens.recs {
			recs = append(recs, g)
		}
		sort.Slice(recs, func(i, j int) bool { return recs[i].Created.After(recs[j].Created) })
		if len(recs) > limit {
			recs = recs[:limit]
		}
		type row struct {
			*GenRec
			Status string `json:"status"`
		}
		out := make([]row, len(recs))
		for i, g := range recs {
			out[i] = row{g, g.status()}
		}
		return map[string]any{"generations": out}
	})
}

func (c *Controller) handleCreateGen(w http.ResponseWriter, r *http.Request) {
	var req GenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	rec, err := c.newGen(req, "web")
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	c.writeGenJSON(w, 201, func() any { return map[string]any{"generation": rec, "status": rec.status()} })
}

func (c *Controller) genOr404(w http.ResponseWriter, r *http.Request) *GenRec {
	id := r.PathValue("id")
	c.gens.mu.Lock()
	rec := c.gens.recs[id]
	c.gens.mu.Unlock()
	if rec == nil {
		writeJSON(w, 404, map[string]string{"error": "no such generation"})
	}
	return rec
}

func (c *Controller) handleGetGen(w http.ResponseWriter, r *http.Request) {
	rec := c.genOr404(w, r)
	if rec == nil {
		return
	}
	c.writeGenJSON(w, 200, func() any { return map[string]any{"generation": rec, "status": rec.status()} })
}

func (c *Controller) handleGenFile(w http.ResponseWriter, r *http.Request) {
	rec := c.genOr404(w, r)
	if rec == nil {
		return
	}
	idx, err := strconv.Atoi(r.PathValue("index"))
	c.gens.mu.Lock()
	ok := err == nil && idx >= 0 && idx < len(rec.Items) && rec.Items[idx].Status == "done"
	c.gens.mu.Unlock()
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "no such picture"})
		return
	}
	p := genFile(filepath.Join(c.genDir(), rec.ID), idx)
	if _, err := os.Stat(p); err != nil {
		writeJSON(w, 404, map[string]string{"error": "the picture was deleted"})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+rec.ID+"-"+strconv.Itoa(idx)+`.png"`)
	}
	http.ServeFile(w, r, p)
}

func (c *Controller) handleCancelGen(w http.ResponseWriter, r *http.Request) {
	if !c.cancelGen(r.PathValue("id")) {
		writeJSON(w, 404, map[string]string{"error": "no such generation"})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleRetryGen(w http.ResponseWriter, r *http.Request) {
	rec := c.genOr404(w, r)
	if rec == nil {
		return
	}
	c.gens.mu.Lock()
	for _, it := range rec.Items {
		if it.Status == "failed" || it.Status == "cancelled" {
			it.Status, it.Error, it.Attempts, it.Node, it.NodeName = "queued", "", 0, "", ""
		}
	}
	c.gens.mu.Unlock()
	c.saveGen(rec)
	c.genKick()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleKeepGen(w http.ResponseWriter, r *http.Request) {
	rec := c.genOr404(w, r)
	if rec == nil {
		return
	}
	var req struct {
		Keep bool `json:"keep"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req)
	c.gens.mu.Lock()
	rec.Keep = req.Keep
	c.gens.mu.Unlock()
	c.saveGen(rec)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleDeleteGen(w http.ResponseWriter, r *http.Request) {
	rec := c.genOr404(w, r)
	if rec == nil {
		return
	}
	c.gens.mu.Lock()
	running := !rec.finished()
	c.gens.mu.Unlock()
	if running {
		c.cancelGen(rec.ID)
	}
	c.deleteGen(rec.ID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
