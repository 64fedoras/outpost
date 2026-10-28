package main

// Editing a sent message, like Claude: the edited text replaces the message and
// the work re-runs from that point. The old branch is kept in the thread
// (Superseded, shown dimmed) rather than deleted.
//
//   - While the plan is being asked about or approved (nothing written yet), any
//     of the round's user messages can be edited: later messages are superseded
//     and the planner runs again in the same round.
//   - Once the task has stopped (done, failed, stopped), the opening request of
//     a round can be edited: that round and every later one is undone (the files
//     are put back from the per-round copies, see tasks_rounds.go), and the
//     edited text starts a new round.
//   - A running task has to be stopped first. Cloud spend stays counted.

import (
	"net/http"
	"strconv"
	"strings"
)

// editTarget finds the message to edit and the index its branch starts at.
// t.mu must be held.
func (t *Task) editTargetLocked(id string) (idx, from int, errMsg string) {
	idx = -1
	for i, m := range t.Messages {
		if m.ID == id {
			idx = i
		}
	}
	if idx < 0 {
		return -1, 0, "no such message"
	}
	m := t.Messages[idx]
	if m.Role != "user" || (m.Kind != "text" && m.Kind != "answers") || m.Superseded {
		return -1, 0, "this message cannot be edited"
	}
	switch t.Status {
	case "awaiting_answers", "awaiting_approval":
		if m.Round != t.Round || t.Approved != nil {
			return -1, 0, "only this round's messages can be edited before the work starts"
		}
		return idx, idx, ""
	case "done", "failed", "cancelled", "interrupted":
		for i, x := range t.Messages {
			if x.Round == m.Round && x.Role == "user" && !x.Superseded && (x.Kind == "text" || x.Kind == "answers") {
				if i != idx {
					return -1, 0, "only the request that opened a round can be edited once the task has stopped"
				}
				break
			}
		}
		return idx, idx, ""
	}
	return -1, 0, "stop the task first, then edit the message"
}

// handleEditMessage takes {"text": "…"} for message {msg}.
func (c *Controller) handleEditMessage(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSONBody(w, r, &req); err != nil || strings.TrimSpace(req.Text) == "" {
		writeJSON(w, 400, map[string]string{"error": "write a message"})
		return
	}
	text := strings.TrimSpace(req.Text)
	t.mu.Lock()
	idx, from, bad := t.editTargetLocked(r.PathValue("msg"))
	if bad != "" {
		t.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": bad})
		return
	}
	old := t.Messages[idx]
	idle := t.Status != "awaiting_answers" && t.Status != "awaiting_approval"
	round := t.Round
	t.mu.Unlock()

	restored := 0
	if idle {
		// Put the files back to how they were before the edited request, newest round first.
		nums := t.roundNumbers()
		for i := len(nums) - 1; i >= 0 && nums[i] >= old.Round; i-- {
			n, err := t.undoRound(nums[i])
			restored += n
			if err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
		}
	}

	t.mu.Lock()
	for i := from; i < len(t.Messages); i++ {
		t.Messages[i].Superseded = true
		t.Messages[i].Pending = false
	}
	if idle {
		t.Round = round + 1
		t.Touched, t.ResultBytes = nil, 0
		t.Plan, t.Approved, t.Ended = nil, nil, nil
		t.Subtasks = []*SubtaskState{}
		t.TestStatus, t.TestOutput, t.FixRounds, t.Error, t.PausedFrom = "", "", 0, "", ""
		t.BuildStatus, t.BuildOutput = "", ""
		t.Asked = false
		note := "Edited request " + strconv.Itoa(old.Round) + "; the earlier answer is kept above, dimmed."
		if restored > 0 {
			note = "Edited request " + strconv.Itoa(old.Round) + ": " + strconv.Itoa(restored) + " file(s) put back as they were before it. The earlier answer is kept above, dimmed."
		}
		t.addMsgLocked(TaskMessage{Role: "event", Kind: "event", Text: note})
	} else {
		t.Plan = nil
	}
	t.Steering = nil
	// An edited opening request is also the task's prompt.
	if old.Round == 1 && old.Kind == "text" {
		t.Prompt = text
	}
	newRound := old.Round
	if idle {
		newRound = round + 1
	}
	t.addMsgLocked(TaskMessage{Role: "user", Kind: old.Kind, Text: text, Images: old.Images, Edited: true, Round: newRound})
	t.mu.Unlock()
	t.logf("info", "Edited a message: %s", firstLine(text, 200))
	c.replan(t, "Planning with the edited message.")
	writeJSON(w, 200, map[string]bool{"ok": true})
}
