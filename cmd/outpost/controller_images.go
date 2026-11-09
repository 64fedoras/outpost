package main

// Controller side of image generation (design/image-video-generation.md):
// the stable-diffusion.cpp runtime mirror, the model catalog with per-node fit,
// the queue that spreads pictures over fit nodes, and the stored results with
// their retention. Pictures are kept here only; nodes delete theirs once the
// controller has them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	genDefaultDays = 30
	genDefaultGB   = 5
	genMaxCount    = 16
	genMaxVideos   = 4
	genAttempts    = 2
)

// GenItem is one picture of a generation.
type GenItem struct {
	Index    int       `json:"index"`
	Seed     int64     `json:"seed"`
	Status   string    `json:"status"` // queued | preparing | running | done | failed | cancelled
	Node     string    `json:"node,omitempty"`
	NodeName string    `json:"node_name,omitempty"`
	Step     int       `json:"step,omitempty"`
	Steps    int       `json:"steps,omitempty"`
	Seconds  float64   `json:"seconds,omitempty"`
	Bytes    int64     `json:"bytes,omitempty"`
	Ext      string    `json:"ext,omitempty"` // stored file type; empty means png
	Error    string    `json:"error,omitempty"`
	Attempts int       `json:"attempts,omitempty"`
	Started  time.Time `json:"started,omitempty"`
	jobID    string
	cancel   context.CancelFunc
}

// GenRec is one request: a prompt and its pictures.
type GenRec struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"`
	Model    string     `json:"model"`
	Label    string     `json:"label"`
	Prompt   string     `json:"prompt"`
	Negative string     `json:"negative,omitempty"`
	Width    int        `json:"width"`
	Height   int        `json:"height"`
	Steps    int        `json:"steps"`
	CFG      float64    `json:"cfg"`
	Sampler  string     `json:"sampler,omitempty"`
	Count    int        `json:"count"`
	Frames   int        `json:"frames,omitempty"` // video
	FPS      int        `json:"fps,omitempty"`    // video
	Source   string     `json:"source"`           // web | api:<token name>
	Keep     bool       `json:"keep,omitempty"`
	Created  time.Time  `json:"created"`
	Items    []*GenItem `json:"items"`
}

// status is the summary of its items.
func (g *GenRec) status() string {
	var queued, active, done, failed, cancelled int
	for _, it := range g.Items {
		switch it.Status {
		case "queued":
			queued++
		case "preparing", "running":
			active++
		case "done":
			done++
		case "failed":
			failed++
		case "cancelled":
			cancelled++
		}
	}
	switch {
	case active > 0:
		return "running"
	case queued > 0:
		return "queued"
	case done == len(g.Items):
		return "done"
	case done > 0:
		return "partial"
	case cancelled == len(g.Items):
		return "cancelled"
	}
	return "failed"
}

func (g *GenRec) finished() bool {
	s := g.status()
	return s != "running" && s != "queued"
}

// genSettings are the owner's choices (Settings, Images and video card).
type genSettings struct {
	RetentionDays int                  `json:"retention_days"`        // 0 = keep forever
	MaxGB         int                  `json:"max_gb"`                // 0 = no cap
	APIEnabled    bool                 `json:"api_enabled,omitempty"` // allow /v1/images/generations for tokens
	PauseForTasks bool                 `json:"pause_for_tasks,omitempty"` // start no picture or clip while a coding task is working (gen_priority.go)
	Accepted      map[string]time.Time `json:"accepted,omitempty"`
	Observed      map[string]int64     `json:"observed,omitempty"` // compute buffer a node measured: model|node|WxH
}

type genStore struct {
	mu       sync.Mutex
	recs     map[string]*GenRec
	settings genSettings
	busy     map[string]bool // nodes running a picture now
	prep     map[string]*sync.Mutex
	kick     chan struct{}
	hf       map[string]hfCacheEntry
	sdRel    runtimeCache
	prepStub func(ctx context.Context, nodeID string, d *ImageModelDef) error // tests: stands in for the runtime install and model download
	argsStub func(d *ImageModelDef) []GenArg                                  // tests: stands in for the Hugging Face file lookup
}

type hfCacheEntry struct {
	at   time.Time
	tree []hfTreeEntry
}

func (c *Controller) genDir() string { return filepath.Join(c.dataDir, "generations") }

