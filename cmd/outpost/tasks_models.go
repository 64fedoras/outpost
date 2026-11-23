package main

// Per-node models for tasks. When the owner approves a plan with "Pick models
// for each node" on, the controller loads the best-fit model (best_fit.go) on
// each node that allows it (NodeRec.TaskModels) before any subtask starts, and
// puts each node's previous model back when the task's run ends. A failed load
// leaves the node on its current model; the task carries on with what is
// loaded. Models are only switched before work starts, never during a subtask.
//
// pickTaskModels is pure so the choice can be tested without a cluster.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// defaultModelCapGB is the most a task may download for its models unless
// the owner sets another cap on the plan card.
const defaultModelCapGB = 60

// taskModelWait is how long a task waits for its model loads before it starts
// with whatever is loaded.
const taskModelWait = 3 * time.Hour

// How long a load waits for other tasks' calls on the node to end: before a
// task starts (then the node keeps its model) and when putting a model back.
const (
	prepareFreeWait = 10 * time.Minute
	restoreFreeWait = 30 * time.Minute
)

// ModelSwap is one model change a task made (or proposes) on one node.
type ModelSwap struct {
	Node     string     `json:"node"`
	NodeName string     `json:"node_name,omitempty"`
	Model    string     `json:"model"`
	Label    string     `json:"label,omitempty"`
	Bytes    int64      `json:"bytes"`
	Download bool       `json:"download"` // not on the node yet
	Prev     string     `json:"prev,omitempty"`
	PrevLoad *ModelLoad `json:"prev_load,omitempty"` // how the previous model was loaded, for the restore
	State    string     `json:"state,omitempty"`     // "" (proposed) | loading | loaded | failed | restored | kept
	Error    string     `json:"error,omitempty"`
	Skip     string     `json:"skip,omitempty"` // why the node keeps its model (preview only)
}

// pickCand is what pickTaskModels knows about one node.
type pickCand struct {
	Node      string
	OptIn     bool
	Busy      bool   // pooled, a helper, already setting up a model
	Current   string // loaded model, "" if none
	CurBytes  int64
	Best      string // best-fit pick, "" if none
	BestBytes int64
	OnNode    bool // the pick's files are already on the node
}

// pickTaskModels chooses which nodes switch model for a task with work
// subtasks. A node switches only when it opted in, is free, has a pick that
// differs from (and is not smaller than) what it has loaded. The biggest
// picks come first, at most one node per subtask, and downloads stop at the
// cap. Returns the swaps and, for the preview, why each other node keeps its
// model.
func pickTaskModels(cands []pickCand, work int, capBytes int64) (swaps []ModelSwap, kept []ModelSwap) {
	sorted := append([]pickCand(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].BestBytes != sorted[j].BestBytes {
			return sorted[i].BestBytes > sorted[j].BestBytes
		}
		return sorted[i].Node < sorted[j].Node
	})
	var downloaded int64
	used := 0
	keep := func(c pickCand, why string) {
		kept = append(kept, ModelSwap{Node: c.Node, Model: c.Current, Prev: c.Current, Skip: why})
	}
	for _, c := range sorted {
		switch {
		case !c.OptIn:
			keep(c, "not allowed to change model")
		case c.Busy:
			keep(c, "busy setting up or pooling a model")
		case c.Best == "":
			keep(c, "no model fits")
		case c.Best == c.Current:
			keep(c, "already has the best model")
		case c.Current != "" && c.BestBytes <= c.CurBytes:
			keep(c, "its model is already as big")
		case used >= work:
			keep(c, "not needed: fewer subtasks than nodes")
		case !c.OnNode && capBytes > 0 && downloaded+c.BestBytes > capBytes:
			keep(c, "download would pass the cap")
		default:
			if !c.OnNode {
				downloaded += c.BestBytes
			}
			used++
			swaps = append(swaps, ModelSwap{Node: c.Node, Model: c.Best, Bytes: c.BestBytes, Download: !c.OnNode, Prev: c.Current})
		}
	}
	return swaps, kept
}

