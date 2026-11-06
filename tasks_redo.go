package main

// Redo one subtask (idea 18): on a task that has stopped, write one subtask
// again on the strongest free node, on a chosen node, or by the planner,
// optionally with a note added to its instructions, then run the tests again.
// The other subtasks and their files are kept.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// preferWait is how long a redo waits for the chosen node before any node may take it.
const preferWait = 10 * time.Minute

// preferredNode returns the node a redo asked for if it is free, "" to keep
// waiting, or after preferWait any free node.
func (c *Controller) preferredNode(t *Task, st *SubtaskState, free []nodeScore, waited time.Duration) string {
	t.mu.Lock()
	want := st.Prefer
	t.mu.Unlock()
	for _, s := range free {
		if s.ID == want {
			return want
		}
	}
	if waited > preferWait && len(free) > 0 {
		t.mu.Lock()
		st.Prefer = ""
		t.mu.Unlock()
		t.logf("warn", "%s is still not free after %s; %s goes to another node.", c.nodeLabel(want), preferWait, st.ID)
		return free[0].ID
	}
	return ""
}

// redoByPlanner has the planner write the subtask, in the background. It sets
// the subtask's state itself and reports nil to the dispatch loop (or
// errCancelled), so the loop does not fall back to the planner again.
func (c *Controller) redoByPlanner(ctx context.Context, t *Task, spec PlanSubtask, st *SubtaskState, report func(error)) {
	t.mu.Lock()
	now := time.Now()
	st.Status, st.Node, st.NodeName, st.Started, st.Error = "running", "", "the planner", &now, ""
	t.saveLocked()
	t.mu.Unlock()
	go func() {
		err := c.plannerWritesSubtask(ctx, t, spec)
		if errors.Is(err, errCancelled) {
			report(err)
			return
		}
		t.mu.Lock()
		end := time.Now()
		st.Ended = &end
		if err != nil {
			st.Status, st.Error = "failed", err.Error()
		} else {
			st.Status, st.ByPlanner, st.Error = "done", true, ""
		}
		t.saveLocked()
		t.mu.Unlock()
		report(nil)
	}()
}

// handleRedoSubtask: POST /api/tasks/{id}/subtasks/{sid}/redo
// {"node": "strongest" | "planner" | "<node id>", "note": "...", "cap_usd": 0}
func (c *Controller) handleRedoSubtask(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	var req struct {
		Node   string  `json:"node"`
		Note   string  `json:"note"`
		CapUSD float64 `json:"cap_usd"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	req.Node, req.Note = strings.TrimSpace(req.Node), strings.TrimSpace(req.Note)
	if req.Node == "" {
		req.Node = "strongest"
	}
	if req.Node != "strongest" && req.Node != "planner" {
		c.mu.Lock()
		_, known := c.nodes[req.Node]
		c.mu.Unlock()
		if !known {
			writeJSON(w, 400, map[string]string{"error": "unknown node"})
			return
		}
	}
	if len(req.Note) > 4000 {
		req.Note = req.Note[:4000]
	}
	sid := r.PathValue("sid")
	t.mu.Lock()
	status, planner, orModel := t.Status, t.Planner, t.ORModel
	ok := t.Plan != nil && t.Approved != nil
	var spec *PlanSubtask
	if ok {
		for i := range t.Plan.Subtasks {
			if t.Plan.Subtasks[i].ID == sid {
				spec = &t.Plan.Subtasks[i]
			}
		}
	}
	st := t.subtask(sid)
	t.mu.Unlock()
	switch {
	case status != "done" && status != "failed" && status != "cancelled":
		writeJSON(w, 409, map[string]string{"error": "stop the task or wait for it to finish first"})
		return
	case !ok || spec == nil || st == nil:
		writeJSON(w, 404, map[string]string{"error": "no such subtask in the approved plan"})
		return
	case spec.Kind == "investigate":
		writeJSON(w, 409, map[string]string{"error": "investigate steps cannot be redone on their own"})
		return
	}
	if err := c.checkPlanner(planner, orModel); err != nil && req.Node == "planner" {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	t.mu.Lock()
	st.Status, st.Node, st.NodeName, st.Error = "pending", "", "", ""
	st.Attempts, st.ByPlanner, st.Reviewer, st.ReviewFix, st.ReviewNote, st.BestOf = 0, false, "", false, "", ""
	st.Started, st.Ended = nil, nil
	st.Prefer, st.RedoNote = req.Node, req.Note
	t.applyRerunCap(req.CapUSD)
	t.Ended, t.Error, t.PausedFrom = nil, "", ""
	t.TestStatus, t.TestOutput, t.FixRounds = "", "", 0
	t.BuildStatus, t.BuildOutput = "", ""
	t.Status = "running"
	who := map[string]string{"strongest": "the strongest free node", "planner": "the planner"}[req.Node]
	if who == "" {
		who = c.nodeLabel(req.Node)
	}
	text := "Redoing " + spec.Title + " on " + who + ", then running the tests again."
	if req.Note != "" {
		text += " Your note: " + req.Note
	}
	t.addMsgLocked(TaskMessage{Role: "event", Kind: "event", Text: text})
	t.mu.Unlock()
	t.logf("info", "%s", text)
	c.startTask(t, c.execTask)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