func (c *Controller) loadGens() {
	g := &genStore{recs: map[string]*GenRec{}, busy: map[string]bool{}, prep: map[string]*sync.Mutex{},
		kick: make(chan struct{}, 1), hf: map[string]hfCacheEntry{}, settings: genSettings{RetentionDays: genDefaultDays, MaxGB: genDefaultGB}}
	g.sdRel.locks = map[string]*sync.Mutex{}
	c.gens = g
	if b, err := os.ReadFile(filepath.Join(c.genDir(), "settings.json")); err == nil {
		_ = json.Unmarshal(b, &g.settings)
	}
	entries, _ := os.ReadDir(c.genDir())
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(c.genDir(), e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		var rec GenRec
		if json.Unmarshal(b, &rec) != nil || rec.ID != e.Name() {
			continue
		}
		for _, it := range rec.Items {
			// What was running when the controller stopped did not finish; the owner can run it again.
			if it.Status == "preparing" || it.Status == "running" || it.Status == "queued" {
				it.Status, it.Error = "failed", "interrupted by a controller restart"
			}
		}
		g.recs[rec.ID] = &rec
	}
}

func (c *Controller) saveGenSettingsLocked() {
	_ = os.MkdirAll(c.genDir(), 0o755)
	b, _ := json.MarshalIndent(c.gens.settings, "", "  ")
	_ = writeFileAtomic(filepath.Join(c.genDir(), "settings.json"), b, 0o600)
}

func (c *Controller) saveGen(rec *GenRec) {
	dir := filepath.Join(c.genDir(), rec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("generation %s: %v", rec.ID, err)
		return
	}
	c.gens.mu.Lock()
	if c.gens.recs[rec.ID] != rec { // deleted while a picture was still finishing
		c.gens.mu.Unlock()
		os.RemoveAll(dir)
		return
	}
	b, _ := json.MarshalIndent(rec, "", "  ")
	c.gens.mu.Unlock()
	_ = writeFileAtomic(filepath.Join(dir, "meta.json"), b, 0o644)
}

func genFile(dir string, index int) string { return filepath.Join(dir, strconv.Itoa(index)+".png") }

// genItemFile is the stored file of a finished item (a picture, or a clip).
func genItemFile(dir string, it *GenItem) string {
	ext := it.Ext
	if ext == "" {
		ext = "png"
	}
	return filepath.Join(dir, strconv.Itoa(it.Index)+"."+ext)
}

// ---------- stable-diffusion.cpp runtime mirror ----------

func (c *Controller) sdReleases(ctx context.Context, force bool) ([]ghRelease, error) {
	rc := &c.gens.sdRel
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if !force && rc.releases != nil && time.Since(rc.checked) < time.Hour {
		return rc.releases, nil
	}
	base := "https://api.github.com"
	if u := os.Getenv("OUTPOST_GITHUB_API"); u != "" {
		base = strings.TrimRight(u, "/")
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/repos/leejet/stable-diffusion.cpp/releases?per_page=20", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "outpost/"+Version)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub: %s", resp.Status)
	}
	var rels []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return nil, err
	}
	rc.releases, rc.checked = rels, time.Now()
	return rels, nil
}

// sdNewest finds the newest release with a build for variant.
func sdNewest(rels []ghRelease, variant string) (build, name, url string, size int64) {
	for _, r := range rels {
		for _, a := range r.Assets {
			if sdAssetMatch(a.Name, variant) {
				return r.TagName, a.Name, a.URL, a.Size
			}
		}
	}
	return "", "", "", 0
}

