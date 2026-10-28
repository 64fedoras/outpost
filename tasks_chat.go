package main

// A task is a conversation. Each request the user makes starts a round:
//
//   user request → (planner questions → user answers) → plan card
//   → user approves or replies with changes → work → result
//
// Later messages depend on where the task is: they answer questions, revise
// the plan, steer the running work (notes every later subtask, review and fix
// follows), or, once the task has finished, start a new round that builds on
// the same project files.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type TaskMessage struct {
	ID        string     `json:"id"`
	Time      time.Time  `json:"time"`
	Role      string     `json:"role"` // user | assistant | event
	Kind      string     `json:"kind"` // text | answers | steer | questions | plan | result | error | event
	Text      string     `json:"text,omitempty"`
	Questions []Question `json:"questions,omitempty"`
	Plan      *Plan      `json:"plan,omitempty"`
	Files     []string   `json:"files,omitempty"` // result: files added or changed in this round
	Round     int        `json:"round"`
}

type Question struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Multi    bool     `json:"multi,omitempty"`
}

// addMsgLocked appends a message to the conversation; t.mu must be held.
func (t *Task) addMsgLocked(m TaskMessage) {
	m.ID = fmt.Sprintf("m%d", len(t.Messages)+1)
	m.Time = time.Now()
	if m.Round == 0 {
		m.Round = t.Round
	}
	t.Messages = append(t.Messages, m)
	t.saveLocked()
}

func (t *Task) addMsg(m TaskMessage) {
	t.mu.Lock()
	t.addMsgLocked(m)
	t.mu.Unlock()
}

// touch records a file written in this round, for the result message.
func (t *Task) touch(paths ...string) {
	t.mu.Lock()
	for _, p := range paths {
		found := false
		for _, x := range t.Touched {
			if x == p {
				found = true
				break
			}
		}
		if !found {
			t.Touched = append(t.Touched, p)
		}
	}
	t.mu.Unlock()
}

// migrateMessages gives tasks made before 0.6 a conversation.
func (t *Task) migrateMessages() {
	if len(t.Messages) > 0 {
		return
	}
	if t.Round == 0 {
		t.Round = 1
	}
	t.addMsgLocked(TaskMessage{Role: "user", Kind: "text", Text: t.Prompt})
	if t.Plan != nil {
		p := *t.Plan
		t.addMsgLocked(TaskMessage{Role: "assistant", Kind: "plan", Plan: &p})
	}
	if t.Status == "done" {
		t.addMsgLocked(TaskMessage{Role: "assistant", Kind: "result", Text: resultText(t)})
	}
}

// steeringText is the user's notes for the running round, for worker, review
// and fix prompts.
func (t *Task) steeringText() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.Steering) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nNotes from the user, added while the work was running. Where they conflict with the plan, follow the notes:\n")
	for _, s := range t.Steering {
		fmt.Fprintf(&b, "- %s\n", s)
	}
	return b.String()
}

// plannerPrompt is the user turn for planning the current round: the project
// so far (for follow-ups), earlier requests, and this round's conversation.
func (c *Controller) plannerPrompt(t *Task, budget int, want []string) string {
	t.mu.Lock()
	round := t.Round
	msgs := append([]TaskMessage(nil), t.Messages...)
	t.mu.Unlock()
	var b strings.Builder
	b.WriteString(t.instructionsBlock())
	var prevPlan, roundPlan *Plan
	var earlier []string
	for _, m := range msgs {
		if m.Kind == "plan" && m.Plan != nil {
			if m.Round < round {
				prevPlan = m.Plan
			} else {
				roundPlan = m.Plan
			}
		}
		if m.Round < round && m.Role == "user" && m.Kind != "steer" {
			earlier = append(earlier, m.Text)
		}
	}
	files := listRepo(t.repoDir())
	if round > 1 && len(files) > 0 {
		b.WriteString("This is a follow-up request on an existing project. Plan only the files to add or change.\n\n")
		if prevPlan != nil {
			fmt.Fprintf(&b, "Project: %s\n%s\nLanguage: %s\nTest command so far: %s\n\n", prevPlan.Title, prevPlan.Summary, prevPlan.Language, prevPlan.TestCommand)
		}
		if len(earlier) > 0 {
			b.WriteString("Earlier requests and answers, oldest first:\n")
			for _, e := range earlier {
				fmt.Fprintf(&b, "- %s\n", strings.ReplaceAll(firstLine(e, 600), "\n", " "))
			}
			b.WriteString("\n")
		}
		if repoNeedsIndex(t.repoDir(), budget) {
			b.WriteString("Project index (path, size, summary, top-level declarations):\n")
			b.WriteString(renderIndex(buildIndex(t.repoDir()), budget/3))
			if len(want) > 0 {
				b.WriteString("\nFiles you asked for:\n")
				b.WriteString(wantedFiles(t.repoDir(), want, budget/3))
			}
		} else {
			b.WriteString("Current project files:\n")
			b.WriteString(repoSnapshot(t.repoDir(), budget/2))
		}
		b.WriteString("\n")
	}
	b.WriteString("Conversation about this request:\n")
	for _, m := range msgs {
		if m.Round != round {
			continue
		}
		switch {
		case m.Role == "user":
			fmt.Fprintf(&b, "User: %s\n", m.Text)
		case m.Kind == "questions":
			b.WriteString("You asked:\n")
			for _, q := range m.Questions {
				fmt.Fprintf(&b, "  - %s (options: %s)\n", q.Question, strings.Join(q.Options, " / "))
			}
		case m.Kind == "plan":
			b.WriteString("You proposed a plan (below).\n")
		}
	}
	if roundPlan != nil {
		prev, _ := json.MarshalIndent(roundPlan, "", "  ")
		b.WriteString("\nYour latest plan:\n" + string(prev) + "\n\nRevise it to follow the user's messages after it.\n")
	}
	return b.String()
}

