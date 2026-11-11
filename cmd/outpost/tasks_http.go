package main

// Task HTTP handlers, repo file listing and zip export.
// Split out of tasks.go; see that file for the task model and overview.

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type taskSummary struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Status      string     `json:"status"`
	Planner     string     `json:"planner"`
	CostUSD     float64    `json:"cost_usd"`
	CapUSD      float64    `json:"cap_usd"`
	Done        int        `json:"done"`
	Total       int        `json:"total"`
	TestStatus  string     `json:"test_status"`
	Created     time.Time  `json:"created"`
	Ended       *time.Time `json:"ended,omitempty"`
	ResultBytes int64      `json:"result_bytes"`
	NeedsYou    bool       `json:"needs_you"` // waiting for answers, approval, a higher cap or a resume
	Round       int        `json:"round"`
	ProjectID   string     `json:"project_id,omitempty"`
	Updated     time.Time  `json:"updated"`
	Pinned      bool       `json:"pinned,omitempty"`
	AutoResume  bool       `json:"auto_resume,omitempty"` // interrupted, resuming on its own once its node is back
	CapKind     string     `json:"cap_kind,omitempty"`    // paused_cap: "monthly" when the monthly cap stopped it
	// Resolved: marked resolved and nothing happened since (no new message,
	// no later end, not running and not waiting on the person).
	Resolved bool `json:"resolved,omitempty"`
}

func (t *Task) summaryLocked() taskSummary {
	s := taskSummary{ID: t.ID, Title: t.Title, Status: t.Status, Planner: t.Planner, CostUSD: t.CostUSD, CapUSD: t.CapUSD,
		ProjectID: t.ProjectID, Total: len(t.Subtasks), TestStatus: t.TestStatus, Created: t.Created, Ended: t.Ended, ResultBytes: t.ResultBytes, Round: t.Round, Updated: t.Created, Pinned: t.Pinned, AutoResume: t.AutoResume, CapKind: t.CapKind}
	switch {
	case t.Status == "interrupted" && t.AutoResume:
		s.NeedsYou = false // resuming on its own
	case t.Status == "awaiting_answers" || t.Status == "awaiting_approval" || t.Status == "paused_cap" || t.Status == "interrupted":
		s.NeedsYou = true
	default:
		s.NeedsYou = t.hasPendingAsk()
	}
	if n := len(t.Messages); n > 0 {
		s.Updated = t.Messages[n-1].Time
	}
	for _, st := range t.Subtasks {
		if st.Status == "done" {
			s.Done++
		}
	}
	if s.Title == "" {
		s.Title = firstLine(t.Prompt, 80)
	}
	s.Resolved = t.resolvedLocked(s.Updated, s.NeedsYou)
	return s
}

// resolvedLocked reports whether the resolved mark still holds: a follow-up,
// a retry or anything that needs the person clears it without a write.
func (t *Task) resolvedLocked(updated time.Time, needsYou bool) bool {
	if t.ResolvedAt == nil || needsYou {
		return false
	}
	switch t.Status {
	case "done", "failed", "cancelled":
	default:
		return false // working, queued or resuming
	}
	if updated.After(*t.ResolvedAt) || (t.Ended != nil && t.Ended.After(*t.ResolvedAt)) {
		return false
	}
	return true
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

func (c *Controller) taskSummaries() []taskSummary {
	c.mu.Lock()
	list := make([]*Task, 0, len(c.tasks))
	for _, t := range c.tasks {
		list = append(list, t)
	}
	c.mu.Unlock()
	out := make([]taskSummary, 0, len(list))
	for _, t := range list {
		t.mu.Lock()
		out = append(out, t.summaryLocked())
		t.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

func (c *Controller) getTask(id string) *Task {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tasks[id]
}

func (c *Controller) handleListTasks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, c.taskSummaries())
}

func (c *Controller) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	req, files, err := readCreateRequest(w, r)
	if err != nil || strings.TrimSpace(req.Prompt) == "" {
		msg := "describe the task"
		if err != nil {
			msg = err.Error()
		}
		writeJSON(w, 400, map[string]string{"error": msg})
		return
	}
	t, code, err := c.createTask(req, files, createOpts{})
	if err != nil {
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"id": t.ID})
}