// ensureSDRuntime makes sure a stable-diffusion.cpp build for variant is on the
// controller. It is kept next to the llama.cpp builds under the key "sd-<variant>".
func (c *Controller) ensureSDRuntime(ctx context.Context, variant string) (*RuntimeRec, error) {
	key := "sd-" + variant
	rc := &c.gens.sdRel
	rc.mu.Lock()
	lk := rc.locks[key]
	if lk == nil {
		lk = &sync.Mutex{}
		rc.locks[key] = lk
	}
	rc.mu.Unlock()
	lk.Lock()
	defer lk.Unlock()

	c.mu.Lock()
	have := c.runtimes[key]
	c.mu.Unlock()
	if have != nil {
		return have, nil // a build is mirrored already; updating it is a deliberate step, not a side effect of a picture
	}
	rels, err := c.sdReleases(ctx, false)
	if err != nil {
		return nil, fmt.Errorf("stable-diffusion.cpp release check failed: %w", err)
	}
	build, name, url, size := sdNewest(rels, variant)
	if build == "" {
		return nil, fmt.Errorf("no stable-diffusion.cpp release has a %s build", variant)
	}
	dir := filepath.Join(c.runtimeDir(), key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	log.Printf("downloading stable-diffusion.cpp %s for %s (%s)", build, variant, humanBytes(size))
	sum, err := downloadResumable(ctx, &http.Client{}, url, "", filepath.Join(dir, name), size, func(int64) {})
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	rec := &RuntimeRec{Variant: key, Build: build, Archive: name, SHA256: sum, Bytes: size, Downloaded: time.Now()}
	b, _ := json.MarshalIndent(rec, "", "  ")
	if err := writeFileAtomic(filepath.Join(dir, "runtime.json"), b, 0o644); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.runtimes[key] = rec
	c.mu.Unlock()
	return rec, nil
}

// ---------- model files ----------

// resolveImageFiles looks up sizes and checksums on Hugging Face so the node can
// download and verify the files itself.
func (c *Controller) resolveImageFiles(ctx context.Context, d *ImageModelDef) ([]ModelFetchFile, error) {
	var out []ModelFetchFile
	seen := map[string]bool{}
	for _, f := range d.Files {
		c.gens.mu.Lock()
		ce, ok := c.gens.hf[f.Repo]
		c.gens.mu.Unlock()
		if !ok || time.Since(ce.at) > 10*time.Minute {
			tree, err := hfTree(ctx, f.Repo, c.hfToken())
			if err != nil {
				return nil, err
			}
			ce = hfCacheEntry{at: time.Now(), tree: tree}
			c.gens.mu.Lock()
			c.gens.hf[f.Repo] = ce
			c.gens.mu.Unlock()
		}
		paths := make([]string, 0, len(ce.tree))
		by := map[string]hfTreeEntry{}
		for _, e := range ce.tree {
			if e.Type == "file" {
				paths = append(paths, e.Path)
				by[e.Path] = e
			}
		}
		p, err := pickRepoFile(paths, f.Match)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Repo, err)
		}
		e := by[p]
		name := filepath.Base(p)
		if seen[name] {
			return nil, fmt.Errorf("two files of %s are both called %s", d.ID, name)
		}
		seen[name] = true
		sum := ""
		if e.LFS != nil {
			sum = e.LFS.OID
		}
		size := e.Size
		if e.LFS != nil && e.LFS.Size > 0 {
			size = e.LFS.Size
		}
		out = append(out, ModelFetchFile{ModelFile: ModelFile{Path: name, SHA256: sum, Bytes: size}, URL: hfURL(f.Repo, p)})
	}
	return out, nil
}

func (c *Controller) prepLock(nodeID string) *sync.Mutex {
	c.gens.mu.Lock()
	defer c.gens.mu.Unlock()
	m := c.gens.prep[nodeID]
	if m == nil {
		m = &sync.Mutex{}
		c.gens.prep[nodeID] = m
	}
	return m
}

