package main

// Node benchmarks and smart routing (roadmap row 5).
//
// Each node with a loaded model gets a short benchmark once the model is ready;
// real subtasks then refine its speed figure. Models have a tier (Small, Medium,
// Large) and plan subtasks a difficulty (easy, normal, hard). chooseNode and
// pickReviewer turn those into placement decisions and are pure so they can be
// tested without a cluster.

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Model tiers. Higher is stronger.
const (
	tierSmall  = 1
	tierMedium = 2
	tierLarge  = 3
)

// hardWait is how long a hard subtask waits for the strongest model to be free
// before it takes any free node.
const hardWait = 2 * time.Minute

func tierName(t int) string {
	switch t {
	case tierSmall:
		return "small"
	case tierMedium:
		return "medium"
	case tierLarge:
		return "large"
	}
	return ""
}

// tierFromName parses "small", "medium" or "large" (any case); 0 if unknown.
func tierFromName(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "small":
		return tierSmall
	case "medium":
		return tierMedium
	case "large":
		return tierLarge
	}
	return 0
}

var paramsRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9.])(\d+(?:\.\d+)?)b(?:[^a-z]|$)`)

// guessTier estimates a model's strength from the parameter count in its name
// (30b, 3.2b ...), or from its file size when the name has none (a Q4 file is
// roughly 0.6 GB per billion parameters).
func guessTier(name string, bytes int64) int {
	params := 0.0
	if m := paramsRe.FindStringSubmatch(name); m != nil {
		params, _ = strconv.ParseFloat(m[1], 64)
	} else if bytes > 0 {
		params = float64(bytes) / 0.6e9
	}
	switch {
	case params == 0:
		return tierMedium
	case params < 9:
		return tierSmall
	case params < 25:
		return tierMedium
	}
	return tierLarge
}

// normDifficulty maps the planner's tag to easy, normal or hard (default normal).
func normDifficulty(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "easy":
		return "easy"
	case "hard":
		return "hard"
	}
	return "normal"
}

// NodeBench is a node's measured speed with its current model.
type NodeBench struct {
	Model     string    `json:"model"`
	GenTPS    float64   `json:"gen_tps"`              // generation speed from the benchmark
	PromptTPS float64   `json:"prompt_tps,omitempty"` // rough prompt speed from the benchmark
	TokPerSec float64   `json:"tok_per_s"`            // rolling average, seeded by the benchmark and updated by real subtasks
	Samples   int       `json:"samples"`              // real subtasks in the average
	At        time.Time `json:"at"`
}

// nodeScore is what routing knows about a worker.
type nodeScore struct {
	ID    string
	Tier  int
	Speed float64 // tokens per second, 0 if unknown
}

func betterNode(a, b nodeScore) bool { // stronger tier, then faster, then ID
	if a.Tier != b.Tier {
		return a.Tier > b.Tier
	}
	if a.Speed != b.Speed {
		return a.Speed > b.Speed
	}
	return a.ID < b.ID
}

func bestOf(ns []nodeScore, better func(a, b nodeScore) bool) string {
	if len(ns) == 0 {
		return ""
	}
	sorted := append([]nodeScore(nil), ns...)
	sort.Slice(sorted, func(i, j int) bool { return better(sorted[i], sorted[j]) })
	return sorted[0].ID
}

// chooseNode picks the node for a subtask from the free workers, or "" to wait.
// all is every ready worker, busy or not; waited is how long the subtask has
// been ready.
func chooseNode(difficulty string, free, all []nodeScore, waited time.Duration) string {
	if len(free) == 0 {
		return ""
	}
	switch difficulty {
	case "hard":
		bestFree, bestAll := 0, 0
		for _, n := range free {
			if n.Tier > bestFree {
				bestFree = n.Tier
			}
		}
		for _, n := range all {
			if n.Tier > bestAll {
				bestAll = n.Tier
			}
		}
		if bestFree < bestAll && waited < hardWait {
			return ""
		}
		return bestOf(free, betterNode)
	case "easy":
		// Fastest free node; on equal speed the weaker one, so strong nodes stay free.
		return bestOf(free, func(a, b nodeScore) bool {
			if a.Speed != b.Speed {
				return a.Speed > b.Speed
			}
			if a.Tier != b.Tier {
				return a.Tier < b.Tier
			}
			return a.ID < b.ID
		})
	}
	// Normal: tier counts most, speed breaks ties and can lift a fast node over a
	// slow one a tier up only by less than one tier's worth.
	maxSpeed := 0.0
	for _, n := range free {
		if n.Speed > maxSpeed {
			maxSpeed = n.Speed
		}
	}
	score := func(n nodeScore) float64 {
		s := float64(n.Tier)
		if maxSpeed > 0 {
			s += 1.5 * n.Speed / maxSpeed
		}
		return s
	}
	best := free[0]
	for _, n := range free[1:] {
		if score(n) > score(best) || score(n) == score(best) && n.ID < best.ID {
			best = n
		}
	}
	return best.ID
}

// pickReviewer chooses a reviewer among the free nodes other than the writer:
// one at least as strong as the writer when there is one (the fastest of them),
// otherwise the best free node.
func pickReviewer(writer nodeScore, free []nodeScore) string {
	var strong []nodeScore
	for _, n := range free {
		if n.Tier >= writer.Tier {
			strong = append(strong, n)
		}
	}
	if len(strong) > 0 {
		return bestOf(strong, func(a, b nodeScore) bool {
			if a.Speed != b.Speed {
				return a.Speed > b.Speed
			}
			return a.ID < b.ID
		})
	}
	return bestOf(free, betterNode)
}

// scoreNodes describes the given workers for routing.
func (c *Controller) scoreNodes(ids []string) []nodeScore {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]nodeScore, 0, len(ids))
	for _, id := range ids {
		out = append(out, c.scoreLocked(id))
	}
	return out
}

func (c *Controller) scoreLocked(id string) nodeScore {
	s := nodeScore{ID: id, Tier: tierMedium}
	hb := c.lastHB[id]
	if hb == nil || hb.LoadedModel == "" {
		return s
	}
	s.Tier = c.modelTierLocked(hb.LoadedModel, c.loadedModelBytesLocked(id))
	if n := c.nodes[id]; n != nil && n.Bench != nil && n.Bench.Model == hb.LoadedModel {
		s.Speed = n.Bench.TokPerSec
	}
	return s
}

// modelTierLocked is the tier set on the Models tab, or the guess.
func (c *Controller) modelTierLocked(name string, bytes int64) int {
	if m := c.models[name]; m != nil {
		if t := tierFromName(m.Tier); t != 0 {
			return t
		}
	}
	return guessTier(name, bytes)
}

func (c *Controller) isReserved(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reserved[id]
}

func (c *Controller) freeScores(scores []nodeScore, skip string) []nodeScore {
	var free []nodeScore
	for _, s := range scores {
		if s.ID != skip && !c.isReserved(s.ID) {
			free = append(free, s)
		}
	}
	return free
}

// recordSpeed folds a real subtask's tokens per second into the node's figure.
func (c *Controller) recordSpeed(id string, tps float64) {
	if tps <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n, hb := c.nodes[id], c.lastHB[id]
	if n == nil || hb == nil || hb.LoadedModel == "" {
		return
	}
	if n.Bench == nil || n.Bench.Model != hb.LoadedModel {
		n.Bench = &NodeBench{Model: hb.LoadedModel, TokPerSec: tps, At: time.Now()}
	} else if n.Bench.Samples == 0 && n.Bench.GenTPS == 0 {
		n.Bench.TokPerSec = tps
	} else {
		n.Bench.TokPerSec = 0.7*n.Bench.TokPerSec + 0.3*tps
	}
	n.Bench.Samples++
	c.saveNodesLocked()
}

// ---------- benchmark ----------

const benchRetry = 10 * time.Minute

// benchLoop benchmarks nodes whose model has not been measured yet.
func (c *Controller) benchLoop() {
	tried := map[string]time.Time{} // node|model -> last attempt
	for range time.Tick(20 * time.Second) {
		var todo []string
		c.mu.Lock()
		for id := range c.conns {
			hb := c.lastHB[id]
			if hb == nil || hb.Paused || !hb.InferenceRdy || hb.InferenceBusy != 0 || hb.LoadedModel == "" || c.pipelines[id] != nil || c.reserved[id] {
				continue
			}
			if n := c.nodes[id]; n != nil && n.Bench != nil && n.Bench.Model == hb.LoadedModel {
				continue
			}
			if time.Since(tried[id+"|"+hb.LoadedModel]) < benchRetry {
				continue
			}
			tried[id+"|"+hb.LoadedModel] = time.Now()
			todo = append(todo, id)
		}
		c.mu.Unlock()
		for _, id := range todo {
			if c.tryReserve(id) {
				go func(id string) {
					defer c.releaseNode(id)
					c.benchmarkNode(context.Background(), id)
				}(id)
			}
		}
	}
}

var benchPrompt = strings.Repeat("The quick brown fox jumps over the lazy dog while the cluster hums quietly. ", 75) +
	"\n\nIn about 200 words, describe how a hash map works."

func (c *Controller) benchmarkNode(ctx context.Context, id string) {
	c.mu.Lock()
	hb := c.lastHB[id]
	model := ""
	if hb != nil {
		model = hb.LoadedModel
	}
	c.mu.Unlock()
	if model == "" {
		return
	}
	temp := 0.0
	spec := InferenceSpec{Messages: []ChatMessage{{Role: "user", Content: benchPrompt}},
		Params: InferenceParams{Temperature: &temp, MaxTokens: 256}}
	start := time.Now()
	out, err := c.runInferenceJob(ctx, id, spec, "Benchmark", 5*time.Minute)
	if err != nil || out.TokensPerSec <= 0 {
		return
	}
	b := &NodeBench{Model: model, GenTPS: out.TokensPerSec, TokPerSec: out.TokensPerSec, At: time.Now()}
	// Whatever time was not spent generating went to reading the prompt.
	if gen := float64(out.OutputTokens) / out.TokensPerSec; out.PromptTokens > 0 {
		if p := time.Since(start).Seconds() - gen; p > 0.05 {
			b.PromptTPS = float64(out.PromptTokens) / p
		}
	}
	c.mu.Lock()
	if n := c.nodes[id]; n != nil {
		n.Bench = b
		c.saveNodesLocked()
	}
	c.mu.Unlock()
}

// ---------- API ----------

// handleSetModelTier sets a model's tier ("small", "medium", "large") or clears it ("").
func (c *Controller) handleSetModelTier(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tier string `json:"tier"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Tier != "" && tierFromName(req.Tier) == 0 {
		writeJSON(w, 400, map[string]string{"error": "tier must be small, medium or large"})
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.models[r.PathValue("name")]
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "no such model"})
		return
	}
	m.Tier = strings.ToLower(req.Tier)
	c.saveModelLocked(m)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleSetDifficulty changes a subtask's difficulty while the plan awaits approval.
func (c *Controller) handleSetDifficulty(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	var req struct {
		Subtask    string `json:"subtask"`
		Difficulty string `json:"difficulty"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Status != "awaiting_approval" || t.Plan == nil {
		writeJSON(w, 409, map[string]string{"error": "the plan can only be changed before you approve it"})
		return
	}
	d := normDifficulty(req.Difficulty)
	found := false
	set := func(p *Plan) {
		for i := range p.Subtasks {
			if p.Subtasks[i].ID == req.Subtask {
				p.Subtasks[i].Difficulty = d
				found = true
			}
		}
	}
	set(t.Plan)
	for i := len(t.Messages) - 1; i >= 0; i-- {
		if t.Messages[i].Kind == "plan" && t.Messages[i].Plan != nil {
			set(t.Messages[i].Plan)
			break
		}
	}
	if !found {
		writeJSON(w, 404, map[string]string{"error": "no such subtask"})
		return
	}
	t.saveLocked()
	writeJSON(w, 200, map[string]bool{"ok": true})
}