// createOpts are how a task was started when not by hand.
type createOpts struct {
	Unattended bool   // a schedule started it: no questions, the plan is approved on its own
	Template   string // the saved task it came from
}

// createTask starts a task from a request; the int is the HTTP status for an error.
func (c *Controller) createTask(req createReq, files []attachedFile, opt createOpts) (*Task, int, error) {
	proj := c.getProject(req.Project)
	if req.Project != "" && proj == nil {
		return nil, 404, errors.New("no such project")
	}
	c.mu.Lock()
	set := c.settings
	c.mu.Unlock()
	if proj != nil {
		req = proj.applyDefaults(req)
	}
	if req.Planner == "" {
		req.Planner = set.DefaultPlanner
	}
	if req.Planner == "auto" {
		p, err := c.autoPlanner()
		if err != nil {
			return nil, 409, err
		}
		req.Planner = p
	}
	if req.ClaudeModel == "" {
		req.ClaudeModel = set.ClaudeModel
	}
	if _, ok := claudePrices[req.ClaudeModel]; !ok {
		req.ClaudeModel = "claude-sonnet-5-5"
	}
	if req.ORModel == "" {
		req.ORModel = set.OpenRouterModel
	}
	if req.CapUSD <= 0 {
		req.CapUSD = set.DefaultCapUSD
	}
	if err := c.checkPlanner(req.Planner, req.ORModel); err != nil {
		return nil, 409, err
	}
	if opt.Unattended && isCloudPlanner(req.Planner) {
		return nil, 409, errors.New("a scheduled task must use a planner on your nodes, not a paid cloud planner")
	}
	var imgNames []string
	var t *Task
	review := req.Review == nil || *req.Review
	t = &Task{ProjectID: req.Project, ID: newID("task"), Prompt: strings.TrimSpace(req.Prompt), Planner: req.Planner, ClaudeModel: req.ClaudeModel, ORModel: req.ORModel, Review: review,
		Instructions: joinInstructions(set.Instructions, projectInstructions(proj)), Mockup: req.Mockup, TestFirst: req.TestFirst, MaxFix: clampFixRounds(req.FixRounds), TestMinutes: clampTestMinutes(req.TestMinutes), CapUSD: req.CapUSD, Status: "planning", Created: time.Now(), Subtasks: []*SubtaskState{}, Events: []TaskEvent{}, Round: 1,
		NodeLabel: normLabel(req.NodeLabel), Unattended: opt.Unattended, Template: opt.Template}
	t.dir = filepath.Join(c.tasksDir(), t.ID)
	if proj != nil {
		t.repo = proj.repoDir()
		t.Seeded = len(listRepo(t.repo)) > 0
	}
	if err := os.MkdirAll(t.repoDir(), 0o755); err != nil {
		return nil, 500, err
	}
	if err := c.applyAttachments(t, files); err != nil {
		os.RemoveAll(t.dir)
		return nil, 400, err
	}
	if len(req.Images) > 0 {
		imgs, err := decodeImages(req.Images)
		if err == nil {
			err = c.checkPlannerVision(req.Planner, req.ORModel)
		}
		if err == nil {
			imgNames, err = t.saveImages(imgs)
		}
		if err != nil {
			os.RemoveAll(t.dir)
			return nil, 400, err
		}
	}
	t.Events = append(t.Events, TaskEvent{Time: time.Now(), Level: "info", Text: "Task created; planning with " + c.plannerLabel(t.Planner, t.ClaudeModel, t.ORModel) + "."})
	t.mu.Lock()
	t.addMsgLocked(TaskMessage{Role: "user", Kind: "text", Text: t.Prompt, Images: imgNames})
	t.mu.Unlock()
	c.mu.Lock()
	c.tasks[t.ID] = t
	c.mu.Unlock()
	c.startTask(t, c.planTask)
	return t, 200, nil
}