// ensureSDOnNode installs stable-diffusion.cpp and downloads the model files on a
// node when they are missing. One preparation per node at a time.
func (c *Controller) ensureSDOnNode(ctx context.Context, id string, d *ImageModelDef) (err error) {
	// A failure before the node reports back (download error, node offline, send
	// error) used to leave the node's "preparing" or "downloading" status in place, so
	// the page kept showing a spinner. Mark any unfinished status failed instead.
	defer func() {
		if err == nil {
			return
		}
		c.mu.Lock()
		st := c.nodeOps[id]
		stuck := st != nil && (st.Op == "runtime.sd" || st.Op == "model.fetch") && st.State != "failed" && st.State != "done" && st.State != "ready"
		if stuck {
			op := st.OpStatus
			op.State, op.Error = "failed", err.Error()
			c.recordNodeOpLocked(id, op)
		}
		c.mu.Unlock()
	}()
	lk := c.prepLock(id)
	lk.Lock()
	defer lk.Unlock()
	c.mu.Lock()
	var hello Hello
	if n := c.nodes[id]; n != nil && n.Hello != nil {
		hello = *n.Hello
	}
	nc := c.conns[id]
	c.mu.Unlock()
	if nc == nil {
		return fmt.Errorf("%s is offline", c.nodeLabel(id))
	}
	if !hasFeature(hello.Features, "node.gen") {
		return fmt.Errorf("%s runs an older Outpost that cannot make images; update it first", c.nodeLabel(id))
	}
	variant, note := sdVariant(&hello)
	if variant == "" {
		return fmt.Errorf("no stable-diffusion.cpp build for %s: %s", c.nodeLabel(id), note)
	}
	if hello.SDRuntime == nil || hello.SDRuntime.Variant != variant {
		c.setNodeOp(id, OpStatus{Op: "runtime.sd", Target: variant, State: "preparing"})
		rt, err := c.ensureSDRuntime(ctx, variant)
		if err != nil {
			return err
		}
		if hello.SDRuntime == nil || hello.SDRuntime.Build != rt.Build || hello.SDRuntime.Variant != variant {
			if err := nc.send("runtime.install", RuntimeInstall{Runtime: "sd", Build: rt.Build, Variant: variant, Archive: rt.Archive, SHA256: rt.SHA256, Bytes: rt.Bytes}); err != nil {
				return err
			}
			if err := c.waitNodeOp(ctx, id, "runtime.sd", variant, 30*time.Minute); err != nil {
				return fmt.Errorf("installing stable-diffusion.cpp on %s: %w", c.nodeLabel(id), err)
			}
		}
	}
	if !c.nodeHasModel(id, d.Dir()) {
		fetch, err := c.resolveImageFiles(ctx, d)
		if err != nil {
			return fmt.Errorf("looking up %s: %w", d.Label, err)
		}
		c.setNodeOp(id, OpStatus{Op: "model.fetch", Target: d.Dir(), State: "downloading", BytesTotal: totalFetch(fetch)})
		if err := nc.send("model.fetch", ModelFetch{Name: d.Dir(), Files: fetch, Token: c.hfToken(), Reclaim: c.reclaimFor(id, d.Dir())}); err != nil {
			return err
		}
		if err := c.waitNodeOp(ctx, id, "model.fetch", d.Dir(), 24*time.Hour); err != nil {
			return fmt.Errorf("downloading %s on %s: %w", d.Label, c.nodeLabel(id), err)
		}
	}
	return nil
}

func totalFetch(fs []ModelFetchFile) int64 {
	var t int64
	for _, f := range fs {
		t += f.Bytes
	}
	return t
}

func hasFeature(list []string, f string) bool {
	for _, x := range list {
		if x == f {
			return true
		}
	}
	return false
}

// genFilesFor maps a catalog entry's files to the names a node stored. The node's
// manifest has the base names resolved from the repository, so the controller
// reads them from the node's model list when it can, else from a fresh lookup.
func (c *Controller) genArgs(ctx context.Context, d *ImageModelDef) ([]GenArg, error) {
	fetch, err := c.resolveImageFiles(ctx, d)
	if err != nil {
		return nil, err
	}
	args := make([]GenArg, len(d.Files))
	for i, f := range d.Files {
		args[i] = GenArg{Flag: f.Flag, File: fetch[i].Path}
	}
	return args, nil
}

// ---------- fit ----------

// GenNode is one node's view for the Create page and the queue.
type GenNode struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Fit      string `json:"fit"`
	Offload  bool   `json:"offload,omitempty"`
	Online   bool   `json:"online"`
	Paused   bool   `json:"paused,omitempty"`
	Busy     bool   `json:"busy,omitempty"`
	Ready    bool   `json:"ready"`     // runtime and model are on the node
	HasModel bool   `json:"has_model"` // the model files are on the node
	CanRun   bool   `json:"can_run"`
	Why      string `json:"why,omitempty"` // when it cannot run now
	VRAM     int64  `json:"vram"`
}

// genNodesLocked evaluates every node for a model at a size. c.mu must be held.
func (c *Controller) genNodesLocked(d *ImageModelDef, w, h int) []GenNode {
	return c.genNodesFramesLocked(d, w, h, 0)
}

