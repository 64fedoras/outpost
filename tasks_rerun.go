package main

// Re-running an approved plan. Two modes, both on a task that has stopped:
//
//   - "all" (default): the files this round wrote are put back (the same per-round
//     copy Undo a round uses, see tasks_rounds.go) and the same plan runs again from
//     the start in a new round. The earlier run stays in the thread.
//   - "failed": subtasks that failed go back to pending and the run continues from
//     there, keeping finished subtasks and their files (and re-running the tests).
//
// The planner is not called again. Cloud spend keeps counting against the cap.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleRerunPlan takes {"mode": "all"|"failed", "cap_usd": 0}.
func (c *Controller) handleRerunPlan(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	var req struct {
		Mode   string  `json:"mode"`
		CapUSD float64 `json:"cap_usd"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Mode == "" {
		req.Mode = "all"
	}
	if req.Mode != "all" && req.Mode != "failed" {
		writeJSON(w, 400, map[string]string{"error": "mode must be all or failed"})
		return
	}
	t.mu.Lock()
	status, planner, orModel := t.Status, t.Planner, t.ORModel
	hasPlan := t.Plan != nil && t.Approved != nil
	t.mu.Unlock()
	switch status {
	case "done", "failed", "cancelled":
	case "interrupted":
		writeJSON(w, 409, map[string]string{"error": "this task was interrupted; use Resume instead"})
		return
	default:
		writeJSON(w, 409, map[string]string{"error": "stop the task or wait for it to finish first"})
		return
	}
	if !hasPlan {
		writeJSON(w, 409, map[string]string{"error": "there is no approved plan to re-run"})
		return
	}
	if err := c.checkPlanner(planner, orModel); err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}

	if req.Mode == "failed" {
		t.mu.Lock()
		reset := t.resetFailedSubtasksLocked()
		t.applyRerunCap(req.CapUSD)
		t.Ended, t.Error, t.PausedFrom = nil, "", ""
		t.TestStatus, t.TestOutput, t.FixRounds = "", "", 0
		t.BuildStatus, t.BuildOutput = "", ""
		t.Status = "running"
		note := "Retrying the plan: " + strconv.Itoa(reset) + " failed subtask(s) run again; finished ones are kept."
		if reset == 0 {
			note = "Retrying the plan: no subtask failed, so the tests and fixes run again."
		}
		t.addMsgLocked(TaskMessage{Role: "event", Kind: "event", Text: note})
		t.mu.Unlock()
		t.logf("info", "%s", note)
		c.startTask(t, c.execTask)
		writeJSON(w, 200, map[string]any{"ok": true, "mode": "failed", "reset": reset})
		return
	}

	restored, round, err := c.rerunWhole(t, req.CapUSD, false)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "mode": "all", "round": round, "files": restored})
}

// rerunWhole puts the current round's files back and runs the plan again from
// the start as a new round. edited marks the plan copy shown in the thread.
func (c *Controller) rerunWhole(t *Task, capUSD float64, edited bool) (restored, round int, err error) {
	t.mu.Lock()
	old := t.Round
	t.mu.Unlock()
	if restored, err = t.undoRound(old); err != nil {
		return 0, 0, err
	}
	t.mu.Lock()
	t.Round = old + 1
	t.Touched, t.ResultBytes, t.Steering = nil, 0, nil
	t.Subtasks = make([]*SubtaskState, len(t.Plan.Subtasks))
	for i, s := range t.Plan.Subtasks {
		t.Subtasks[i] = &SubtaskState{ID: s.ID, Status: "pending"}
	}
	t.TestStatus, t.TestOutput, t.FixRounds, t.Error, t.PausedFrom = "", "", 0, "", ""
	t.BuildStatus, t.BuildOutput = "", ""
	t.Artifacts = nil
	t.applyRerunCap(capUSD)
	now := time.Now()
	t.Approved, t.Ended = &now, nil
	t.Status = "running"
	p := *t.Plan
	t.addMsgLocked(TaskMessage{Role: "assistant", Kind: "plan", Plan: &p, Edited: edited})
	note := "You approved the plan again; it runs from the start as request " + strconv.Itoa(old+1) + ". The earlier run stays above."
	if restored > 0 {
		note = "You approved the plan again; " + strconv.Itoa(restored) + " file(s) were put back as they were before request " + strconv.Itoa(old) + ", and it runs from the start as request " + strconv.Itoa(old+1) + ". The earlier run stays above."
	}
	t.addMsgLocked(TaskMessage{Role: "event", Kind: "event", Text: note})
	t.mu.Unlock()
	t.logf("info", "Re-running the approved plan (round %d, %d files put back).", old+1, restored)
	c.startTask(t, c.execTask)
	return restored, old + 1, nil
}

// resetFailedSubtasksLocked puts failed subtasks back to pending and returns how
// many there were; t.mu must be held.
func (t *Task) resetFailedSubtasksLocked() int {
	n := 0
	for _, s := range t.Subtasks {
		if s.Status == "failed" {
			s.Status, s.Node, s.NodeName, s.Started, s.Ended = "pending", "", "", nil, nil
			s.Attempts, s.Error, s.ReviewNote = 0, "", ""
			n++
		}
	}
	return n
}

// applyRerunCap raises the spend cap when the request asks for a higher one;
// t.mu must be held.
func (t *Task) applyRerunCap(capUSD float64) {
	if capUSD > 0 {
		t.CapUSD = capUSD
	}
}

// cleanEditedPlan takes the plan the user edited, keeps the fields the editor
// does not show from the current plan, and checks the result like a planner's plan.
func cleanEditedPlan(cur, in *Plan) (*Plan, error) {
	p := *in
	p.Note, p.PreviewPage, p.PreviewCommand, p.PreviewURL, p.Mockup = cur.Note, cur.PreviewPage, cur.PreviewCommand, cur.PreviewURL, cur.Mockup
	for _, f := range []*string{&p.Title, &p.Summary, &p.Language, &p.SetupCommand, &p.TestCommand, &p.BuildCommand} {
		*f = strings.TrimSpace(*f)
	}
	if p.Title == "" {
		return nil, errors.New("give the plan a title")
	}
	p.Subtasks = append([]PlanSubtask(nil), in.Subtasks...)
	for i := range p.Subtasks {
		s := &p.Subtasks[i]
		s.Title, s.Instructions = strings.TrimSpace(s.Title), strings.TrimSpace(s.Instructions)
		if s.Title == "" || s.Instructions == "" {
			return nil, fmt.Errorf("subtask %d needs a title and instructions", i+1)
		}
		var files []string
		for _, f := range s.Files {
			if f = strings.TrimSpace(f); f != "" {
				files = append(files, f)
			}
		}
		s.Files = files
	}
	if err := validatePlan(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// handleEditPlan takes {"plan": {...}, "run": bool, "cap_usd": 0}. Before approval
// the edited plan replaces the one on the card (approve it next, or "run" does
// that); on a stopped task it is saved, and "run" re-runs it from a clean start.
// The replaced plan stays in the thread, dimmed. A running task is refused.
func (c *Controller) handleEditPlan(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	var req struct {
		Plan   *Plan   `json:"plan"`
		Run    bool    `json:"run"`
		CapUSD float64 `json:"cap_usd"`
	}
	if err := readJSONBody(w, r, &req); err != nil || req.Plan == nil {
		writeJSON(w, 400, map[string]string{"error": "send the plan"})
		return
	}
	t.mu.Lock()
	status, cur, approved, planner, orModel := t.Status, t.Plan, t.Approved != nil, t.Planner, t.ORModel
	t.mu.Unlock()
	pending := status == "awaiting_approval"
	switch {
	case pending, status == "done" || status == "failed" || status == "cancelled":
	default:
		writeJSON(w, 409, map[string]string{"error": "stop the task or wait for it to finish before editing the plan"})
		return
	}
	if cur == nil || (!pending && !approved) {
		writeJSON(w, 409, map[string]string{"error": "there is no plan to edit"})
		return
	}
	if cur.Mockup {
		writeJSON(w, 409, map[string]string{"error": "design-variant plans cannot be edited"})
		return
	}
	np, err := cleanEditedPlan(cur, req.Plan)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if req.Run {
		if err := c.checkPlanner(planner, orModel); err != nil {
			writeJSON(w, 409, map[string]string{"error": err.Error()})
			return
		}
	}

	t.mu.Lock()
	if t.Status != status {
		t.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "the task changed; try again"})
		return
	}
	// The plan being replaced: the latest live plan message of this round.
	replaced := -1
	for i, m := range t.Messages {
		if m.Kind == "plan" && !m.Superseded && m.Round == t.Round {
			replaced = i
		}
	}
	old := map[string]*SubtaskState{}
	for _, s := range t.Subtasks {
		old[s.ID] = s
	}
	t.Plan = np
	t.Subtasks = make([]*SubtaskState, len(np.Subtasks))
	for i, s := range np.Subtasks {
		if o := old[s.ID]; o != nil && !pending {
			t.Subtasks[i] = o
		} else {
			t.Subtasks[i] = &SubtaskState{ID: s.ID, Status: "pending"}
		}
	}
	if pending || !req.Run {
		if replaced >= 0 {
			t.Messages[replaced].Superseded = true
		}
		cp := *np
		t.addMsgLocked(TaskMessage{Role: "assistant", Kind: "plan", Plan: &cp, Text: strings.TrimSpace(np.Note), Edited: true})
	}
	t.mu.Unlock()
	t.logf("info", "You edited the plan (%d subtasks).", len(np.Subtasks))

	switch {
	case !req.Run:
	case pending:
		t.mu.Lock()
		t.applyRerunCap(req.CapUSD)
		now := time.Now()
		t.Approved, t.Status = &now, "running"
		t.addMsgLocked(TaskMessage{Role: "event", Kind: "event", Text: "You approved the edited plan."})
		t.mu.Unlock()
		c.startTask(t, c.execTask)
	default:
		if _, _, err := c.rerunWhole(t, req.CapUSD, true); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