// isCloudPlanner reports whether a planner is billed per call (and so has a cap).
// autoPlanner picks the planner for "Auto": the ready node with the biggest
// loaded model (the same order workers use), so the choice is stored on the
// task as a concrete node.
func (c *Controller) autoPlanner() (string, error) {
	if w := c.workerNodes(); len(w) > 0 {
		return "node:" + w[0], nil
	}
	return "", errors.New("Auto found no node with a model ready; load a model on a node or pick another planner")
}

func isCloudPlanner(p string) bool { return p == "claude" || p == "openrouter" }

// checkPlanner checks a planner can be used; orModel is the OpenRouter model
// the task would use.
func (c *Controller) checkPlanner(p, orModel string) error {
	if p == "claude" {
		if c.anthropicKey() == "" {
			return errors.New("add a Claude API key in Settings, or pick a node as the planner")
		}
		return nil
	}
	if p == "openrouter" {
		if c.openRouterKey() == "" {
			return errors.New("add an OpenRouter API key in Settings, or pick another planner")
		}
		if orModel == "" {
			return errors.New("pick an OpenRouter model in Settings")
		}
		return nil
	}
	id := strings.TrimPrefix(p, "node:")
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.nodes[id]; !ok || !strings.HasPrefix(p, "node:") {
		return errors.New("pick Claude, OpenRouter or a node as the planner")
	}
	return nil
}

func (c *Controller) plannerLabel(p, model, orModel string) string {
	if p == "claude" {
		return claudePrices[model].Label
	}
	if p == "openrouter" {
		return openRouterLabel(orModel)
	}
	id := strings.TrimPrefix(p, "node:")
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := c.nodes[id]; n != nil {
		return "the local model on " + displayName(n)
	}
	return "node " + id
}

type taskDetail struct {
	*Task
	Files         []repoFile    `json:"files"`
	PlannerLabel  string        `json:"planner_label"`
	ClaudeKeySet  bool          `json:"claude_key_set"`
	WorkersOnline int           `json:"workers_online"`
	ExecNodes     int           `json:"exec_nodes"`
	Activity      *actDetail    `json:"activity,omitempty"`
	Estimate      *planEstimate `json:"estimate,omitempty"` // while the plan waits for approval (estimate.go)
}

type repoFile struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

func listRepo(dir string) []repoFile {
	out := []repoFile{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			info, _ := d.Info()
			var size int64
			if info != nil {
				size = info.Size()
			}
			out = append(out, repoFile{Path: filepath.ToSlash(rel), Bytes: size})
		}
		return nil
	})
	return out
}

func (c *Controller) handleGetTask(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	workers, execs := c.workerCounts()
	t.mu.Lock()
	b, _ := json.Marshal(t)
	planner, model, orModel, status := t.Planner, t.ClaudeModel, t.ORModel, t.Status
	t.mu.Unlock()
	var est *planEstimate
	if status == "awaiting_approval" {
		est = c.estimatePlan(t)
	}
	var copyT Task
	_ = json.Unmarshal(b, &copyT)
	writeJSON(w, 200, taskDetail{Task: &copyT, Files: listRepo(t.repoDir()), PlannerLabel: c.plannerLabel(planner, model, orModel),
		ClaudeKeySet: c.anthropicKey() != "", WorkersOnline: workers, ExecNodes: execs, Activity: c.activityFor(t), Estimate: est})
}

func (c *Controller) handleTaskFile(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	p, err := safeJoin(t.repoDir(), r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad path"})
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "no such file"})
		return
	}
	if len(b) > maxRepoFileSize {
		b = b[:maxRepoFileSize]
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", "attachment; filename=\""+strings.ReplaceAll(filepath.Base(p), "\"", "")+"\"")
	}
	w.Write(b)
}