// genNodesFramesLocked is genNodesLocked for a clip of frames frames (0 for a picture).
func (c *Controller) genNodesFramesLocked(d *ImageModelDef, w, h, frames int) []GenNode {
	var out []GenNode
	for id, n := range c.nodes {
		gn := GenNode{ID: id, Name: displayName(n), Online: c.conns[id] != nil, Fit: fitNo}
		hello := n.Hello
		if hello == nil {
			gn.Why = "no hardware report yet"
			out = append(out, gn)
			continue
		}
		variant, note := sdVariant(hello)
		if variant == "" {
			gn.Why = note
			out = append(out, gn)
			continue
		}
		vram, ram := nodeMemory(hello)
		if !strings.HasSuffix(variant, "vulkan") {
			vram = 0
		}
		gn.VRAM = vram
		var obs int64
		if frames == 0 { // a clip's buffer depends on its length, which the key does not carry
			obs = c.gens.settings.Observed[genObsKey(d.ID, id, w, h)]
		}
		gn.Fit, gn.Offload = genFit(d, w, h, frames, obs, vram, ram)
		gn.HasModel = false
		for _, m := range hello.Models {
			if m.Name == d.Dir() {
				gn.HasModel = true
			}
		}
		gn.Ready = hello.SDRuntime != nil && hello.SDRuntime.Variant == variant && gn.HasModel
		hb := c.lastHB[id]
		gn.Paused = hb != nil && hb.Paused
		gn.Busy = c.gens.busy[id] || (hb != nil && hb.InferenceBusy != 0) || c.pipelines[id] != nil || c.lendingToLocked(id) != ""
		switch {
		case !gn.Online:
			gn.Why = "offline"
		case gn.Fit == fitNo:
			gn.Why = "the model does not fit in this node's memory at this size"
		case gn.Paused:
			gn.Why = "paused"
		case !hasFeature(hello.Features, "node.gen"):
			gn.Why = "update this node's Outpost to make images"
		case frames > 0 && !hasFeature(hello.Features, "node.video"):
			gn.Why = "update this node's Outpost to make video"
		default:
			gn.CanRun = true
			if gn.Busy {
				gn.Why = "busy"
			}
		}
		out = append(out, gn)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func genObsKey(model, node string, w, h int) string {
	return fmt.Sprintf("%s|%s|%dx%d", model, node, w, h)
}

// pickGenNode chooses the node for the next picture: not busy, fit, with the model
// already there first, then the best fit level, then the most GPU memory.
func pickGenNode(nodes []GenNode) *GenNode {
	var best *GenNode
	better := func(a, b *GenNode) bool { // a better than b
		if a.Ready != b.Ready {
			return a.Ready
		}
		if a.HasModel != b.HasModel {
			return a.HasModel
		}
		if ra, rb := fitRank(a.Fit), fitRank(b.Fit); ra != rb {
			return ra < rb
		}
		if a.VRAM != b.VRAM {
			return a.VRAM > b.VRAM
		}
		return a.Name < b.Name
	}
	for i := range nodes {
		n := &nodes[i]
		if !n.CanRun || n.Busy {
			continue
		}
		if best == nil || better(n, best) {
			best = n
		}
	}
	return best
}

// ---------- the queue ----------

func (c *Controller) genKick() {
	select {
	case c.gens.kick <- struct{}{}:
	default:
	}
}

// genLoop hands queued pictures to free nodes and applies retention.
func (c *Controller) genLoop() {
	t := time.NewTicker(3 * time.Second)
	prune := time.NewTicker(time.Hour)
	defer t.Stop()
	defer prune.Stop()
	for {
		select {
		case <-t.C:
		case <-c.gens.kick:
		case <-prune.C:
			c.pruneGens()
			continue
		}
		c.dispatchGens()
	}
}

func (c *Controller) dispatchGens() {
	type todo struct {
		rec  *GenRec
		it   *GenItem
		node GenNode
		def  *ImageModelDef
	}
	var start []todo
	c.gens.mu.Lock()
	pause := c.gens.settings.PauseForTasks
	c.gens.mu.Unlock()
	if pause && c.anyTaskWorking() {
		return // picture work waits for the coding tasks (gen_priority.go)
	}
	c.mu.Lock()
	c.gens.mu.Lock()
	ids := make([]string, 0, len(c.gens.recs))
	for id := range c.gens.recs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return c.gens.recs[ids[i]].Created.Before(c.gens.recs[ids[j]].Created) })
	for _, id := range ids {
		rec := c.gens.recs[id]
		def := imageModel(rec.Model)
		if def == nil {
			continue
		}
		var nodes []GenNode
		for _, it := range rec.Items {
			if it.Status != "queued" {
				continue
			}
			if nodes == nil {
				nodes = c.genNodesFramesLocked(def, rec.Width, rec.Height, rec.Frames)
			}
			n := pickGenNode(nodes)
			if n == nil {
				break
			}
			c.gens.busy[n.ID] = true
			it.Status, it.Node, it.NodeName, it.Started = "preparing", n.ID, n.Name, time.Now()
			start = append(start, todo{rec, it, *n, def})
			for i := range nodes {
				if nodes[i].ID == n.ID {
					nodes[i].Busy = true
				}
			}
		}
	}
	c.gens.mu.Unlock()
	c.mu.Unlock()
	for _, t := range start {
		go c.runGenItem(t.rec, t.it, t.node, t.def)
	}
}

