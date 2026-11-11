package main

// The project conversation: the middle of the project page. The owner talks to
// the project; the project's planner (Claude, OpenRouter or a node's model)
// answers with what it can see (the project's tasks, files and instructions) and
// starts tasks by writing [start task] blocks. Tasks it starts show up in the
// conversation as live cards (web/project.js), so their progress, questions and
// results come back into the chat. "Start as task" skips the model and starts the
// message as a task directly. The log lives in the project folder (chat.json) on
// the controller; nodes keep nothing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type ProjectChatMsg struct {
	ID    string    `json:"id"`
	Role  string    `json:"role"` // user | assistant
	Text  string    `json:"text,omitempty"`
	Time  time.Time `json:"time"`
	Tasks []string  `json:"tasks,omitempty"` // tasks this message started
	Who   string    `json:"who,omitempty"`   // assistant: the model or node that answered
	Error string    `json:"error,omitempty"`
	Cost  float64   `json:"cost_usd,omitempty"`
}

type projectChat struct {
	mu       sync.Mutex
	path     string
	Messages []ProjectChatMsg `json:"messages"`
	busy     bool
	cancel   context.CancelFunc
}

type projectChatView struct {
	Messages []ProjectChatMsg `json:"messages"`
	Busy     bool             `json:"busy"`
}

const (
	maxProjectChatMsgs  = 400
	maxProjectChatChars = 16000 // one message from the owner
	maxChatTasks        = 3     // tasks one answer may start
	projectChatOut      = 2000  // answer tokens
	projectChatHistory  = 24000 // characters of earlier conversation sent along
)

var (
	projectChats   sync.Map // project folder -> *projectChat
	projectChatMax = 10 * time.Minute
	taskBlockRe    = regexp.MustCompile(`(?is)\[start task\]\s*(.*?)\s*\[/start task\]`)
)

func (p *Project) chatLog() *projectChat {
	p.mu.Lock()
	dir := p.dir
	p.mu.Unlock()
	path := filepath.Join(dir, "chat.json")
	if v, ok := projectChats.Load(path); ok {
		return v.(*projectChat)
	}
	pc := &projectChat{path: path, Messages: []ProjectChatMsg{}}
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, pc) != nil || pc.Messages == nil {
			pc.Messages = []ProjectChatMsg{}
		}
	}
	v, _ := projectChats.LoadOrStore(path, pc)
	return v.(*projectChat)
}

func (pc *projectChat) saveLocked() {
	if len(pc.Messages) > maxProjectChatMsgs {
		pc.Messages = append([]ProjectChatMsg(nil), pc.Messages[len(pc.Messages)-maxProjectChatMsgs:]...)
	}
	b, _ := json.MarshalIndent(pc, "", "  ")
	if err := writeFileAtomic(pc.path, b, 0o600); err != nil {
		log.Printf("project chat: save failed: %v", err)
	}
}

func (pc *projectChat) view() projectChatView {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return projectChatView{Messages: append([]ProjectChatMsg{}, pc.Messages...), Busy: pc.busy}
}

func (pc *projectChat) add(m ProjectChatMsg) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.Messages = append(pc.Messages, m)
	pc.saveLocked()
}

func (c *Controller) projectChatRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/projects/{id}/chat", c.auth(c.handleProjectChat))
	mux.HandleFunc("POST /api/projects/{id}/chat/stop", c.auth(c.handleProjectChatStop))
	mux.HandleFunc("DELETE /api/projects/{id}/chat", c.auth(c.handleProjectChatClear))
}