func (c *Controller) handleDownloadTask(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	if r.URL.Query().Get("artifacts") != "" { // source plus build outputs, streamed
		items := dirItems(t.repoDir(), "")
		items = append(items, dirItems(t.artifactsDir(), "artifacts/")...)
		name := slug(t.Title)
		if name == "" {
			name = t.ID
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-with-artifacts.zip"`, name))
		_ = writeZip(w, items)
		return
	}
	zp := filepath.Join(t.dir, "result.zip")
	if _, err := os.Stat(zp); err != nil {
		if err := zipDir(t.repoDir(), zp); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
	}
	t.mu.Lock()
	name := slug(t.Title)
	t.mu.Unlock()
	if name == "" {
		name = t.ID
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, name))
	http.ServeFile(w, r, zp)
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

func zipDir(src, dst string) error {
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(w, in)
		return err
	})
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func (c *Controller) handleApproveTask(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	var req struct {
		CapUSD float64 `json:"cap_usd"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	t.mu.Lock()
	if t.Status != "awaiting_approval" {
		t.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "this task is not waiting for approval"})
		return
	}
	if req.CapUSD > 0 {
		t.CapUSD = req.CapUSD
	}
	now := time.Now()
	t.Approved = &now
	t.Status = "running"
	ev := "You approved the plan."
	if isCloudPlanner(t.Planner) {
		ev = fmt.Sprintf("You approved the plan with a %s cap.", fmtUSD(t.CapUSD))
	}
	t.addMsgLocked(TaskMessage{Role: "event", Kind: "event", Text: ev})
	t.mu.Unlock()
	t.logf("info", "Plan approved with a %s cap.", fmtUSD(t.CapUSD))
	c.startTask(t, c.execTask)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleReplanTask(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	var req struct {
		Feedback string `json:"feedback"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	t.mu.Lock()
	if t.Status != "awaiting_approval" && t.Status != "failed" {
		t.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "the plan can only be revised before it is approved"})
		return
	}
	t.Feedback = strings.TrimSpace(req.Feedback)
	if t.Feedback != "" {
		t.addMsgLocked(TaskMessage{Role: "user", Kind: "text", Text: t.Feedback})
	}
	t.mu.Unlock()
	c.replan(t, "Revising the plan: "+firstLine(req.Feedback, 200))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	t.mu.Lock()
	cancel := t.cancel
	active := t.Status != "done" && t.Status != "failed" && t.Status != "cancelled"
	if active {
		t.AutoResume = false // a task stopped while waiting to resume stays stopped
	}
	t.mu.Unlock()
	if !active {
		writeJSON(w, 409, map[string]string{"error": "task already finished"})
		return
	}
	if cancel != nil {
		cancel()
	}
	t.setStatus("cancelled")
	t.logf("warn", "Cancelled.")
	t.addMsg(TaskMessage{Role: "event", Kind: "event", Text: "You stopped the task. Files written so far are kept; send a message to continue from here."})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	t.mu.Lock()
	if t.cancel != nil {
		t.cancel()
	}
	t.mu.Unlock()
	c.mu.Lock()
	delete(c.tasks, t.ID)
	c.mu.Unlock()
	os.RemoveAll(t.dir)
	c.spendDeleted(t.ID) // its cost stays on the Spending page, marked deleted
	writeJSON(w, 200, map[string]bool{"ok": true})
}

const maxTaskTitle = 120

