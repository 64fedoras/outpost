package main

// Model list, Hugging Face resolution, controller storage and release. Split out of controller_models.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"
)

func (c *Controller) handleListModels(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	models := make([]*ModelRec, 0, len(c.models))
	for _, m := range c.models {
		m.TierGuess = tierName(guessTier(m.Name, m.BytesTotal))
		models = append(models, m)
	}
	rts := make([]*RuntimeRec, 0, len(c.runtimes))
	for _, rt := range c.runtimes {
		rts = append(rts, rt)
	}
	fits := map[string][]NodeFit{}
	catFits := map[string][]NodeFit{}
	for _, m := range models {
		if m.Status == "ready" {
			fits[m.Name] = c.nodeFitsLocked(m.BytesTotal, m.Ctx)
		}
	}
	cat := catalogNow()
	for _, e := range cat {
		catFits[e.ID] = c.nodeFitsLocked(e.Bytes, e.Ctx)
	}
	recommended := map[string]LoadOption{}
	for id := range c.nodes {
		if c.conns[id] == nil {
			continue
		}
		if opts, _ := c.loadOptionsFor(id, 1); len(opts) > 0 && opts[0].Rank == 1 {
			recommended[id] = opts[0]
		}
	}
	c.mu.Unlock()
	sort.Slice(models, func(i, j int) bool { return models[i].Added.Before(models[j].Added) })
	sort.Slice(rts, func(i, j int) bool { return rts[i].Variant < rts[j].Variant })
	writeJSON(w, 200, map[string]any{"catalog": cat, "models": models, "runtimes": rts,
		"fits": fits, "catalog_fits": catFits, "suggested": suggestCoding(cat, catFits, 4), "recommended": recommended,
		"disk_free": diskFree(c.modelsStoreDir())})
}

// startModelWorkLocked resolves a direct model's files on Hugging Face, or
// downloads a controller-stored model. c.mu must be held.
func (c *Controller) startModelWorkLocked(m *ModelRec) {
	if m.Storage == "" {
		m.Storage = "controller"
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.Error = ""
	if m.Storage == "direct" {
		m.Status = "resolving"
		c.saveModelLocked(m)
		go c.resolveModel(ctx, m)
		return
	}
	m.Status = "downloading"
	c.saveModelLocked(m)
	go c.downloadModel(ctx, m)
}

// hfTreeEntry is one file from the Hugging Face tree API; large (LFS) files
// carry their SHA-256 in lfs.oid.
type hfTreeEntry struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	LFS  *struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	} `json:"lfs"`
}

func hfTree(ctx context.Context, repo, token string) ([]hfTreeEntry, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", hfBase()+"/api/models/"+repo+"/tree/main?recursive=true", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Hugging Face: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("Hugging Face refused %s (%s); the model may be gated: accept its license on huggingface.co and add a token in Settings", repo, resp.Status)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Hugging Face: %s for %s", resp.Status, repo)
	}
	var tree []hfTreeEntry
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		return nil, errors.New("Hugging Face: unexpected answer")
	}
	return tree, nil
}

