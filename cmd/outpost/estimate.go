package main

// Time estimate on the plan card: "about 9 min on 4 nodes". Before approval the
// controller simulates the dispatcher over the nodes that would do the work: each
// subtask goes, once its dependencies are done, to the slot where it would finish
// first (hard subtasks only to the strongest tier, as routing.go does), taking
// expected output tokens divided by the node's measured speed plus a fixed
// overhead for reading the prompt. Review adds a second pass, tests add a run.
// Fix rounds are not included: nobody knows yet whether the tests will fail.
//
// Expected output per file: the current size of a file that already exists in
// the repo, otherwise a guess by difficulty. A node with no measured speed
// counts as slowSpeedGuess and the estimate is marked rough.

import (
	"os"
	"path/filepath"
)

const (
	callOverheadSec = 20.0 // reading the prompt, starting the call, saving files
	slowSpeedGuess  = 8.0  // tokens per second for a node that was never measured
	testRunSec      = 60.0 // one test run on an exec node
	bytesPerToken   = 3.5
)

var newFileTokens = map[string]float64{"easy": 600, "normal": 1000, "hard": 1600}

type subEstimate struct {
	Seconds  float64 `json:"seconds"`
	NodeName string  `json:"node_name,omitempty"`
}

type planEstimate struct {
	Seconds      float64                `json:"seconds"`       // first pass: writing, review and one test run
	WriteSeconds float64                `json:"write_seconds"` // until the last subtask is written
	Nodes        int                    `json:"nodes"`         // nodes that get at least one subtask
	Rough        bool                   `json:"rough"`         // some node has no measured speed yet
	Subtasks     map[string]subEstimate `json:"subtasks"`
}

type estSlot struct {
	id, name string
	tier     int
	speed    float64
	free     float64
}

// estimatePlan returns nil when there is no plan or no node that could do the work.
func (c *Controller) estimatePlan(t *Task) *planEstimate {
	t.mu.Lock()
	if t.Plan == nil || len(t.Plan.Subtasks) == 0 {
		t.mu.Unlock()
		return nil
	}
	subs := append([]PlanSubtask(nil), t.Plan.Subtasks...)
	test := t.Plan.TestCommand != ""
	review := t.Review
	t.mu.Unlock()
	repo := t.repoDir()

	ids := c.taskWorkers(t)
	if len(ids) == 0 {
		return nil
	}
	scores := c.scoreNodes(ids)
	var slots []*estSlot
	maxTier := 0
	c.mu.Lock()
	for _, s := range scores {
		name := s.ID
		if n := c.nodes[s.ID]; n != nil {
			name = displayName(n)
		}
		for i := 0; i < c.slotsLocked(s.ID); i++ {
			slots = append(slots, &estSlot{id: s.ID, name: name, tier: s.Tier, speed: s.Speed})
		}
		maxTier = max(maxTier, s.Tier)
	}
	c.mu.Unlock()
	return simulatePlan(subs, slots, maxTier, review, test, func(rel string) int64 {
		p, err := safeJoin(repo, rel)
		if err != nil {
			return -1
		}
		fi, err := os.Stat(filepath.Clean(p))
		if err != nil || fi.IsDir() {
			return -1
		}
		return fi.Size()
	})
}

// simulatePlan is the pure part of estimatePlan, for tests. size returns a
// file's current size in bytes, or -1 if it does not exist yet.
func simulatePlan(subs []PlanSubtask, slots []*estSlot, maxTier int, review, test bool, size func(string) int64) *planEstimate {
	if len(slots) == 0 {
		return nil
	}
	est := &planEstimate{Subtasks: map[string]subEstimate{}}
	own := make([]*estSlot, len(slots))
	for i, s := range slots {
		cp := *s
		own[i] = &cp
	}
	slots = own
	for _, s := range slots {
		if s.speed <= 0 {
			s.speed, est.Rough = slowSpeedGuess, true
		}
	}
	finish := map[string]float64{}
	used := map[string]bool{}
	pending := append([]PlanSubtask(nil), subs...)
	for len(pending) > 0 {
		progressed := false
		for i := 0; i < len(pending); i++ {
			st := pending[i]
			ready, ok := 0.0, true
			for _, d := range st.DependsOn {
				f, done := finish[d]
				if !done && known(subs, d) {
					ok = false
					break
				}
				ready = max(ready, f)
			}
			if !ok {
				continue
			}
			tokens := 0.0
			for _, f := range st.Files {
				if b := size(f); b >= 0 {
					tokens += float64(b) / bytesPerToken * 1.1
				} else {
					tokens += newFileTokens[normDifficulty(st.Difficulty)]
				}
			}
			if st.Kind == "investigate" {
				tokens = 3 * newFileTokens["normal"] // several tool calls, a report, no files
			}
			candidates := slots
			if normDifficulty(st.Difficulty) == "hard" {
				candidates = nil
				for _, s := range slots {
					if s.tier >= maxTier {
						candidates = append(candidates, s)
					}
				}
			}
			var best *estSlot
			var bestEnd, bestDur float64
			for _, s := range candidates {
				dur := callOverheadSec + tokens/s.speed
				if review && len(slots) > 1 {
					dur += callOverheadSec + 0.5*tokens/s.speed // a second node reads and maybe fixes it; same speed assumed
				}
				end := max(s.free, ready) + dur
				if best == nil || end < bestEnd || (end == bestEnd && s.speed > best.speed) {
					best, bestEnd, bestDur = s, end, dur
				}
			}
			best.free = bestEnd
			finish[st.ID] = bestEnd
			used[best.id] = true
			est.Subtasks[st.ID] = subEstimate{Seconds: bestDur, NodeName: best.name}
			est.WriteSeconds = max(est.WriteSeconds, bestEnd)
			pending = append(pending[:i], pending[i+1:]...)
			i--
			progressed = true
		}
		if !progressed { // a dependency cycle; validatePlan refuses these, so just stop
			break
		}
	}
	est.Seconds = est.WriteSeconds
	if test {
		est.Seconds += testRunSec
	}
	est.Nodes = len(used)
	return est
}

func known(subs []PlanSubtask, id string) bool {
	for _, s := range subs {
		if s.ID == id {
			return true
		}
	}
	return false
}