// handleProjectChat takes one message. mode "task" starts it as a task at once;
// otherwise the planner answers in the background (the page polls the overview).
func (c *Controller) handleProjectChat(w http.ResponseWriter, r *http.Request) {
	p := c.getProject(r.PathValue("id"))
	if p == nil {
		writeJSON(w, 404, map[string]string{"error": "no such project"})
		return
	}
	var req struct {
		Text string `json:"text"`
		Mode string `json:"mode"` // "" (ask) | task
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeJSON(w, 400, map[string]string{"error": "write a message"})
		return
	}
	if len(text) > maxProjectChatChars {
		writeJSON(w, 400, map[string]string{"error": fmt.Sprintf("the message is too long (over %d characters); attach big text to a task instead", maxProjectChatChars)})
		return
	}
	pc := p.chatLog()
	if req.Mode == "task" {
		t, code, err := c.createTask(createReq{Prompt: text, Project: p.ID}, nil, createOpts{})
		if err != nil {
			writeJSON(w, code, map[string]string{"error": err.Error()})
			return
		}
		pc.add(ProjectChatMsg{ID: newID("m"), Role: "user", Text: text, Time: time.Now(), Tasks: []string{t.ID}})
		writeJSON(w, 200, map[string]string{"task": t.ID})
		return
	}
	pc.mu.Lock()
	if pc.busy {
		pc.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "still answering the last message; wait or press Stop"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatWait+projectChatMax)
	pc.busy, pc.cancel = true, cancel
	pc.Messages = append(pc.Messages, ProjectChatMsg{ID: newID("m"), Role: "user", Text: text, Time: time.Now()})
	pc.saveLocked()
	pc.mu.Unlock()
	go func() {
		defer cancel()
		msg := c.answerProjectChat(ctx, p, pc)
		msg.ID, msg.Role, msg.Time = newID("m"), "assistant", time.Now()
		pc.mu.Lock()
		pc.Messages = append(pc.Messages, msg)
		pc.busy, pc.cancel = false, nil
		pc.saveLocked()
		pc.mu.Unlock()
	}()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleProjectChatStop(w http.ResponseWriter, r *http.Request) {
	p := c.getProject(r.PathValue("id"))
	if p == nil {
		writeJSON(w, 404, map[string]string{"error": "no such project"})
		return
	}
	pc := p.chatLog()
	pc.mu.Lock()
	if pc.cancel != nil {
		pc.cancel()
	}
	pc.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleProjectChatClear empties the conversation. Tasks it started are kept.
func (c *Controller) handleProjectChatClear(w http.ResponseWriter, r *http.Request) {
	p := c.getProject(r.PathValue("id"))
	if p == nil {
		writeJSON(w, 404, map[string]string{"error": "no such project"})
		return
	}
	pc := p.chatLog()
	pc.mu.Lock()
	if pc.busy {
		pc.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "still answering; press Stop first"})
		return
	}
	pc.Messages = []ProjectChatMsg{}
	pc.saveLocked()
	pc.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// projectPlanner picks who answers: the project's planner, else the default.
func (c *Controller) projectPlanner(p *Project) (planner, model, orModel string, maxUSD float64, err error) {
	c.mu.Lock()
	set := c.settings
	c.mu.Unlock()
	p.mu.Lock()
	planner = p.Planner
	p.mu.Unlock()
	if planner == "" {
		planner = set.DefaultPlanner
	}
	if planner == "auto" || planner == "" {
		if planner, err = c.autoPlanner(); err != nil {
			return
		}
	}
	model, orModel, maxUSD = set.ClaudeModel, set.OpenRouterModel, set.DefaultCapUSD
	if _, ok := claudePrices[model]; !ok {
		model = "claude-sonnet-5-5"
	}
	err = c.checkPlanner(planner, orModel)
	return
}

// answerProjectChat runs one answer and starts the tasks it asks for.
func (c *Controller) answerProjectChat(ctx context.Context, p *Project, pc *projectChat) ProjectChatMsg {
	planner, model, orModel, maxUSD, err := c.projectPlanner(p)
	if err != nil {
		return ProjectChatMsg{Error: err.Error()}
	}
	pc.mu.Lock()
	hist := append([]ProjectChatMsg(nil), pc.Messages...)
	pc.mu.Unlock()
	system := c.projectChatSystem(p)
	user := projectChatTranscript(hist, projectChatHistory)

	var text, who string
	var cost float64
	if isCloudPlanner(planner) {
		req := llmRequest{System: system, User: user, MaxTokens: projectChatOut, Label: "Project chat"}
		var est float64
		if planner == "openrouter" {
			_ = openRouterCatalog().Refresh(ctx, c.openRouterKey(), false)
			est = openRouterEstimate(orModel, req.Size(), req.MaxTokens/2)
		} else {
			est = estimateCost(model, req.Size(), req.MaxTokens/2)
		}
		if limit, spent := c.monthlyCap(); limit > 0 && spent+est > limit {
			return ProjectChatMsg{Error: fmt.Sprintf("this answer (about %s) would pass the monthly spending cap; raise it on the Spending page or give the project a node planner", fmtUSD(est))}
		}
		if maxUSD > 0 && est > maxUSD {
			return ProjectChatMsg{Error: fmt.Sprintf("this answer would cost about %s, more than the default task cap (%s)", fmtUSD(est), fmtUSD(maxUSD))}
		}
		var resp *llmResponse
		if planner == "openrouter" {
			who = "OpenRouter · " + openRouterLabel(orModel)
			resp, err = callOpenRouter(ctx, c.openRouterKey(), orModel, req)
		} else {
			who = c.spendPlannerLabel("claude", model, "")
			resp, err = callClaude(ctx, c.anthropicKey(), model, req)
		}
		if err == nil {
			p.mu.Lock()
			name := p.Name
			p.mu.Unlock()
			if serr := c.recordSpendFor(planner, "projchat_"+p.ID, name+" · project chat", c.spendPlannerLabel(planner, model, orModel), resp); serr != nil {
				log.Printf("project chat: could not save the spending ledger: %v", serr)
			}
			text, cost = resp.Text, resp.CostUSD
		}
	} else {
		node, aerr := c.acquireModelNode(ctx, strings.TrimPrefix(planner, "node:"), "", chatWait)
		if aerr != nil {
			return ProjectChatMsg{Error: aerr.Error()}
		}
		ctxTok := c.nodeCtx(node)
		maxOut := min(projectChatOut, ctxTok/2)
		budget := (ctxTok - maxOut) * 3
		if len(system)+len(user) > budget {
			user = projectChatTranscript(hist, max(2000, budget-len(system)))
		}
		who = c.nodeLabel(node)
		spec := InferenceSpec{Messages: []ChatMessage{{Role: "system", Content: system}, {Role: "user", Content: user}}, Params: InferenceParams{MaxTokens: maxOut}}
		var out *InferenceOutput
		out, err = c.runInferenceJob(ctx, node, spec, "Project chat", projectChatMax)
		c.releaseNode(node)
		if err == nil {
			text = out.Text
		}
	}
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return ProjectChatMsg{Who: who, Error: "stopped"}
	case err != nil:
		return ProjectChatMsg{Who: who, Error: err.Error()}
	}
	reply, asks := splitTaskBlocks(text)
	msg := ProjectChatMsg{Text: reply, Who: who, Cost: cost}
	var failed []string
	for _, prompt := range asks {
		t, _, terr := c.createTask(createReq{Prompt: prompt, Project: p.ID}, nil, createOpts{})
		if terr != nil {
			failed = append(failed, terr.Error())
			continue
		}
		msg.Tasks = append(msg.Tasks, t.ID)
	}
	if len(failed) > 0 {
		msg.Error = "could not start a task: " + strings.Join(failed, "; ")
	}
	if msg.Text == "" && len(msg.Tasks) > 0 {
		msg.Text = "Started:"
	}
	return msg
}