// resolveModel looks up sizes and checksums so nodes can download and verify
// the files themselves. Nothing is stored on the controller.
func (c *Controller) resolveModel(ctx context.Context, m *ModelRec) {
	tree, err := hfTree(ctx, m.Repo, c.hfToken())
	var files []ModelFile
	var total int64
	if err == nil {
		byPath := map[string]hfTreeEntry{}
		for _, e := range tree {
			byPath[e.Path] = e
		}
		for _, f := range m.HFFiles {
			e, ok := byPath[f]
			if !ok {
				err = fmt.Errorf("%s is not in %s", f, m.Repo)
				break
			}
			mf := ModelFile{Path: path.Base(f), Bytes: e.Size}
			if e.LFS != nil {
				mf.SHA256, mf.Bytes = e.LFS.OID, e.LFS.Size
			}
			files = append(files, mf)
			total += mf.Bytes
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.models[m.Name] != m || m.Storage != "direct" {
		return // deleted or switched while resolving
	}
	if err != nil {
		m.Status, m.Error = "failed", err.Error()
	} else {
		m.Files, m.BytesTotal, m.BytesDone, m.Status = files, total, 0, "ready"
		go c.fillModelShape(m)
	}
	c.saveModelLocked(m)
}

// handleStoreModel downloads a direct model to the controller too.
func (c *Controller) handleStoreModel(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.models[r.PathValue("name")]
	if !ok || m.Storage != "direct" {
		writeJSON(w, 409, map[string]string{"error": "that model is already stored on the controller"})
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.Storage = "controller"
	c.startModelWorkLocked(m)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleUnstoreModel deletes a model's files from the controller but keeps it
// in the library; nodes that need it then download it from Hugging Face.
func (c *Controller) handleUnstoreModel(w http.ResponseWriter, r *http.Request) {
	if err := c.unstoreModel(r.PathValue("name")); err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) unstoreModel(name string) error {
	c.mu.Lock()
	m, ok := c.models[name]
	if !ok || m.Storage == "direct" {
		c.mu.Unlock()
		return errors.New("that model is not stored on the controller")
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.Storage = "direct"
	c.startModelWorkLocked(m) // re-resolve: refreshes sizes and checksums
	c.mu.Unlock()
	go func() {
		time.Sleep(time.Second) // let a cancelled download close its file
		dir := filepath.Join(c.modelsStoreDir(), name)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.Name() != "model.json" {
				os.RemoveAll(filepath.Join(dir, e.Name()))
			}
		}
	}()
	return nil
}

// pickReleaseNode chooses where a released model should live: an online node
// that already holds it, else the idle online node with the most free model
// disk that has room for it. pushNeeded is false in the first case.
func (c *Controller) pickReleaseNode(m *ModelRec, want string) (id string, pushNeeded bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	need := uint64(m.BytesTotal)
	var best string
	var bestFree uint64
	for nid, n := range c.nodes {
		if c.conns[nid] == nil || n.Hello == nil || (want != "" && nid != want) {
			continue
		}
		for _, h := range n.Hello.Models {
			if h.Name == m.Name {
				return nid, false, nil
			}
		}
		if c.pipelines[nid] != nil || n.Hello.ModelsFreeBytes < need+need/20 {
			continue
		}
		if best == "" || n.Hello.ModelsFreeBytes > bestFree {
			best, bestFree = nid, n.Hello.ModelsFreeBytes
		}
	}
	if best == "" {
		return "", false, errors.New("no online, idle node has room for this model; free space on a node or bring one online")
	}
	return best, true, nil
}

// handleReleaseModel moves a controller-stored model onto one node, then frees
// the controller's copy. The node stays a seed: other nodes copy from it over
// the LAN and only fall back to Hugging Face if it is gone.
func (c *Controller) handleReleaseModel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		Node string `json:"node"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	c.mu.Lock()
	m := c.models[name]
	c.mu.Unlock()
	if m == nil || m.Storage == "direct" || m.Status != "ready" {
		writeJSON(w, 409, map[string]string{"error": "that model is not stored (and ready) on the controller"})
		return
	}
	id, push, err := c.pickReleaseNode(m, req.Node)
	if err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	c.mu.Lock()
	nc := c.conns[id]
	nodeName := id
	if n := c.nodes[id]; n != nil && n.Hello != nil {
		nodeName = n.Hello.Hostname
	}
	if push && (nc == nil || c.pipelines[id] != nil) {
		c.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "that node is busy or offline"})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &pipeline{cancel}
	if push {
		c.pipelines[id] = p
	}
	files := append([]ModelFile(nil), m.Files...)
	c.mu.Unlock()
	if !push {
		cancel()
		if err := c.unstoreModel(name); err != nil {
			writeJSON(w, 409, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "node": nodeName, "pushed": false})
		return
	}
	go func() {
		defer c.endPipeline(id, p)
		c.setNodeOp(id, OpStatus{Op: "model.push", Target: name, State: "downloading", BytesTotal: totalBytes(files)})
		fail := func(err error) {
			log.Printf("release %s to node %s failed: %v (the controller keeps its copy)", name, id, err)
			c.setNodeOp(id, OpStatus{Op: "model.push", Target: name, State: "failed", Error: err.Error()})
		}
		if err := nc.send("model.push", ModelPush{Name: name, Files: files}); err != nil {
			fail(err)
			return
		}
		if err := c.waitNodeOp(ctx, id, "model.push", name, 24*time.Hour); err != nil {
			fail(err)
			return
		}
		if !c.nodeHasModel(id, name) {
			fail(errors.New("the node did not report the model after copying"))
			return
		}
		if err := c.unstoreModel(name); err != nil {
			fail(err)
		}
	}()
	writeJSON(w, 200, map[string]any{"ok": true, "node": nodeName, "pushed": true})
}
