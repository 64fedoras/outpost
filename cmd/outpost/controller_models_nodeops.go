package main

// Loading, unloading and cleaning models on nodes. Split out of controller_models.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// ---------- node model operations ----------

type nodeOpView struct {
	OpStatus
	Updated time.Time `json:"updated"`
	// LastFailure survives the "preparing" and "downloading" states of the
	// retries that follow a failure, so the node card keeps showing one steady
	// message (and stays under "Needs attention") until a load succeeds.
	LastFailure *opFailure `json:"last_failure,omitempty"`
}

type opFailure struct {
	Op    string    `json:"op"`
	Error string    `json:"error"`
	At    time.Time `json:"at"`
	Count int       `json:"count"`
}

func (c *Controller) setNodeOp(nodeID string, st OpStatus) {
	c.mu.Lock()
	c.recordNodeOpLocked(nodeID, st)
	c.mu.Unlock()
}

// recordNodeOpLocked stores a node's operation state and notifies when a model
// download or load first fails.
func (c *Controller) recordNodeOpLocked(nodeID string, st OpStatus) {
	prev := c.nodeOps[nodeID]
	view := &nodeOpView{OpStatus: st, Updated: time.Now()}
	if prev != nil {
		view.LastFailure = prev.LastFailure
	}
	switch {
	case st.State == "failed" && (st.Op == "model.load" || st.Op == "model.push" || st.Op == "runtime" || st.Op == "model.fetch"):
		f := &opFailure{Op: st.Op, Error: st.Error, At: time.Now(), Count: 1}
		if view.LastFailure != nil {
			f.Count = view.LastFailure.Count + 1
		}
		view.LastFailure = f
	case st.Op == "model.load" && (st.State == "ready" || st.State == "done"), st.Op == "model.unload", st.Op == "model.remove":
		view.LastFailure = nil
	}
	c.nodeOps[nodeID] = view
	if st.State != "failed" || !(st.Op == "model.load" || st.Op == "model.push" || st.Op == "runtime") {
		return
	}
	if prev != nil && prev.State == "failed" && prev.Op == st.Op && prev.Target == st.Target {
		return
	}
	name := nodeID
	if n := c.nodes[nodeID]; n != nil {
		name = displayName(n)
	}
	verb := map[string]string{"model.load": "Loading", "model.push": "Copying", "runtime": "Installing llama.cpp"}[st.Op]
	if verb == "" {
		verb = "Getting"
	}
	go c.deliverNotification("model_failed", "Model failed", fmt.Sprintf("%s %s on %s failed: %s", verb, st.Target, name, firstNonEmpty(st.Error, "no reason given")), "")
}