// splitTaskBlocks takes the [start task] blocks out of an answer.
func splitTaskBlocks(text string) (string, []string) {
	var asks []string
	for _, m := range taskBlockRe.FindAllStringSubmatch(text, -1) {
		if s := strings.TrimSpace(m[1]); s != "" && len(asks) < maxChatTasks {
			asks = append(asks, s)
		}
	}
	rest := strings.TrimSpace(taskBlockRe.ReplaceAllString(text, ""))
	rest = regexp.MustCompile(`\n{3,}`).ReplaceAllString(rest, "\n\n")
	return rest, asks
}

// projectChatTranscript writes the conversation as text, keeping the newest
// messages that fit budget characters.
func projectChatTranscript(msgs []ProjectChatMsg, budget int) string {
	var parts []string
	used := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Text == "" && len(m.Tasks) == 0 {
			continue
		}
		who := "Owner"
		if m.Role == "assistant" {
			who = "You"
		}
		line := who + ": " + m.Text
		if len(m.Tasks) > 0 {
			line += "\n(started task " + strings.Join(m.Tasks, ", ") + ")"
		}
		if used+len(line) > budget && len(parts) > 0 {
			break
		}
		used += len(line) + 2
		parts = append([]string{line}, parts...)
	}
	return "The conversation so far (newest last). Answer the owner's last message.\n\n" + strings.Join(parts, "\n\n")
}