// handleTaskMessage takes a message from the task's composer. What it does
// depends on where the task is.
func (c *Controller) handleTaskMessage(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		writeJSON(w, 400, map[string]string{"error": "write a message"})
		return
	}
	text := strings.TrimSpace(req.Text)
	t.mu.Lock()
	status := t.Status
	t.mu.Unlock()
	switch status {
	case "awaiting_answers":
		t.addMsg(TaskMessage{Role: "user", Kind: "answers", Text: text})
		c.replan(t, "Planning with your answers.")
	case "planning", "awaiting_approval":
		t.addMsg(TaskMessage{Role: "user", Kind: "text", Text: text})
		c.replan(t, "Revising the plan.")
	case "running", "testing", "fixing", "paused_cap":
		t.mu.Lock()
		t.Steering = append(t.Steering, text)
		t.addMsgLocked(TaskMessage{Role: "user", Kind: "steer", Text: text})
		t.addMsgLocked(TaskMessage{Role: "event", Kind: "event", Text: "Noted. Subtasks that start from now on, reviews and test fixes follow this."})
		t.mu.Unlock()
		t.logf("info", "Note from you: %s", firstLine(text, 200))
	case "done", "failed", "cancelled", "interrupted":
		t.mu.Lock()
		t.Round++
		t.Asked, t.Steering, t.Touched = false, nil, nil
		t.Plan, t.Approved, t.Ended = nil, nil, nil
		t.Subtasks = []*SubtaskState{}
		t.TestStatus, t.TestOutput, t.FixRounds, t.Error, t.PausedFrom = "", "", 0, "", ""
		t.addMsgLocked(TaskMessage{Role: "user", Kind: "text", Text: text})
		t.mu.Unlock()
		c.replan(t, "Planning the follow-up on the existing project.")
	default:
		writeJSON(w, 409, map[string]string{"error": "this task cannot take messages right now"})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// replan (re)starts planning for the current round.
func (c *Controller) replan(t *Task, note string) {
	t.mu.Lock()
	t.Status, t.Error = "planning", ""
	t.saveLocked()
	t.mu.Unlock()
	t.logf("info", "%s", note)
	c.startTask(t, c.planTask)
}

// handleTaskAnswers takes answers to the planner's questions:
// {"answers": {"q1": ["Python"], "q2": ["SQLite", "JSON"]}, "note": "…"}
func (c *Controller) handleTaskAnswers(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	var req struct {
		Answers map[string][]string `json:"answers"`
		Note    string              `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad answers"})
		return
	}
	t.mu.Lock()
	if t.Status != "awaiting_answers" {
		t.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "the planner is not waiting for answers"})
		return
	}
	var qs []Question
	for i := len(t.Messages) - 1; i >= 0; i-- {
		if t.Messages[i].Kind == "questions" {
			qs = t.Messages[i].Questions
			break
		}
	}
	t.mu.Unlock()
	var lines []string
	for _, q := range qs {
		a := req.Answers[q.ID]
		if len(a) == 0 {
			lines = append(lines, q.Question+" → (no preference; you decide)")
			continue
		}
		lines = append(lines, q.Question+" → "+strings.Join(a, ", "))
	}
	if n := strings.TrimSpace(req.Note); n != "" {
		lines = append(lines, n)
	}
	if len(lines) == 0 {
		writeJSON(w, 400, map[string]string{"error": "answer at least one question"})
		return
	}
	t.addMsg(TaskMessage{Role: "user", Kind: "answers", Text: strings.Join(lines, "\n")})
	c.replan(t, "Planning with your answers.")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// resultText summarises a finished round for the conversation.
func resultText(t *Task) string {
	n := len(t.Subtasks)
	var s string
	switch t.TestStatus {
	case "passed":
		s = "Done, and the tests pass."
	case "failing":
		s = fmt.Sprintf("Done, but the tests still fail after %d fix round(s). The last test output is below.", t.FixRounds)
	case "skipped":
		if t.Plan != nil && t.Plan.TestCommand != "" {
			s = "Done. The tests were not run because no node has exec enabled."
		} else {
			s = "Done. This project has no automated tests."
		}
	default:
		s = "Done."
	}
	extra := []string{fmt.Sprintf("%d subtask(s)", n)}
	if t.FixRounds > 0 && t.TestStatus == "passed" {
		extra = append(extra, fmt.Sprintf("%d fix round(s)", t.FixRounds))
	}
	if t.CostUSD > 0 {
		extra = append(extra, "Cloud model cost so far "+fmtUSD(t.CostUSD))
	}
	return s + " " + strings.Join(extra, " · ") + "."
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
