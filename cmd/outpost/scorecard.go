package main

// Per-model scorecard: for every subtask that finished a task, which model
// wrote it and whether it came out clean (accepted on the first try, untouched
// by the reviewer, and the task's tests passed without a fix round). Shown by
// model and language on Settings, so a choice of model rests on real results
// rather than benchmark guesses. It only informs you; routing does not use it.
// Kept in scorecard.json in the controller's data folder; deleted tasks stay
// counted.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type scoreRow struct {
	Model     string `json:"model"`
	Language  string `json:"language"`
	Subtasks  int    `json:"subtasks"`
	Clean     int    `json:"clean"`
	Retries   int    `json:"retries"`      // extra attempts after the first
	ReviewFix int    `json:"review_fixed"` // the reviewer had to change the files
}

type scoreCard struct {
	mu   sync.Mutex
	rows map[string]*scoreRow
}

func (c *Controller) scorePath() string { return filepath.Join(c.dataDir, "scorecard.json") }

func (c *Controller) cardLog() *scoreCard {
	c.cardOnce.Do(func() {
		sc := &scoreCard{rows: map[string]*scoreRow{}}
		if b, err := os.ReadFile(c.scorePath()); err == nil {
			var list []*scoreRow
			if json.Unmarshal(b, &list) == nil {
				for _, r := range list {
					sc.rows[r.Model+"\x00"+r.Language] = r
				}
			}
		}
		c.card = sc
	})
	return c.card
}

// recordScorecard adds the not yet counted subtasks of a finished task.
func (c *Controller) recordScorecard(t *Task) {
	t.mu.Lock()
	lang := ""
	if t.Plan != nil {
		lang = strings.ToLower(strings.TrimSpace(t.Plan.Language))
	}
	roundClean := t.TestStatus != "failing" && t.FixRounds == 0
	type item struct {
		model             string
		clean             bool
		retries, reviewed int
	}
	var items []item
	for _, s := range t.Subtasks {
		if s.Scored || s.Model == "" || s.Status != "done" || s.ByPlanner {
			continue
		}
		s.Scored = true
		it := item{model: s.Model, clean: roundClean && s.Attempts <= 1 && !s.ReviewFix}
		if s.Attempts > 1 {
			it.retries = s.Attempts - 1
		}
		if s.ReviewFix {
			it.reviewed = 1
		}
		items = append(items, it)
	}
	if len(items) > 0 {
		t.saveLocked()
	}
	t.mu.Unlock()
	if len(items) == 0 {
		return
	}
	sc := c.cardLog()
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for _, it := range items {
		k := it.model + "\x00" + lang
		r := sc.rows[k]
		if r == nil {
			r = &scoreRow{Model: it.model, Language: lang}
			sc.rows[k] = r
		}
		r.Subtasks++
		if it.clean {
			r.Clean++
		}
		r.Retries += it.retries
		r.ReviewFix += it.reviewed
	}
	if b, err := json.Marshal(sc.list()); err == nil {
		_ = writeFileAtomic(c.scorePath(), b, 0o600)
	}
}

func (sc *scoreCard) list() []*scoreRow {
	out := make([]*scoreRow, 0, len(sc.rows))
	for _, r := range sc.rows {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return out[i].Language < out[j].Language
	})
	return out
}

func (c *Controller) handleScorecard(w http.ResponseWriter, r *http.Request) {
	sc := c.cardLog()
	sc.mu.Lock()
	rows := sc.list()
	sc.mu.Unlock()
	writeJSON(w, 200, map[string]any{"rows": rows})
}

// loadedModelName is the library model a node has loaded ("" if unknown).
func (c *Controller) loadedModelName(node string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if hb := c.lastHB[node]; hb != nil {
		return hb.LoadedModel
	}
	return ""
}