// waitNodeOp waits until the node reports a final state for op/target.
func (c *Controller) waitNodeOp(ctx context.Context, nodeID, op, target string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		st := c.nodeOps[nodeID]
		online := c.conns[nodeID] != nil
		c.mu.Unlock()
		if st != nil && st.Op == op && st.Target == target {
			switch st.State {
			case "done", "ready":
				return nil
			case "failed":
				return errors.New(st.Error)
			}
		}
		if !online {
			return errors.New("node went offline")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("timed out waiting for %s", op)
}

// reclaimFor says which models a node may delete to make room for target.
// Tier 1 (Names): ones this controller holds a ready copy of. Tier 2 (Direct):
// ones downloaded straight from Hugging Face, which the node can fetch again;
// the node uses them only after tier 1. Models the controller does not know
// are never offered. Disabled when the Settings option is off.
func (c *Controller) reclaimFor(nodeID, target string) *Reclaim {
	c.mu.Lock()
	defer c.mu.Unlock()
	rp := &Reclaim{Enabled: !c.settings.AutoFreeOff}
	n := c.nodes[nodeID]
	if !rp.Enabled || n == nil || n.Hello == nil {
		return rp
	}
	for _, sm := range n.Hello.Models {
		m := c.models[sm.Name]
		if m == nil || sm.Name == target || len(m.Files) == 0 {
			continue
		}
		if m.Storage == "direct" {
			if m.Repo != "" && len(m.HFFiles) == len(m.Files) {
				rp.Direct = append(rp.Direct, sm.Name)
			}
		} else if m.Status == "ready" {
			rp.Names = append(rp.Names, sm.Name)
		}
	}
	return rp
}

func (c *Controller) nodeHasModel(nodeID, name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.nodes[nodeID]
	if n == nil || n.Hello == nil {
		return false
	}
	for _, m := range n.Hello.Models {
		if m.Name == name {
			return true
		}
	}
	return false
}

// handleLoadModel runs the whole pipeline for a node in the background:
// make sure llama.cpp is installed, push the model if missing, then load it.
func (c *Controller) handleLoadModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name      string   `json:"name"`
		Ctx       int      `json:"ctx"`
		GPULayers string   `json:"gpu_layers"`
		Threads   int      `json:"threads"`
		ExtraArgs string   `json:"extra_args"`
		Pool      []string `json:"pool"` // helper node IDs that lend their memory
		CacheType string   `json:"cache_type"`
		FlashAttn string   `json:"flash_attn"`
		Draft     string   `json:"draft"`
		Parallel  int      `json:"parallel"`
		CPUMoe    string   `json:"cpu_moe"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeJSON(w, 400, map[string]string{"error": "model name required"})
		return
	}
	c.mu.Lock()
	m := c.models[req.Name]
	n := c.nodes[id]
	online := c.conns[id] != nil
	busy := c.pipelines[id] != nil
	c.mu.Unlock()
	switch {
	case m == nil || m.Status != "ready":
		writeJSON(w, 409, map[string]string{"error": "that model is not ready in the library yet"})
		return
	case n == nil || !online:
		writeJSON(w, 409, map[string]string{"error": "node is offline"})
		return
	case busy:
		writeJSON(w, 409, map[string]string{"error": "this node is already setting up a model"})
		return
	}
	if len(req.Pool) > 0 && !c.poolingOn() {
		writeJSON(w, 409, map[string]string{"error": "memory pooling is off; enable it under Settings first (trusted networks only)"})
		return
	}
	if err := c.checkPool(id, req.Pool); err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	if req.Ctx <= 0 {
		req.Ctx = m.Ctx
	}
	if err := c.checkServing(&req.CacheType, &req.FlashAttn, req.Draft, req.Name, &req.Parallel); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if !validCPUMoe(req.CPUMoe) {
		writeJSON(w, 400, map[string]string{"error": "experts in RAM must be off, all, or a number of layers"})
		return
	}
	ml := ModelLoad{Name: req.Name, Ctx: req.Ctx, GPULayers: req.GPULayers, Threads: req.Threads, ExtraArgs: strings.Fields(req.ExtraArgs), CPUMoe: req.CPUMoe,
		CacheType: req.CacheType, FlashAttn: req.FlashAttn, Draft: req.Draft, Parallel: req.Parallel}
	ctx, cancel := context.WithCancel(context.Background())
	p := &pipeline{cancel}
	c.mu.Lock()
	c.pipelines[id] = p
	oldPool := n.Pool
	n.LoadPrefs = &ml
	n.Pool = append([]string(nil), req.Pool...)
	for _, h := range req.Pool {
		if hn := c.nodes[h]; hn != nil {
			hn.LoadPrefs = nil // a helper lends memory instead of running its own model
		}
	}
	c.saveNodesLocked()
	c.mu.Unlock()
	c.releaseHelpers(oldPool, req.Pool)
	go func() {
		defer c.endPipeline(id, p)
		if err := c.loadPipeline(ctx, id, ml); err != nil {
			if ctx.Err() != nil {
				return // cancelled by an unload
			}
			log.Printf("node %s: load %s failed: %v", id, ml.Name, err)
			c.setNodeOp(id, OpStatus{Op: "model.load", Target: ml.Name, State: "failed", Error: err.Error()})
		}
	}()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) loadPipeline(ctx context.Context, id string, ml ModelLoad) error {
	c.mu.Lock()
	n := c.nodes[id]
	m := c.models[ml.Name]
	nc := c.conns[id]
	c.mu.Unlock()
	if n == nil || nc == nil || m == nil {
		return errors.New("node or model unavailable")
	}
	// 1. llama.cpp (one build across a pool, so the RPC protocol matches)
	c.mu.Lock()
	pool := append([]string(nil), n.Pool...)
	c.mu.Unlock()
	build := ""
	if len(pool) > 0 {
		var err error
		if build, err = c.poolBuild(ctx, append([]string{id}, pool...)); err != nil {
			return err
		}
	}
	if err := c.installRuntimeOn(ctx, id, build); err != nil {
		return err
	}
	// 1b. helpers lend their memory
	if len(pool) > 0 {
		if !c.poolingOn() {
			return errors.New("memory pooling is off; enable it under Settings first")
		}
		endpoints, err := c.startHelpers(ctx, id, pool, build)
		if err != nil {
			return err
		}
		ml.RPC = endpoints
	}
	// 2. model files (and the draft model's, for speculative decoding)
	if err := c.ensureModelOnNode(ctx, id, nc, m); err != nil {
		return err
	}
	if ml.Draft != "" {
		c.mu.Lock()
		dm := c.models[ml.Draft]
		c.mu.Unlock()
		if dm == nil || dm.Status != "ready" {
			return fmt.Errorf("the draft model %s is not ready in the library", ml.Draft)
		}
		if err := c.ensureModelOnNode(ctx, id, nc, dm); err != nil {
			return fmt.Errorf("draft model: %w", err)
		}
	}
	// 3. load
	c.setNodeOp(id, OpStatus{Op: "model.load", Target: ml.Name, State: "loading"})
	if err := nc.send("model.load", ml); err != nil {
		return err
	}
	return c.waitNodeOp(ctx, id, "model.load", ml.Name, 20*time.Minute)
}

// ensureModelOnNode gets a library model's files onto the node: from another
// node first (checked against the controller's hash), else from Hugging Face
// or the controller.
func (c *Controller) ensureModelOnNode(ctx context.Context, id string, nc *nodeConn, m *ModelRec) error {
	// 2. model files: from another node first, checked against the controller's hash
	if !c.nodeHasModel(id, m.Name) {
		if err := c.copyFromPeers(ctx, id, m); err != nil && ctx.Err() == nil {
			log.Printf("node %s: copy of %s from other nodes failed (%v); using the usual route", id, m.Name, err)
		}
	}
	if !c.nodeHasModel(id, m.Name) && m.Storage == "direct" {
		files := make([]ModelFetchFile, len(m.Files))
		for i, f := range m.Files {
			files[i] = ModelFetchFile{ModelFile: f, URL: hfURL(m.Repo, m.HFFiles[i])}
		}
		c.setNodeOp(id, OpStatus{Op: "model.fetch", Target: m.Name, State: "downloading", BytesTotal: totalBytes(m.Files)})
		if err := nc.send("model.fetch", ModelFetch{Name: m.Name, Files: files, Token: c.hfToken(), Reclaim: c.reclaimFor(id, m.Name)}); err != nil {
			return err
		}
		if err := c.waitNodeOp(ctx, id, "model.fetch", m.Name, 24*time.Hour); err != nil {
			return fmt.Errorf("downloading the model on the node: %w", err)
		}
	}
	if !c.nodeHasModel(id, m.Name) && m.Storage != "direct" {
		c.setNodeOp(id, OpStatus{Op: "model.push", Target: m.Name, State: "downloading", BytesTotal: totalBytes(m.Files)})
		if err := nc.send("model.push", ModelPush{Name: m.Name, Files: m.Files, Reclaim: c.reclaimFor(id, m.Name)}); err != nil {
			return err
		}
		if err := c.waitNodeOp(ctx, id, "model.push", m.Name, 24*time.Hour); err != nil {
			return fmt.Errorf("copying the model: %w", err)
		}
	}
	return nil
}

func totalBytes(fs []ModelFile) int64 {
	var t int64
	for _, f := range fs {
		t += f.Bytes
	}
	return t
}

func (c *Controller) sendToNode(w http.ResponseWriter, id, typ string, body any) {
	c.mu.Lock()
	nc := c.conns[id]
	c.mu.Unlock()
	if nc == nil {
		writeJSON(w, 409, map[string]string{"error": "node is offline"})
		return
	}
	if err := nc.send(typ, body); err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleUnloadModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var pool []string
	c.mu.Lock()
	if p := c.pipelines[id]; p != nil {
		p.cancel() // stop a setup still in progress
	}
	if n := c.nodes[id]; n != nil {
		n.LoadPrefs = nil
		pool, n.Pool = n.Pool, nil
		c.saveNodesLocked()
	}
	c.mu.Unlock()
	c.releaseHelpers(pool, nil)
	c.sendToNode(w, id, "model.unload", struct{}{})
}

func (c *Controller) handleRemoveNodeModel(w http.ResponseWriter, r *http.Request) {
	c.sendToNode(w, r.PathValue("id"), "model.remove", ModelRemove{Name: r.PathValue("name")})
}

// handleCleanupNodeModels deletes several stored models from one node's disk.
// The node decides what is in use and keeps those.
func (c *Controller) handleCleanupNodeModels(w http.ResponseWriter, r *http.Request) {
	var req ModelCleanup
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || (len(req.Names) == 0 && !req.Incomplete) {
		writeJSON(w, 400, map[string]string{"error": "nothing selected"})
		return
	}
	c.sendToNode(w, r.PathValue("id"), "model.cleanup", req)
}

// handleListModels returns the catalog, the controller's model store and the
// llama.cpp builds it holds.