func sortTasksByCreated(list []*Task) {
	sort.Slice(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
}

// projectChatSystem describes the job and the project's current state.
func (c *Controller) projectChatSystem(p *Project) string {
	p.mu.Lock()
	name, instr, test, gitURL, branch := p.Name, p.Instructions, p.TestCommand, p.GitURL, p.GitBranch
	repo := p.repoDir()
	p.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, `You are the lead for the software project %q in Outpost, a self-hosted cluster that plans and writes code with the computers on the owner's network. You talk with the owner about this project. You can see the project's state below; you cannot open files or run commands yourself.

When the owner wants something built, changed, fixed or checked, start a task. The cluster plans it, writes the files, runs the tests and reports back here. Write each task on its own like this:

[start task]
A complete request for the planner: what to change and why, the files involved, how to check it.
[/start task]

Rules: start a task only when the owner asks for work or agrees to your suggestion; answer questions directly. One task per independent piece of work, at most %d; they run one at a time in the order you write them. Do not start a task that repeats one that is already waiting or working. Keep answers short and plain, and say what you started.
`, name, maxChatTasks)
	if test != "" {
		fmt.Fprintf(&b, "\nTest command: %s\n", test)
	}
	if gitURL != "" {
		fmt.Fprintf(&b, "Git: each finished task is committed to branch %s.\n", branch)
	}
	if s := strings.TrimSpace(instr); s != "" {
		fmt.Fprintf(&b, "\nProject instructions (the owner's standing rules):\n%s\n", clip(s, 3000))
	}

	c.mu.Lock()
	var list []*Task
	for _, t := range c.tasks {
		if t.ProjectID == p.ID {
			list = append(list, t)
		}
	}
	c.mu.Unlock()
	sortTasksByCreated(list)
	if len(list) > 25 {
		list = list[len(list)-25:]
	}
	b.WriteString("\nTasks in this project (oldest first):\n")
	if len(list) == 0 {
		b.WriteString("(none yet)\n")
	}
	for _, t := range list {
		t.mu.Lock()
		s := t.summaryLocked()
		f := t.feedLocked()
		t.mu.Unlock()
		state := s.Status
		if s.NeedsYou {
			state += ", waiting on the owner"
		}
		if s.TestStatus != "" {
			state += ", tests " + s.TestStatus
		}
		if s.Resolved {
			state += ", resolved"
		}
		fmt.Fprintf(&b, "- %s (%s) [%s]: %s\n", s.Title, s.ID, state, clip(f.Requests[len(f.Requests)-1].Text, 300))
		if f.Result != "" {
			fmt.Fprintf(&b, "  result: %s\n", clip(f.Result, 300))
		}
	}
	files := listRepo(repo)
	fmt.Fprintf(&b, "\nProject files (%d):\n", len(files))
	for i, f := range files {
		if i == 300 {
			fmt.Fprintf(&b, "… and %d more\n", len(files)-300)
			break
		}
		b.WriteString(f.Path + "\n")
	}
	return b.String()
}