// helperNodesLocked are nodes lending their memory to another node's model.
func (c *Controller) helperNodesLocked() map[string]bool {
	h := map[string]bool{}
	for _, n := range c.nodes {
		for _, id := range n.Pool {
			h[id] = true
		}
	}
	return h
}

// taskModelPlan works out the swaps for a task now. c.mu must not be held.
func (c *Controller) taskModelPlan(t *Task) (swaps, kept []ModelSwap) {
	t.mu.Lock()
	work := 0
	if t.Plan != nil {
		work = len(t.Plan.Subtasks)
	}
	label := t.NodeLabel
	capGB := t.ModelCapGB
	t.mu.Unlock()
	if capGB <= 0 {
		capGB = defaultModelCapGB
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	helpers := c.helperNodesLocked()
	var cands []pickCand
	labels := map[string]string{}
	for id, n := range c.nodes {
		if c.conns[id] == nil || (label != "" && !hasLabel(n.Labels, label)) {
			continue
		}
		hb := c.lastHB[id]
		pc := pickCand{Node: id, OptIn: n.taskModelsOn(), Busy: c.pipelines[id] != nil || len(n.Pool) > 0 || helpers[id]}
		if hb != nil && hb.Paused {
			pc.Busy = true
		}
		if hb != nil {
			pc.Current = hb.LoadedModel
			pc.CurBytes = c.loadedModelBytesLocked(id)
		}
		if pc.OptIn && !pc.Busy {
			opts, _ := c.loadOptionsFor(id, 1)
			for _, o := range opts {
				if o.Rank == 1 {
					pc.Best, pc.BestBytes, pc.OnNode = o.Name, o.Bytes, o.OnNode
					labels[o.Name] = o.Label
				}
			}
		}
		cands = append(cands, pc)
	}
	swaps, kept = pickTaskModels(cands, work, int64(capGB*(1<<30)))
	for i := range swaps {
		s := &swaps[i]
		s.NodeName, s.Label = c.nodeLabelLocked(s.Node), labels[s.Model]
		if n := c.nodes[s.Node]; n != nil && n.LoadPrefs != nil && n.LoadPrefs.Name == s.Prev {
			pl := *n.LoadPrefs
			s.PrevLoad = &pl
		}
	}
	for i := range kept {
		kept[i].NodeName = c.nodeLabelLocked(kept[i].Node)
	}
	return swaps, kept
}

// nodeLabelLocked is the node's display name. c.mu must be held.
func (c *Controller) nodeLabelLocked(id string) string {
	if n := c.nodes[id]; n != nil && n.Name != "" {
		return n.Name
	}
	return id
}

// handleTaskModelPlan shows the plan card what "Pick models for each node"
// would do right now.
func (c *Controller) handleTaskModelPlan(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	swaps, kept := c.taskModelPlan(t)
	if swaps == nil {
		swaps = []ModelSwap{}
	}
	if kept == nil {
		kept = []ModelSwap{}
	}
	writeJSON(w, 200, map[string]any{"swaps": swaps, "kept": kept, "default_cap_gb": defaultModelCapGB})
}

// prepareTaskModels loads the chosen models before the task's work starts.
// It does nothing unless the task asked for it, and nothing again on a resume
// (the swaps are saved with the task).
func (c *Controller) prepareTaskModels(ctx context.Context, t *Task) {
	t.mu.Lock()
	want := t.AutoModels && t.ModelSwaps == nil
	t.mu.Unlock()
	if !want {
		return
	}
	swaps, _ := c.taskModelPlan(t)
	t.mu.Lock()
	t.ModelSwaps = append([]ModelSwap{}, swaps...)
	t.saveLocked()
	t.mu.Unlock()
	if len(swaps) == 0 {
		t.logf("info", "Pick models for each node: every node keeps its model.")
		return
	}
	wctx, cancel := context.WithTimeout(ctx, taskModelWait)
	defer cancel()
	var wg sync.WaitGroup
	for i := range swaps {
		i := i
		s := swaps[i]
		what := "Loading"
		if s.Download {
			what = fmt.Sprintf("Downloading %s and loading", humanBytes(s.Bytes))
		}
		t.logf("info", "%s %s on %s for this task.", what, s.Model, c.nodeLabel(s.Node))
		c.setSwapState(t, i, "loading", "")
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := c.loadForTask(wctx, s.Node, s.Model)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				t.logf("warn", "Could not load %s on %s (%v); it keeps its current model.", s.Model, c.nodeLabel(s.Node), err)
				c.setSwapState(t, i, "failed", err.Error())
				return
			}
			c.setSwapState(t, i, "loaded", "")
		}()
	}
	wg.Wait()
}