// failGenItem ends a picture. A retryable failure goes back in the queue once.
func (c *Controller) failGenItem(rec *GenRec, it *GenItem, node string, err error, retry bool) {
	c.gens.mu.Lock()
	delete(c.gens.busy, node)
	it.cancel = nil
	if it.Status == "cancelled" {
		c.gens.mu.Unlock()
		c.saveGen(rec)
		c.genKick()
		return
	}
	it.Attempts++
	if retry && it.Attempts < genAttempts {
		it.Status = "queued"
		log.Printf("generation %s picture %d: %v; trying again", rec.ID, it.Index, err)
	} else {
		it.Status, it.Error = "failed", err.Error()
	}
	c.gens.mu.Unlock()
	c.saveGen(rec)
	c.genKick()
}

func (c *Controller) runGenItem(rec *GenRec, it *GenItem, gn GenNode, def *ImageModelDef) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	c.gens.mu.Lock()
	it.cancel = cancel
	it.Steps = rec.Steps
	it.Step = 0
	c.gens.mu.Unlock()

	prep := c.ensureSDOnNode
	if c.gens.prepStub != nil {
		prep = c.gens.prepStub
	}
	if err := prep(ctx, gn.ID, def); err != nil {
		c.failGenItem(rec, it, gn.ID, err, false)
		return
	}
	var args []GenArg
	if c.gens.argsStub != nil {
		args = c.gens.argsStub(def)
	} else {
		var err error
		if args, err = c.genArgs(ctx, def); err != nil {
			c.failGenItem(rec, it, gn.ID, err, false)
			return
		}
	}
	spec := GenSpec{Kind: rec.Kind, Model: rec.Model, Dir: def.Dir(), Files: args, Offload: gn.Offload, Prompt: rec.Prompt,
		Negative: rec.Negative, Width: rec.Width, Height: rec.Height, Steps: rec.Steps, CFG: rec.CFG, Sampler: rec.Sampler, Seed: it.Seed,
		Frames: rec.Frames, FPS: rec.FPS}
	b, _ := json.Marshal(spec)
	job := &JobDispatch{Kind: "generate", TimeoutS: int((90 * time.Minute).Seconds()), Spec: b, JobID: newID("job")}
	c.gens.mu.Lock()
	it.jobID, it.Status = job.JobID, "running"
	c.gens.mu.Unlock()
	ch, err := c.dispatchJob(gn.ID, job, rec.Kind+": "+shortText(rec.Prompt, 60))
	if err != nil {
		c.failGenItem(rec, it, gn.ID, err, true)
		return
	}
	var res *JobResult
	select {
	case res = <-ch:
	case <-ctx.Done():
		c.cancelJob(job.JobID)
		c.failGenItem(rec, it, gn.ID, errors.New("cancelled"), false)
		return
	}
	if res.Status != "succeeded" {
		var e error = errors.New("the node reported: " + res.Status)
		retry := false
		if res.Error != nil {
			e, retry = res.Error, res.Error.Retryable
		}
		if res.Status == "cancelled" {
			c.gens.mu.Lock()
			it.Status = "cancelled"
			c.gens.mu.Unlock()
		}
		c.failGenItem(rec, it, gn.ID, e, retry)
		return
	}
	dir := filepath.Join(c.genDir(), rec.ID)
	tmp := filepath.Join(dir, ".fetch-"+strconv.Itoa(it.Index))
	arts, err := c.fetchJobFiles(ctx, gn.ID, job.JobID, res.Files, tmp, nil)
	if err != nil || len(arts) == 0 {
		if err == nil {
			err = errors.New("the node returned no picture")
		}
		os.RemoveAll(tmp)
		c.failGenItem(rec, it, gn.ID, err, true)
		return
	}
	var out GenOutput
	_ = json.Unmarshal(res.Output, &out)
	ext := genExt(rec.Kind, out.Format)
	if ext == "png" {
		ext = ""
	}
	final := genItemFile(dir, &GenItem{Index: it.Index, Ext: ext})
	if err := os.Rename(filepath.Join(tmp, arts[0].Path), final); err != nil {
		os.RemoveAll(tmp)
		c.failGenItem(rec, it, gn.ID, err, false)
		return
	}
	os.RemoveAll(tmp)
	c.gens.mu.Lock()
	delete(c.gens.busy, gn.ID)
	it.Status, it.Seconds, it.Bytes, it.Step, it.cancel, it.Ext = "done", out.Seconds, arts[0].Bytes, it.Steps, nil, ext
	if out.ComputeBytes > 0 && rec.Kind == genImage {
		if c.gens.settings.Observed == nil {
			c.gens.settings.Observed = map[string]int64{}
		}
		c.gens.settings.Observed[genObsKey(rec.Model, gn.ID, rec.Width, rec.Height)] = out.ComputeBytes
		c.saveGenSettingsLocked()
	}
	c.gens.mu.Unlock()
	c.saveGen(rec)
	c.genKick()
}