// handleTaskMeta renames, pins or resolves a task. Either field may be left out. An
// empty title is refused; the planner never overwrites a title that is set.
func (c *Controller) handleTaskMeta(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	var req struct {
		Title    *string `json:"title"`
		Pinned   *bool   `json:"pinned"`
		Resolved *bool   `json:"resolved"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	var title string
	if req.Title != nil {
		title = strings.Join(strings.Fields(*req.Title), " ")
		if title == "" {
			writeJSON(w, 400, map[string]string{"error": "the name cannot be empty"})
			return
		}
		if r := []rune(title); len(r) > maxTaskTitle {
			title = string(r[:maxTaskTitle])
		}
	}
	t.mu.Lock()
	if req.Title != nil {
		t.Title = title
	}
	if req.Pinned != nil {
		t.Pinned = *req.Pinned
	}
	if req.Resolved != nil {
		if *req.Resolved {
			now := time.Now()
			t.ResolvedAt = &now
		} else {
			t.ResolvedAt = nil
		}
	}
	t.saveLocked()
	s := t.summaryLocked()
	t.mu.Unlock()
	if req.Title != nil {
		c.spendRename(t.ID, title)
	}
	writeJSON(w, 200, s)
}

// handleResumeTask continues a paused task (after raising the cap or switching
// to a local planner) or restarts an interrupted/failed one where it stopped.
func (c *Controller) handleResumeTask(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	var req struct {
		CapUSD  float64 `json:"cap_usd"`
		Planner string  `json:"planner"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	if req.Planner != "" && t != nil {
		t.mu.Lock()
		orModel := t.ORModel
		t.mu.Unlock()
		if orModel == "" {
			c.mu.Lock()
			orModel = c.settings.OpenRouterModel
			c.mu.Unlock()
		}
		if err := c.checkPlanner(req.Planner, orModel); err != nil {
			writeJSON(w, 409, map[string]string{"error": err.Error()})
			return
		}
	}
	t.mu.Lock()
	if req.CapUSD > 0 {
		t.CapUSD = req.CapUSD
	}
	if req.Planner != "" {
		t.Planner = req.Planner
		if req.Planner == "openrouter" && t.ORModel == "" {
			c.mu.Lock()
			t.ORModel = c.settings.OpenRouterModel
			c.mu.Unlock()
		}
	}
	status, from, ch := t.Status, t.PausedFrom, t.resume
	t.saveLocked()
	t.mu.Unlock()
	switch status {
	case "paused_cap":
		if ch != nil {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	case "interrupted", "failed":
		c.restartTask(t, status, from, "Resumed.")
	default:
		writeJSON(w, 409, map[string]string{"error": "this task is not paused or interrupted"})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// restartTask moves an interrupted or failed task back to where it stopped and
// starts it again; note is what the thread shows.
func (c *Controller) restartTask(t *Task, status, from, note string) {
	t.mu.Lock()
	t.Error = ""
	if from == "" || status == "failed" {
		if t.Plan == nil {
			from = "planning"
		} else if t.Approved == nil {
			from = "awaiting_approval"
		} else {
			from = "running"
		}
	}
	t.PausedFrom = ""
	t.AutoResume = false
	if status == "failed" && from == "running" {
		// Try again: failed subtasks run again instead of staying failed.
		if n := t.resetFailedSubtasksLocked(); n > 0 {
			note = "Trying again: " + strconv.Itoa(n) + " failed subtask(s) run again; finished ones are kept."
		}
	}
	t.mu.Unlock()
	switch from {
	case "planning":
		t.setStatus("planning")
		c.startTask(t, c.planTask)
	case "awaiting_approval":
		t.setStatus("awaiting_approval")
	default:
		t.setStatus("running")
		c.startTask(t, c.execTask)
	}
	t.logf("info", "%s", note)
	t.addMsg(TaskMessage{Role: "event", Kind: "event", Text: note})
}

func (c *Controller) startTask(t *Task, fn func(ctx context.Context, t *Task)) {
	ctx, cancel := context.WithCancel(context.Background())
	ctx = withCacheScope(ctx, t.ID)
	t.mu.Lock()
	if t.cancel != nil {
		t.cancel()
	}
	t.cancel = cancel
	t.resume = make(chan struct{}, 1)
	t.mu.Unlock()
	go func() {
		defer func() {
			if p := recover(); p != nil {
				log.Printf("task %s crashed: %v", t.ID, p)
				t.mu.Lock()
				t.Error = fmt.Sprint(p)
				t.mu.Unlock()
				t.setStatus("failed")
			}
			c.closeCacheScope(t.ID) // nodes delete this task's build cache
		}()
		fn(ctx, t)
	}()
}