func (c *Controller) setSwapState(t *Task, i int, state, errText string) {
	t.mu.Lock()
	if i < len(t.ModelSwaps) {
		t.ModelSwaps[i].State, t.ModelSwaps[i].Error = state, errText
		t.saveLocked()
	}
	t.mu.Unlock()
}

// restoreTaskModels puts back each node's previous model after the run, when
// the task asked for it. A node whose model was changed by someone else in the
// meantime is left alone.
func (c *Controller) restoreTaskModels(t *Task) {
	t.mu.Lock()
	if !t.AutoModels || t.KeepModels || len(t.ModelSwaps) == 0 {
		t.mu.Unlock()
		return
	}
	swaps := append([]ModelSwap(nil), t.ModelSwaps...)
	t.mu.Unlock()
	for i, s := range swaps {
		if s.State != "loaded" {
			continue
		}
		c.mu.Lock()
		hb := c.lastHB[s.Node]
		cur := ""
		if hb != nil {
			cur = hb.LoadedModel
		}
		c.mu.Unlock()
		if cur != s.Model {
			c.setSwapState(t, i, "kept", "")
			continue // changed by hand since; leave it
		}
		var err error
		if s.Prev == "" {
			err = c.unloadForTask(s.Node)
		} else {
			ml := ModelLoad{Name: s.Prev}
			if s.PrevLoad != nil {
				ml = *s.PrevLoad
			}
			ctx, cancel := context.WithTimeout(context.Background(), taskModelWait)
			err = c.loadWithPrefs(ctx, s.Node, ml, restoreFreeWait)
			cancel()
		}
		if err != nil {
			t.logf("warn", "Could not put %s back on %s: %v", orNothing(s.Prev), c.nodeLabel(s.Node), err)
			c.setSwapState(t, i, "loaded", err.Error())
			continue
		}
		t.logf("info", "Put %s back on %s.", orNothing(s.Prev), c.nodeLabel(s.Node))
		c.setSwapState(t, i, "restored", "")
	}
}

func orNothing(s string) string {
	if s == "" {
		return "no model"
	}
	return s
}

// loadForTask gets a model into the library if needed (catalog models are
// added with direct storage: the node downloads them) and loads it.
func (c *Controller) loadForTask(ctx context.Context, id, name string) error {
	if err := c.ensureInLibrary(ctx, name); err != nil {
		return err
	}
	c.mu.Lock()
	m := c.models[name]
	c.mu.Unlock()
	if m == nil {
		return errors.New("model not in the library")
	}
	return c.loadWithPrefs(ctx, id, ModelLoad{Name: name, Ctx: m.Ctx}, prepareFreeWait)
}