func shortText(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// genJobOutput records a node's "step total" progress line for the picture it belongs to.
func (c *Controller) genJobOutput(o JobOutput) {
	if o.Stream != "progress" || c.gens == nil {
		return
	}
	f := strings.Fields(o.Data)
	if len(f) < 2 {
		return
	}
	step, _ := strconv.Atoi(f[len(f)-2])
	total, _ := strconv.Atoi(f[len(f)-1])
	c.gens.mu.Lock()
	defer c.gens.mu.Unlock()
	for _, rec := range c.gens.recs {
		for _, it := range rec.Items {
			if it.jobID == o.JobID {
				it.Step, it.Steps = step, total
				return
			}
		}
	}
}

// ---------- retention ----------

// pruneGens deletes finished results older than the retention days, then the
// oldest ones until the folder is under the size cap. Kept results stay.
func (c *Controller) pruneGens() {
	c.gens.mu.Lock()
	st := c.gens.settings
	var cand []*GenRec
	for _, r := range c.gens.recs {
		if r.Keep || !r.finished() {
			continue
		}
		cand = append(cand, r)
	}
	c.gens.mu.Unlock()
	sort.Slice(cand, func(i, j int) bool { return cand[i].Created.Before(cand[j].Created) })
	del := map[string]bool{}
	if st.RetentionDays > 0 {
		cut := time.Now().AddDate(0, 0, -st.RetentionDays)
		for _, r := range cand {
			if r.Created.Before(cut) {
				del[r.ID] = true
			}
		}
	}
	if st.MaxGB > 0 {
		limit := int64(st.MaxGB) << 30
		total := dirBytes(c.genDir())
		for _, r := range cand {
			if total <= limit {
				break
			}
			if !del[r.ID] {
				del[r.ID] = true
				total -= dirBytes(filepath.Join(c.genDir(), r.ID))
			}
		}
	}
	for id := range del {
		c.deleteGen(id)
	}
	if len(del) > 0 {
		log.Printf("image results: removed %d old generation(s) under the retention settings", len(del))
	}
}

func (c *Controller) deleteGen(id string) {
	c.gens.mu.Lock()
	delete(c.gens.recs, id)
	c.gens.mu.Unlock()
	os.RemoveAll(filepath.Join(c.genDir(), id))
}

// ---------- creating a request ----------

// GenRequest is the body of POST /api/images.
type GenRequest struct {
	Model    string  `json:"model"`
	Prompt   string  `json:"prompt"`
	Negative string  `json:"negative"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Steps    int     `json:"steps"`
	CFG      float64 `json:"cfg"`
	Sampler  string  `json:"sampler"`
	Count    int     `json:"count"`
	Frames   int     `json:"frames"` // video
	FPS      int     `json:"fps"`    // video
	Seed     *int64  `json:"seed"`
	Node     string  `json:"node"` // optional: only this node
}

// newGen validates a request and queues its pictures.
func (c *Controller) newGen(req GenRequest, source string) (*GenRec, error) {
	def := imageModel(req.Model)
	if def == nil {
		return nil, fmt.Errorf("unknown model %q", req.Model)
	}
	if req.Width == 0 {
		req.Width = def.Size
	}
	if req.Height == 0 {
		req.Height = def.DefaultHeight()
	}
	if def.Kind == genVideo {
		if req.Frames == 0 {
			req.Frames = def.Frames
		}
		if req.FPS == 0 {
			req.FPS = def.FPS
		}
		if req.Count > genMaxVideos {
			return nil, fmt.Errorf("at most %d clips at a time", genMaxVideos)
		}
	} else {
		req.Frames, req.FPS = 0, 0
	}
	if req.Steps == 0 {
		req.Steps = def.Steps
	}
	if req.CFG == 0 {
		req.CFG = def.CFG
	}
	if req.Sampler == "" {
		req.Sampler = def.Sampler
	}
	if req.Count == 0 {
		req.Count = 1
	}
	if req.Count < 0 || req.Count > genMaxCount {
		return nil, fmt.Errorf("count must be 1 to %d", genMaxCount)
	}
	if !def.Negative {
		req.Negative = ""
	}
	check := GenSpec{Kind: def.Kind, Prompt: req.Prompt, Negative: req.Negative, Width: req.Width, Height: req.Height, Steps: req.Steps, CFG: req.CFG, Sampler: req.Sampler,
		Frames: req.Frames, FPS: req.FPS, Files: []GenArg{{Flag: "-m", File: "x"}}}
	if err := check.validate(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.gens.mu.Lock()
	if def.Restricted && c.gens.settings.Accepted[def.ID].IsZero() {
		c.gens.mu.Unlock()
		c.mu.Unlock()
		return nil, fmt.Errorf("%s has a restricted licence (%s); accept it first", def.Label, def.License)
	}
	nodes := c.genNodesFramesLocked(def, req.Width, req.Height, req.Frames)
	c.gens.mu.Unlock()
	c.mu.Unlock()
	any := false
	for _, n := range nodes {
		if n.CanRun && n.Fit != fitNo {
			any = true
		}
	}
	if !any {
		return nil, fmt.Errorf("no online node can run %s at %dx%d; check Nodes (needs an up-to-date node with enough memory)", def.Label, req.Width, req.Height)
	}
	base := rand.Int63n(1 << 40)
	if req.Seed != nil && *req.Seed >= 0 {
		base = *req.Seed
	}
	rec := &GenRec{ID: newID("gen"), Kind: def.Kind, Model: def.ID, Label: def.Label, Prompt: strings.TrimSpace(req.Prompt), Negative: req.Negative,
		Width: req.Width, Height: req.Height, Steps: req.Steps, CFG: req.CFG, Sampler: req.Sampler, Count: req.Count, Frames: req.Frames, FPS: req.FPS, Source: source, Created: time.Now()}
	for i := 0; i < req.Count; i++ {
		rec.Items = append(rec.Items, &GenItem{Index: i, Seed: base + int64(i), Status: "queued", Steps: req.Steps})
	}
	c.gens.mu.Lock()
	c.gens.recs[rec.ID] = rec
	c.gens.mu.Unlock()
	c.saveGen(rec)
	c.genKick()
	return rec, nil
}

func (c *Controller) cancelGen(id string) bool {
	c.gens.mu.Lock()
	rec := c.gens.recs[id]
	if rec == nil {
		c.gens.mu.Unlock()
		return false
	}
	var jobs []string
	var cancels []context.CancelFunc
	for _, it := range rec.Items {
		switch it.Status {
		case "queued":
			it.Status = "cancelled"
		case "preparing", "running":
			it.Status = "cancelled"
			if it.jobID != "" {
				jobs = append(jobs, it.jobID)
			}
			if it.cancel != nil {
				cancels = append(cancels, it.cancel)
			}
		}
	}
	c.gens.mu.Unlock()
	for _, j := range jobs {
		c.cancelJob(j)
	}
	for _, f := range cancels {
		f()
	}
	c.saveGen(rec)
	return true
}
