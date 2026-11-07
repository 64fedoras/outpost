package main

// Learning from failures (idea 47): when a task in a project finishes with
// tests still failing, the planner is asked for one short standing rule that
// would have avoided the failure. The rule is only a suggestion on the
// project card; the owner adds it to the project's instructions or dismisses
// it. A cloud planner skips the call when it would pass the task's cost cap.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type ProjectSuggestion struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	TaskID    string    `json:"task_id"`
	TaskTitle string    `json:"task_title"`
	Created   time.Time `json:"created"`
}

const maxSuggestions = 5

const suggestSystem = `You help a small cluster of local coding models do better next time. A task in a project finished with its tests still failing. Suggest ONE short standing instruction for future tasks in this project that would most likely have avoided this failure (for example a convention, a library to use or avoid, how tests are run). It must be general, not a fix for this one bug, and at most 200 characters.

Answer with the instruction only, on one line. If there is nothing general to learn, answer exactly NONE.`

var suggestTimeout = 5 * time.Minute

// suggestFromFailure runs in the background after a task ended with failing tests.
func (c *Controller) suggestFromFailure(t *Task) {
	t.mu.Lock()
	pid, title, planner := t.ProjectID, t.Title, t.Planner
	var req strings.Builder
	for _, m := range t.Messages {
		if m.Role == "user" && !m.Superseded && (m.Kind == "text" || m.Kind == "steer") {
			fmt.Fprintf(&req, "- %s\n", clipText(strings.TrimSpace(m.Text), 1500))
		}
	}
	out := t.TestOutput
	cost, capUSD, model, orModel := t.CostUSD, t.CapUSD, t.ClaudeModel, t.ORModel
	cmd := ""
	if t.Plan != nil {
		cmd = t.Plan.TestCommand
	}
	t.mu.Unlock()
	p := c.getProject(pid)
	if p == nil || planner == "" {
		return
	}
	p.mu.Lock()
	instr := p.Instructions
	p.mu.Unlock()
	if len(out) > 6000 {
		out = out[len(out)-6000:]
	}
	user := fmt.Sprintf("Requests:\n%s\nTest command: %s\n\nLast test output (end):\n%s\n\nThe project's current instructions:\n%s\n",
		req.String(), cmd, out, firstNonEmpty(instr, "(none)"))
	lr := llmRequest{System: suggestSystem, User: user, MaxTokens: 300, Temperature: 0.2, Label: "Suggesting a project rule"}
	if isCloudPlanner(planner) {
		est := estimateCost(model, lr.Size(), lr.MaxTokens)
		if planner == "openrouter" {
			est = openRouterEstimate(orModel, lr.Size(), lr.MaxTokens)
		}
		if cost+est > capUSD {
			t.logf("info", "No project rule suggested: the call would pass the cost cap.")
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), suggestTimeout)
	defer cancel()
	resp, err := c.plannerCall(ctx, t, lr)
	if err != nil {
		t.logf("info", "No project rule suggested: %v", err)
		return
	}
	text := cleanSuggestion(resp.Text)
	if text == "" {
		return
	}
	if p.addSuggestion(ProjectSuggestion{ID: newID("sg"), Text: text, TaskID: t.ID, TaskTitle: title, Created: time.Now()}) {
		_ = p.save()
		t.logf("info", "Suggested a rule for project %s: %s", p.Name, text)
	}
}

// cleanSuggestion keeps the first meaningful line, or "" for NONE.
func cleanSuggestion(s string) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.Trim(l, " \t`\"*-•")
		if l == "" {
			continue
		}
		if strings.EqualFold(strings.TrimRight(l, "."), "none") {
			return ""
		}
		if len(l) > 300 {
			l = l[:300]
		}
		return l
	}
	return ""
}

// addSuggestion keeps the newest few, skipping one already present or already in the instructions.
func (p *Project) addSuggestion(s ProjectSuggestion) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	low := strings.ToLower(s.Text)
	if strings.Contains(strings.ToLower(p.Instructions), low) {
		return false
	}
	for _, o := range p.Suggestions {
		if strings.ToLower(o.Text) == low {
			return false
		}
	}
	p.Suggestions = append(p.Suggestions, s)
	if len(p.Suggestions) > maxSuggestions {
		p.Suggestions = p.Suggestions[len(p.Suggestions)-maxSuggestions:]
	}
	return true
}

// handleProjectSuggestion adds a suggestion to the instructions (action add) or drops it.
func (c *Controller) handleProjectSuggestion(w http.ResponseWriter, r *http.Request) {
	p := c.getProject(r.PathValue("id"))
	if p == nil {
		writeJSON(w, 404, map[string]string{"error": "no such project"})
		return
	}
	sid, act := r.PathValue("sid"), r.PathValue("act")
	if act != "add" && act != "dismiss" {
		writeJSON(w, 404, map[string]string{"error": "unknown action"})
		return
	}
	p.mu.Lock()
	found := false
	for i, s := range p.Suggestions {
		if s.ID != sid {
			continue
		}
		found = true
		if act == "add" {
			p.Instructions = clipInstructions(strings.TrimSpace(strings.TrimSpace(p.Instructions) + "\n" + s.Text))
		}
		p.Suggestions = append(p.Suggestions[:i:i], p.Suggestions[i+1:]...)
		break
	}
	p.mu.Unlock()
	if !found {
		writeJSON(w, 404, map[string]string{"error": "no such suggestion"})
		return
	}
	if err := p.save(); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