// loadWithPrefs runs a load the way the Load button does and waits for it.
func (c *Controller) loadWithPrefs(ctx context.Context, id string, ml ModelLoad, freeWait time.Duration) error {
	// Wait for calls running on the node (other tasks) to end; once the
	// pipeline is set the node takes no new work until the load is done.
	deadline := time.Now().Add(freeWait)
	for {
		c.mu.Lock()
		if c.reserved[id] == 0 || time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		c.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	// c.mu is held here.
	if c.reserved[id] > 0 {
		c.mu.Unlock()
		return errors.New("the node stayed busy with other work")
	}
	if ctx.Err() != nil {
		c.mu.Unlock()
		return ctx.Err()
	}
	pctx, cancel := context.WithCancel(ctx)
	p := &pipeline{cancel}
	n := c.nodes[id]
	m := c.models[ml.Name]
	switch {
	case n == nil || c.conns[id] == nil:
		c.mu.Unlock()
		cancel()
		return errors.New("node is offline")
	case c.pipelines[id] != nil:
		c.mu.Unlock()
		cancel()
		return errors.New("the node is already setting up a model")
	case m == nil || m.Status != "ready":
		c.mu.Unlock()
		cancel()
		return fmt.Errorf("%s is not ready in the library", ml.Name)
	}
	if ml.Ctx <= 0 {
		ml.Ctx = m.Ctx
	}
	c.pipelines[id] = p
	prefs := ml
	n.LoadPrefs = &prefs
	c.saveNodesLocked()
	c.mu.Unlock()
	defer c.endPipeline(id, p)
	err := c.loadPipeline(pctx, id, ml)
	if err != nil && pctx.Err() == nil {
		c.setNodeOp(id, OpStatus{Op: "model.load", Target: ml.Name, State: "failed", Error: err.Error()})
	}
	return err
}

// unloadForTask unloads a node's model (it had none before the task).
func (c *Controller) unloadForTask(id string) error {
	for deadline := time.Now().Add(restoreFreeWait); ; time.Sleep(time.Second) {
		c.mu.Lock()
		busy := c.reserved[id] > 0
		c.mu.Unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("the node stayed busy with other work")
		}
	}
	c.mu.Lock()
	nc := c.conns[id]
	if n := c.nodes[id]; n != nil {
		n.LoadPrefs = nil
		c.saveNodesLocked()
	}
	c.mu.Unlock()
	if nc == nil {
		return errors.New("node is offline")
	}
	return nc.send("model.unload", struct{}{})
}

// ensureInLibrary adds a catalog model to the library (direct storage) and
// waits until its files are resolved.
func (c *Controller) ensureInLibrary(ctx context.Context, name string) error {
	c.mu.Lock()
	m := c.models[name]
	if m == nil || m.Status == "failed" {
		ce := catalogEntry(name)
		if ce == nil {
			c.mu.Unlock()
			return fmt.Errorf("%s is neither in the library nor in the catalog", name)
		}
		m = modelFromCatalog(ce, "direct")
		c.models[name] = m
		c.startModelWorkLocked(m)
	}
	c.mu.Unlock()
	for {
		c.mu.Lock()
		st, e := m.Status, m.Error
		c.mu.Unlock()
		switch st {
		case "ready":
			return nil
		case "failed":
			return fmt.Errorf("adding %s to the library failed: %s", name, e)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func catalogEntry(id string) *CatalogEntry {
	cat := catalogNow()
	for i := range cat {
		if cat[i].ID == id {
			return &cat[i]
		}
	}
	return nil
}

// modelFromCatalog is a new library record for a catalog entry.
func modelFromCatalog(ce *CatalogEntry, storage string) *ModelRec {
	m := &ModelRec{Added: time.Now(), Ctx: ce.Ctx, Storage: storage, Name: ce.ID, Label: ce.Label, Repo: ce.Repo}
	m.HFFiles = append([]string{}, ce.Files...)
	if ce.MMProj != "" {
		m.HFFiles = append(m.HFFiles, ce.MMProj)
	}
	return m
}

// runWithTaskModels runs the task's execution phase between loading its
// models and putting the previous ones back. The restore only happens once the
// task has ended (done, failed or cancelled): a task paused for an answer, or
// stopped by a controller restart, keeps its models for when it goes on.
func (c *Controller) runWithTaskModels(ctx context.Context, t *Task) {
	c.prepareTaskModels(ctx, t)
	c.executeTask(ctx, t)
	if ctx.Err() != nil {
		time.Sleep(2 * time.Second) // let a cancel mark the task cancelled first
	}
	t.mu.Lock()
	st := t.Status
	t.mu.Unlock()
	if taskEnded(st) {
		go c.restoreTaskModels(t)
	}
}

func taskEnded(status string) bool {
	switch status {
	case "done", "failed", "cancelled":
		return true
	}
	return false
}
