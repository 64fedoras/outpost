package main

// Task orchestration. A task goes:
//   planning (→ awaiting_answers → planning) → awaiting_approval → running
//   (subtasks on worker nodes) → testing ⇄ fixing → done
// and is a conversation (tasks_chat.go): a message after it finishes starts
// another round on the same project files.
// The planner is Claude, a model on OpenRouter, or a local model on a chosen
// node. Workers are the nodes that have a model loaded. A task's cloud spend is metered per call and
// the task pauses (paused_cap) before a call that would pass its cap.

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
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxSubtasks     = 16
	workerAttempts  = 3
	maxFixRounds    = 3
	testTimeout     = 10 * time.Minute
	inferTimeout    = 30 * time.Minute
	maxRepoFileSize = 1 << 20
)

type Plan struct {
	Title        string        `json:"title"`
	Summary      string        `json:"summary"`
	Language     string        `json:"language"`
	SetupCommand string        `json:"setup_command"`
	TestCommand  string        `json:"test_command"`
	BuildCommand string        `json:"build_command,omitempty"` // runs after the tests pass
	Artifacts    []string      `json:"artifacts,omitempty"`     // glob patterns (relative to the project root) of files the build produces
	Subtasks     []PlanSubtask `json:"subtasks"`
	Delete       []string      `json:"delete,omitempty"` // files to remove this round (fileops.go)
	Rename       []PlanRename  `json:"rename,omitempty"` // files to move this round
	Note         string        `json:"note,omitempty"`   // a sentence or two to the user, shown with the plan
	// Visual checks (visual.go): what to screenshot after the work.
	PreviewPage    string `json:"preview_page,omitempty"`    // HTML file to render
	PreviewCommand string `json:"preview_command,omitempty"` // starts the app (Linux and macOS nodes)
	PreviewURL     string `json:"preview_url,omitempty"`     // where it listens, on the node
	Mockup         bool   `json:"mockup,omitempty"`          // variants to pick from, no real code yet
}

type PlanSubtask struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Files        []string `json:"files"`
	DependsOn    []string `json:"depends_on"`
	Instructions string   `json:"instructions"`
	Difficulty   string   `json:"difficulty,omitempty"` // easy | normal | hard
	Kind         string   `json:"kind,omitempty"`       // "" (write the files) or "investigate" (agent mode: read, search, run, report)
}

type SubtaskState struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"` // pending | running | reviewing | done | failed
	Node       string     `json:"node,omitempty"`
	NodeName   string     `json:"node_name,omitempty"`
	JobIDs     []string   `json:"job_ids,omitempty"`
	Attempts   int        `json:"attempts"`
	Error      string     `json:"error,omitempty"`
	ByPlanner  bool       `json:"by_planner,omitempty"`
	TokPerSec  float64    `json:"tok_per_s,omitempty"`
	Reviewer   string     `json:"reviewer,omitempty"`     // node that reviewed it
	ReviewFix  bool       `json:"review_fixed,omitempty"` // the reviewer changed the files
	ReviewNote string     `json:"review_note,omitempty"`  // why the review was skipped or rejected
	Report     string     `json:"report,omitempty"`       // investigate subtasks: what the agent found
	AgentCalls int        `json:"agent_calls,omitempty"`  // tool calls so far in agent mode
	Waiting    bool       `json:"waiting,omitempty"`      // the agent asked the user something
	BestOf     string     `json:"best_of,omitempty"`      // best-of-N: which node's copy won and why
	Prefer     string     `json:"prefer,omitempty"`       // Redo: "strongest", "planner" or a node id (tasks_redo.go)
	RedoNote   string     `json:"redo_note,omitempty"`    // Redo: the user's note, added to the instructions
	Started    *time.Time `json:"started,omitempty"`
	Ended      *time.Time `json:"ended,omitempty"`
}

type TaskEvent struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"` // info | warn | error | cost
	Text  string    `json:"text"`
}

type Task struct {
	ID           string          `json:"id"`
	Prompt       string          `json:"prompt"`
	Title        string          `json:"title"`
	Pinned       bool            `json:"pinned,omitempty"` // kept above the other groups in the sidebar
	Status       string          `json:"status"`
	PausedFrom   string          `json:"paused_from,omitempty"`
	Planner      string          `json:"planner"` // "claude", "openrouter" or "node:<id>"
	Review       bool            `json:"review"`  // a second node reviews each subtask
	ClaudeModel  string          `json:"claude_model"`
	ORModel      string          `json:"openrouter_model,omitempty"`
	CapUSD       float64         `json:"cap_usd"`
	CostUSD      float64         `json:"cost_usd"`
	EstimateUSD  float64         `json:"estimate_usd"`
	ClaudeCalls  int             `json:"claude_calls"`
	ClaudeIn     int             `json:"claude_in"`
	ClaudeOut    int             `json:"claude_out"`
	ClaudeCacheR int             `json:"claude_cache_read"`
	ClaudeCacheW int             `json:"claude_cache_write"`
	Plan         *Plan           `json:"plan,omitempty"`
	Subtasks     []*SubtaskState `json:"subtasks"`
	Feedback     string          `json:"feedback,omitempty"`
	Events       []TaskEvent     `json:"events"`
	TestStatus   string          `json:"test_status"` // "" | passed | failing | skipped
	TestOutput   string          `json:"test_output,omitempty"`
	BuildStatus  string          `json:"build_status,omitempty"` // "" | passed | failed | skipped
	BuildOutput  string          `json:"build_output,omitempty"`
	Artifacts    []Artifact      `json:"artifacts,omitempty"`
	Cleaned      *time.Time      `json:"cleaned,omitempty"` // old round copies (and maybe build outputs) removed by the clean-up
	FixRounds    int             `json:"fix_rounds"`
	Error        string          `json:"error,omitempty"`
	ResultBytes  int64           `json:"result_bytes"`
	Created      time.Time       `json:"created"`
	Approved     *time.Time      `json:"approved,omitempty"`
	Ended        *time.Time      `json:"ended,omitempty"`
	Messages     []TaskMessage   `json:"messages"`
	Round        int             `json:"round"`                  // 1 for the first request, +1 per follow-up
	Queued       []QueuedNote    `json:"queued,omitempty"`       // notes sent while the planner is answering, not yet given to it
	Steering     []string        `json:"steering,omitempty"`     // notes the user sent while this round ran
	Asked        bool            `json:"asked,omitempty"`        // the planner already asked questions this round
	Touched      []string        `json:"touched,omitempty"`      // files written this round
	Instructions string          `json:"instructions,omitempty"` // standing rules copied from settings when the task was created
	ProjectID    string          `json:"project_id,omitempty"`   // "" = unfiled; see projects.go
	Seeded       bool            `json:"seeded,omitempty"`       // the repo started with files (project or attached zip)
	Attachments  []Attachment    `json:"attachments,omitempty"`
	Mockup       bool            `json:"mockup,omitempty"`         // plan 2 or 3 design variants first (visual.go)
	MaxFix       int             `json:"max_fix_rounds,omitempty"` // fix rounds after failing tests; 0 = maxFixRounds
	TestMinutes  int             `json:"test_minutes,omitempty"`   // time limit for one test run; 0 = testTimeout
	// AutoResume: interrupted by a restart and waiting to resume on its own
	// (local planner only). Saved so a second restart keeps it, and shown in the
	// UI as "Resuming" instead of the Resume card.
	AutoResume bool `json:"auto_resume,omitempty"`
	// NodeLabel limits workers and reviewers to nodes with this label (labels.go).
	NodeLabel string `json:"node_label,omitempty"`
	// Unattended: a schedule started it, so the planner asks nothing and the
	// plan is approved on its own (templates.go). Template is the saved task.
	Unattended bool   `json:"unattended,omitempty"`
	Template   string `json:"template,omitempty"`

	saveTimer *time.Timer // pending delayed save (saveSoonLocked)
	mu        sync.Mutex
	dir       string
	repo      string // working files; the project's repo for a project task, else <dir>/repo
	cancel    context.CancelFunc
	resume    chan struct{}

	asks map[string]chan string // agent questions waiting for an answer, by message id
	act  []ActStep              // what the test-and-fix phase is doing now (tasks_activity.go), memory only
}

func (c *Controller) tasksDir() string { return filepath.Join(c.dataDir, "tasks") }

func (t *Task) repoDir() string {
	if t.repo != "" {
		return t.repo
	}
	return filepath.Join(t.dir, "repo")
}

// saveLocked writes task.json now (safely, keeping the previous save as
// task.json.bak); t.mu must be held.
func (t *Task) saveLocked() {
	defer taskLive.changed(t.ID)
	if t.saveTimer != nil {
		t.saveTimer.Stop()
		t.saveTimer = nil
	}
	b, _ := json.MarshalIndent(t, "", "  ")
	if err := writeFileAtomicBackup(filepath.Join(t.dir, "task.json"), b, 0o644); err != nil {
		log.Printf("task %s: save failed: %v", t.ID, err)
	}
}

// saveSoonLocked saves within saveDelay, merging the saves of a burst of log
// lines into one write; t.mu must be held. State changes use saveLocked.
func (t *Task) saveSoonLocked() {
	taskLive.changed(t.ID)
	if t.saveTimer != nil {
		return
	}
	t.saveTimer = time.AfterFunc(saveDelay, func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.saveTimer != nil {
			t.saveTimer = nil
			t.saveLocked()
		}
	})
}

// flush writes a pending delayed save now (used on shutdown).
func (t *Task) flush() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.saveTimer != nil {
		t.saveLocked()
	}
}

const saveDelay = 250 * time.Millisecond

// damagedDir holds task folders whose task.json could not be read.
const damagedDir = "_damaged"

func (c *Controller) quarantineTask(dir, name string) (string, error) {
	root := filepath.Join(c.tasksDir(), damagedDir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(root, name+"-"+time.Now().Format("20060102-150405"))
	return dst, os.Rename(dir, dst)
}

func (t *Task) logf(level, format string, a ...any) {
	t.mu.Lock()
	t.Events = append(t.Events, TaskEvent{Time: time.Now(), Level: level, Text: fmt.Sprintf(format, a...)})
	if len(t.Events) > 400 {
		t.Events = t.Events[len(t.Events)-400:]
	}
	t.saveSoonLocked()
	t.mu.Unlock()
}

func (t *Task) setStatus(s string) {
	t.mu.Lock()
	t.Status = s
	if s == "done" || s == "failed" || s == "cancelled" {
		now := time.Now()
		t.Ended = &now
	}
	t.saveLocked()
	t.mu.Unlock()
}

func (c *Controller) loadTasks() {
	c.tasks = map[string]*Task{}
	entries, _ := os.ReadDir(c.tasksDir())
	for _, e := range entries {
		if !e.IsDir() || e.Name() == damagedDir {
			continue
		}
		dir := filepath.Join(c.tasksDir(), e.Name())
		t := &Task{}
		usedBak, err := readWithBackup(filepath.Join(dir, "task.json"), func(b []byte) error {
			*t = Task{}
			if err := json.Unmarshal(b, t); err != nil {
				return err
			}
			if t.ID != e.Name() {
				return fmt.Errorf("task id %q does not match its folder", t.ID)
			}
			return nil
		})
		if err != nil {
			// Never drop a task silently: move it aside and say so.
			if dst, merr := c.quarantineTask(dir, e.Name()); merr == nil {
				c.noteRecovered(fmt.Sprintf("Task %s could not be read (%v). Its folder was moved to %s.", e.Name(), err, dst))
			} else {
				c.noteRecovered(fmt.Sprintf("Task %s could not be read (%v) and could not be moved aside (%v).", e.Name(), err, merr))
			}
			continue
		}
		if usedBak {
			c.noteRecovered(fmt.Sprintf("Task %q: task.json was damaged; loaded the previous save, so the last few events may be missing.", firstNonEmpty(t.Title, t.ID)))
		}
		t.dir = dir
		if p := c.projects[t.ProjectID]; p != nil {
			t.repo = p.repoDir()
		} else {
			t.ProjectID = "" // its project is gone; the task keeps its conversation only
		}
		switch t.Status {
		case "queued", "planning", "running", "testing", "building", "fixing", "paused_cap":
			t.PausedFrom = t.Status
			t.Status = "interrupted"
			for _, s := range t.Subtasks {
				if s.Status == "running" {
					s.Status = "pending"
				}
				if s.Status == "written" || s.Status == "reviewing" {
					s.Status = "done" // files are on disk; the review is skipped
				}
			}
			msg := "The controller restarted while this task was active. Click Resume to continue where it stopped."
			t.AutoResume = autoResumeEligible(t) && !c.settings.AutoResumeOff
			if t.AutoResume {
				msg = "The controller restarted while this task was active. It resumes on its own once a node with a loaded model is back."
			}
			t.Events = append(t.Events, TaskEvent{Time: time.Now(), Level: "warn", Text: msg})
			t.saveLocked()
		case "interrupted":
			// Interrupted by an earlier restart and still waiting: keep waiting
			// only if it may resume on its own.
			if t.AutoResume && (!autoResumeEligible(t) || c.settings.AutoResumeOff) {
				t.AutoResume = false
				t.saveLocked()
			}
		default:
			t.AutoResume = false
		}
		for i := range t.Messages {
			t.Messages[i].Pending = false // no agent is waiting after a restart
		}
		for _, s := range t.Subtasks {
			s.Waiting = false
		}
		t.migrateMessages()
		c.tasks[t.ID] = t
	}
}

// ---------- HTTP ----------

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
}

func (t *Task) summaryLocked() taskSummary {
	s := taskSummary{ID: t.ID, Title: t.Title, Status: t.Status, Planner: t.Planner, CostUSD: t.CostUSD, CapUSD: t.CapUSD,
		ProjectID: t.ProjectID, Total: len(t.Subtasks), TestStatus: t.TestStatus, Created: t.Created, Ended: t.Ended, ResultBytes: t.ResultBytes, Round: t.Round, Updated: t.Created, Pinned: t.Pinned, AutoResume: t.AutoResume}
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
	return s
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
		Instructions: joinInstructions(set.Instructions, projectInstructions(proj)), Mockup: req.Mockup, MaxFix: clampFixRounds(req.FixRounds), TestMinutes: clampTestMinutes(req.TestMinutes), CapUSD: req.CapUSD, Status: "planning", Created: time.Now(), Subtasks: []*SubtaskState{}, Events: []TaskEvent{}, Round: 1,
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
	Files         []repoFile `json:"files"`
	PlannerLabel  string     `json:"planner_label"`
	ClaudeKeySet  bool       `json:"claude_key_set"`
	WorkersOnline int        `json:"workers_online"`
	ExecNodes     int        `json:"exec_nodes"`
	Activity      *actDetail `json:"activity,omitempty"`
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
	planner, model, orModel := t.Planner, t.ClaudeModel, t.ORModel
	t.mu.Unlock()
	var copyT Task
	_ = json.Unmarshal(b, &copyT)
	writeJSON(w, 200, taskDetail{Task: &copyT, Files: listRepo(t.repoDir()), PlannerLabel: c.plannerLabel(planner, model, orModel),
		ClaudeKeySet: c.anthropicKey() != "", WorkersOnline: workers, ExecNodes: execs, Activity: c.activityFor(t)})
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
	writeJSON(w, 200, map[string]bool{"ok": true})
}

const maxTaskTitle = 120

// handleTaskMeta renames or pins a task. Either field may be left out. An
// empty title is refused; the planner never overwrites a title that is set.
func (c *Controller) handleTaskMeta(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	var req struct {
		Title  *string `json:"title"`
		Pinned *bool   `json:"pinned"`
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
	t.saveLocked()
	s := t.summaryLocked()
	t.mu.Unlock()
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
		}()
		fn(ctx, t)
	}()
}

// ---------- model calls ----------

// errCancelled marks work stopped by the user.
var errCancelled = errors.New("cancelled")

// plannerCall runs one request on the task's planner. For Claude and
// OpenRouter it enforces the soft cap: the task pauses before a call that would exceed it and waits
// for the user to raise the cap or switch to a local planner.
func (c *Controller) plannerCall(ctx context.Context, t *Task, req llmRequest) (*llmResponse, error) {
	for {
		t.mu.Lock()
		planner, model, orModel, cost, cap := t.Planner, t.ClaudeModel, t.ORModel, t.CostUSD, t.CapUSD
		t.mu.Unlock()
		if !isCloudPlanner(planner) {
			return c.nodeCall(ctx, t, strings.TrimPrefix(planner, "node:"), req, true)
		}
		var est float64
		if planner == "openrouter" {
			_ = openRouterModels.refresh(ctx, c.openRouterKey(), false) // for prices; cached for an hour
			est = openRouterEstimate(orModel, req.size(), req.MaxTokens/2)
		} else {
			est = estimateCost(model, req.size(), req.MaxTokens/2)
			if req.WebSearch {
				est += webSearchUses * webSearchUSD
			}
		}
		if cost+est > cap || (est == 0 && cost >= cap) {
			if err := c.pauseForCap(ctx, t, est); err != nil {
				return nil, err
			}
			continue
		}
		var resp *llmResponse
		var err error
		who := "Claude"
		if planner == "openrouter" {
			who = "OpenRouter"
		}
		live := taskLive.begin(t.ID, req.Sub, req.Label, who, true)
		req.Live = live
		if planner == "openrouter" {
			resp, err = callOpenRouter(ctx, c.openRouterKey(), orModel, req)
		} else {
			resp, err = callClaude(ctx, c.anthropicKey(), model, req)
		}
		live.End()
		if err != nil {
			if ctx.Err() != nil {
				// A call that is cancelled (Stop, Send now) returns no usage, so nothing is added to
				// the meter; the provider may still bill the tokens it had generated by then.
				t.logf("info", "%s: call cancelled; its partial answer is discarded and not counted in the cost meter.", req.Label)
				return nil, errCancelled
			}
			return nil, err
		}
		t.mu.Lock()
		t.CostUSD += resp.CostUSD
		t.ClaudeCalls++
		t.ClaudeIn += resp.InTokens
		t.ClaudeOut += resp.OutTokens
		t.ClaudeCacheR += resp.CacheRead
		t.ClaudeCacheW += resp.CacheWrite
		total := t.CostUSD
		t.saveLocked()
		t.mu.Unlock()
		t.logf("cost", "%s: %s in / %s out tokens, $%.4f (task total $%.4f)", req.Label, fmtInt(resp.InTokens+resp.CacheRead+resp.CacheWrite), fmtInt(resp.OutTokens), resp.CostUSD, total)
		if resp.WebSearches > 0 {
			t.logf("cost", "%s: %d web search(es), %s of that call", req.Label, resp.WebSearches, fmtUSD(float64(resp.WebSearches)*webSearchUSD))
		}
		return resp, nil
	}
}

// fmtUSD shows enough digits that small amounts do not round to $0.00.
func fmtUSD(v float64) string {
	if v >= 0.1 || v == 0 {
		return fmt.Sprintf("$%.2f", v)
	}
	return fmt.Sprintf("$%.3f", v)
}

func fmtInt(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func (c *Controller) pauseForCap(ctx context.Context, t *Task, est float64) error {
	t.mu.Lock()
	from := t.Status
	t.PausedFrom, t.Status = from, "paused_cap"
	ch := t.resume
	cost, cap := t.CostUSD, t.CapUSD
	t.saveLocked()
	t.mu.Unlock()
	c.notifyTask(t, "cap", fmt.Sprintf("Paused at %s of the %s cap. Raise the cap or switch planner to continue.", fmtUSD(cost), fmtUSD(cap)))
	t.logf("warn", "Paused: the next cloud model call (about %s) would pass the %s cap (spent %s). Raise the cap or switch to a local planner to continue.", fmtUSD(est), fmtUSD(cap), fmtUSD(cost))
	select {
	case <-ctx.Done():
		return errCancelled
	case <-ch:
	}
	t.mu.Lock()
	t.Status, t.PausedFrom = from, ""
	t.saveLocked()
	t.mu.Unlock()
	t.logf("info", "Continuing with a %s cap.", fmtUSD(t.CapUSD))
	return nil
}

// nodeCall runs one request on a node's local model, waiting for the node to be free.
func (c *Controller) nodeCall(ctx context.Context, t *Task, nodeID string, req llmRequest, reserve bool) (*llmResponse, error) {
	if reserve {
		if err := c.reserveNode(ctx, nodeID); err != nil {
			return nil, err
		}
		defer c.releaseNode(nodeID)
	}
	temp := req.Temperature
	user := ChatMessage{Role: "user", Content: req.User}
	if len(req.Images) > 0 {
		if !c.nodeVision(nodeID) {
			return nil, errNoVisionLocal
		}
		for _, img := range req.Images {
			user.Images = append(user.Images, img.dataURI())
		}
	}
	spec := InferenceSpec{Messages: []ChatMessage{{Role: "system", Content: req.System}, user},
		Params: InferenceParams{Temperature: &temp, MaxTokens: req.MaxTokens, JSONSchema: req.JSONSchema}}
	if len(req.Msgs) > 0 {
		spec.Messages = append([]ChatMessage{{Role: "system", Content: req.System}}, openAIMessages(req.Msgs)...)
		spec.Tools = toolDefs(req.Tools)
	}
	start := time.Now()
	live := taskLive.begin(t.ID, req.Sub, req.Label, c.nodeLabel(nodeID), false)
	defer live.End()
	var out *InferenceOutput
	var err error
	if c.inferStub != nil {
		out, err = c.inferStub(ctx, nodeID, spec)
	} else {
		out, err = c.runInferenceJobLive(ctx, nodeID, spec, req.Label, inferTimeout, live)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, errCancelled
		}
		return nil, err
	}
	return &llmResponse{Text: out.Text, Calls: fromWireCalls(out.ToolCalls), InTokens: out.PromptTokens, OutTokens: out.OutputTokens, Model: out.Model,
		TokensPerSec: out.TokensPerSec, ElapsedSecond: time.Since(start).Seconds()}, nil
}

// ---------- node selection ----------

// workerNodes lists nodes that can run inference now (online, model ready).
func (c *Controller) workerNodes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	now := time.Now()
	for id := range c.conns {
		hb := c.lastHB[id]
		if hb == nil || hb.Paused || !hb.InferenceRdy || hb.LoadedModel == "" || c.pipelines[id] != nil {
			continue
		}
		if n := c.nodes[id]; n != nil && !n.Hours.open(now) {
			continue
		}
		if until, ok := c.cooldown[id]; ok && time.Now().Before(until) {
			continue
		}
		out = append(out, id)
	}
	// Strongest first: the node with the biggest loaded model gets work (and
	// reviews) before a small test model that happens to sort first by ID.
	size := map[string]int64{}
	for _, id := range out {
		size[id] = c.loadedModelBytesLocked(id)
	}
	sort.Slice(out, func(i, j int) bool {
		if size[out[i]] != size[out[j]] {
			return size[out[i]] > size[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

// loadedModelBytesLocked is the size of the model a node has loaded, a rough
// measure of how capable it is. 0 if unknown. c.mu must be held.
func (c *Controller) loadedModelBytesLocked(id string) int64 {
	hb := c.lastHB[id]
	if hb == nil || hb.LoadedModel == "" {
		return 0
	}
	if m := c.models[hb.LoadedModel]; m != nil {
		if b := totalBytes(m.Files); b > 0 {
			return b
		}
		if m.BytesTotal > 0 {
			return m.BytesTotal
		}
	}
	if n := c.nodes[id]; n != nil && n.Hello != nil {
		for _, m := range n.Hello.Models {
			if m.Name == hb.LoadedModel {
				return m.Bytes
			}
		}
	}
	return 0
}

// errRequeue means the node, not the subtask, was the problem: the subtask goes
// back in the queue without using up an attempt.
var errRequeue = errors.New("node not ready")

func isNodeProblem(err error) bool {
	var ei *ErrInfo
	if errors.As(err, &ei) {
		switch ei.Code {
		case "inference_unavailable", "model_missing", "busy", "send_failed", "lost", "model_hung":
			return true
		}
	}
	msg := err.Error()
	return strings.Contains(msg, "node is offline") || strings.Contains(msg, "inference lost")
}

func (c *Controller) coolDown(node string, d time.Duration) {
	c.mu.Lock()
	c.cooldown[node] = time.Now().Add(d)
	c.mu.Unlock()
}

func (c *Controller) execNodes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for id := range c.conns {
		if hb := c.lastHB[id]; hb != nil && hb.Paused {
			continue
		}
		if n := c.nodes[id]; n != nil && n.ExecEnabled && n.Hours.open(time.Now()) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func (c *Controller) workerCounts() (int, int) { return len(c.workerNodes()), len(c.execNodes()) }

// tryReserve claims a node for one inference call (llama-server runs one slot).
func (c *Controller) tryReserve(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reserved[id] >= c.slotsLocked(id) {
		return false
	}
	c.reserved[id]++
	return true
}

func (c *Controller) reserveNode(ctx context.Context, id string) error {
	for {
		if c.tryReserve(id) {
			return nil
		}
		select {
		case <-ctx.Done():
			return errCancelled
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (c *Controller) releaseNode(id string) {
	c.mu.Lock()
	if c.reserved[id] <= 1 {
		delete(c.reserved, id)
	} else {
		c.reserved[id]--
	}
	c.mu.Unlock()
}

func (c *Controller) nodeLabel(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := c.nodes[id]; n != nil {
		return displayName(n)
	}
	return id
}

// nodeCtx returns the context size a node's model was loaded with.
func (c *Controller) nodeCtx(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := c.nodes[id]; n != nil && n.LoadPrefs != nil && n.LoadPrefs.Ctx > 0 {
		return n.LoadPrefs.Ctx
	}
	return 8192
}

// ---------- planning ----------

const plannerSystem = `You are the planner for Outpost, which splits a software task across several small local coding models that work in parallel and never talk to each other.

Each worker sees only: the project summary, the list of all subtasks, its own instructions, the files of the subtasks it depends on, and the current content of any existing file it must change. Workers are small models, so:
- Keep each subtask small and concrete: one to three files, ideally under 200 lines in total.
- Every file belongs to exactly one subtask. Use exact paths relative to the project root.
- Spell out interfaces in the instructions: module names, function and class names, signatures, data formats, and which file defines them. Two subtasks must never guess at each other's names.
- Use depends_on when a subtask needs to read another subtask's files.
- Include a README.md that explains how to run the project and its tests.
- Prefer the language's standard library. Avoid anything that needs network access at test time.
- If the project can be tested automatically, include small tests and give a test_command that runs them from the project root on Linux (for example "python3 -m unittest discover -s tests"). Use setup_command only if something must be installed or built first. Leave test_command empty if testing is not practical.
- Tag every subtask with a difficulty: "easy" (boilerplate, config, README, simple data files), "normal", or "hard" (tricky logic, algorithms, or code that many other files depend on). Hard subtasks go to the strongest model, so use the tag sparingly.
- If the project produces something to ship (a binary, a package, a bundle), give a build_command that builds it from the project root (for example "sh build.sh") and artifacts, the glob patterns of the files it produces (for example ["dist/*"]; one * matches within a directory, there is no **). The build runs after the tests pass. Leave both empty for a project that is only source code.
- Use at most 12 subtasks.
- To remove files, list them in "delete"; to move or rename a file, add {"from": "old/path", "to": "new/path"} to "rename" (its content moves with it; a subtask may then change the file at its new path). Do not write a subtask that empties or stubs out a file to "remove" it. Leave both lists empty when nothing is removed or moved.
- If the project has a web page, set preview_page to its HTML file (for example "index.html") so it can be screenshotted and checked. For a server app that needs running first, set preview_command (starts it, e.g. "python3 app.py") and preview_url (where it listens, e.g. "http://127.0.0.1:8000"). Leave them empty otherwise.

Follow-up requests on an existing project include its current files. Plan only the files to add or change; a subtask that changes an existing file lists it, and its worker gets the file's current content and outputs the complete new version. Keep the summary describing the whole project, and keep test_command running the whole test suite, and keep build_command and artifacts unless the build changes.

Questions: if the request leaves open a choice that would change the design (language, interface, storage, platform, scope) and a wrong guess would waste the work, you may ask before planning, once per request. Prefer sensible defaults; most requests need no questions. To ask, answer with only:
{"questions": [{"id": "q1", "question": "short question", "options": ["option A", "option B"], "multi": false}], "note": "one sentence on why you are asking"}
Use at most 4 questions with 2 to 4 short options each; the user can also type their own answer.

Otherwise answer with only a JSON object, no other text:
{"title": "short name", "summary": "what the program does and how it is structured, including the key interfaces", "language": "main language", "setup_command": "", "test_command": "", "preview_page": "", "preview_command": "", "preview_url": "", "build_command": "", "artifacts": [], "delete": [], "rename": [], "note": "one or two sentences to the user: the approach and any assumption you made", "subtasks": [{"id": "t1", "title": "short title", "files": ["path/file.ext"], "depends_on": [], "difficulty": "normal", "instructions": "precise instructions"}]}`

// plannerIndexHelp is added to the planner's instructions when the project is
// too large to paste in, so it sees an index instead.
const plannerIndexHelp = `

This project is large, so you see an index (each file's path, size and top-level declarations) instead of the file contents. If you need to read some files before planning, answer once with only {"need_files": ["path/one", "path/two"]} (at most 12 paths) and they will be added. Otherwise plan from the index; a worker is given the current content of every file its subtask changes and the declarations of the files it depends on.`

func (c *Controller) planTask(ctx context.Context, t *Task) {
	t.mu.Lock()
	mayAsk := !t.Asked && !t.Unattended
	t.flushQueuedLocked() // notes still waiting (Send now, a re-plan) join the round's messages, so they are in this prompt
	t.mu.Unlock()
	budget := c.plannerBudget(t)
	user := c.plannerPrompt(t, budget, nil)
	user += c.webContext(ctx, c.roundURLs(t))
	c.mu.Lock()
	webSearch := c.settings.WebSearch
	c.mu.Unlock()
	t.mu.Lock()
	webSearch = webSearch && t.Planner == "claude"
	t.mu.Unlock()
	indexed := t.roundHasFiles() && repoNeedsIndex(t.repoDir(), budget)
	t.mu.Lock()
	mockup := t.Mockup
	t.mu.Unlock()
	fetched := false
	var plan *Plan
	var lastErr error
	for attempt := 0; attempt < 3 && plan == nil; attempt++ {
		req := llmRequest{System: plannerSystem + c.toolsPrompt(), User: user, MaxTokens: 8000, Temperature: 0.2, Label: "Planning", WebSearch: webSearch, Images: t.roundImages()}
		if indexed {
			req.System += plannerIndexHelp
		}
		if mockup {
			req.System += mockupHelp
		}
		if c.agentEnabled() {
			req.System += plannerInvestigateHelp
		}
		if t.Unattended {
			req.User += "\n\nNobody is watching this task (it was started by a schedule), so do not ask questions; produce the plan, using sensible defaults for anything still open."
		} else if !mayAsk {
			req.User += "\n\nYou have already asked your questions for this request. Do not ask again; produce the plan, using sensible defaults for anything still open."
		}
		if attempt > 0 {
			req.User += "\n\nYour previous answer could not be used: " + lastErr.Error() + ". Answer again with only the JSON object."
			if parseFailed(lastErr) && !t.cloudPlanner() {
				// A local model wrote something that is not JSON: this time llama-server
				// holds it to the plan's shape (idea 8, only as a retry).
				req.JSONSchema = planSchema
				t.logf("info", "Asking the planner again with the answer held to the plan's JSON shape.")
			}
		}
		resp, err := c.plannerCall(ctx, t, req)
		if err != nil {
			if ctx.Err() == nil {
				c.failTask(t, err)
			}
			return
		}
		if ctx.Err() != nil { // sent now, stopped or steered: a newer pass owns the task
			return
		}
		if notes := t.takeQueued(); len(notes) > 0 {
			// The user added to the request while this answer was being written.
			// Keep the answer and give the planner the note in the same conversation.
			user += foldNotesPrompt(resp.Text, notes)
			t.logf("info", "Giving the planner your note (%d) with its answer so far.", len(notes))
			attempt-- // not a failed attempt
			continue
		}
		raw := []byte(extractJSON(resp.Text))
		var need struct {
			NeedFiles []string `json:"need_files"`
		}
		if indexed && !fetched && json.Unmarshal(raw, &need) == nil && len(need.NeedFiles) > 0 {
			fetched = true
			user = c.plannerPrompt(t, budget, need.NeedFiles)
			t.logf("info", "The planner asked for %d file(s) from the project index.", len(need.NeedFiles))
			attempt-- // the file exchange is not a failed attempt
			continue
		}
		var ask struct {
			Questions []Question `json:"questions"`
			Note      string     `json:"note"`
		}
		if json.Unmarshal(raw, &ask) == nil && len(ask.Questions) > 0 {
			if !mayAsk {
				lastErr = errors.New("you asked questions again; produce the plan instead")
				continue
			}
			qs := cleanQuestions(ask.Questions)
			if len(qs) == 0 {
				lastErr = errors.New("the questions had no usable options")
				continue
			}
			t.mu.Lock()
			if ctx.Err() != nil { // the user steered meanwhile; a newer planning run owns the task
				t.mu.Unlock()
				return
			}
			if len(t.Queued) > 0 { // a note arrived after the last check: plan again with it
				t.mu.Unlock()
				c.replan(t, "Planning again with your note.")
				return
			}
			t.Asked = true
			t.Status = "awaiting_answers"
			t.addMsgLocked(TaskMessage{Role: "assistant", Kind: "questions", Questions: qs, Text: strings.TrimSpace(ask.Note)})
			t.mu.Unlock()
			c.notifyTask(t, "question", fmt.Sprintf("The planner has %d question(s) for you.", len(qs)))
			t.logf("info", "The planner has %d question(s) before planning.", len(qs))
			return
		}
		p := &Plan{}
		if err := json.Unmarshal(raw, p); err != nil {
			lastErr = &parseError{fmt.Errorf("not valid JSON (%v)", err)}
			continue
		}
		mergeSharedFiles(p)
		if err := validatePlan(p); err != nil {
			lastErr = err
			continue
		}
		if mockup {
			p.Mockup, p.SetupCommand, p.TestCommand = true, "", ""
			if err := validateMockupPlan(p); err != nil {
				lastErr = err
				continue
			}
		}
		plan = p
	}
	if plan == nil {
		c.failTask(t, fmt.Errorf("the planner did not produce a usable plan: %v", lastErr))
		return
	}
	t.mu.Lock()
	if ctx.Err() != nil { // the user steered meanwhile; a newer planning run owns the task
		t.mu.Unlock()
		return
	}
	if len(t.Queued) > 0 { // a note arrived after the last check: plan again with it
		t.mu.Unlock()
		c.replan(t, "Planning again with your note.")
		return
	}
	t.Plan = plan
	if t.Title == "" {
		t.Title = plan.Title
	}
	t.Subtasks = make([]*SubtaskState, len(plan.Subtasks))
	for i, s := range plan.Subtasks {
		t.Subtasks[i] = &SubtaskState{ID: s.ID, Status: "pending"}
	}
	t.EstimateUSD = 0
	if isCloudPlanner(t.Planner) {
		files := 0
		for _, s := range plan.Subtasks {
			files += len(s.Files)
		}
		repoTok := files * 1200
		var fix float64
		if t.Planner == "openrouter" {
			fix = openRouterEstimate(t.ORModel, (repoTok+3000)*3, (repoTok/2+500)*3)
		} else {
			fix = usageCost(t.ClaudeModel, repoTok+3000, repoTok/2+500, 0, 0)
		}
		t.EstimateUSD = t.CostUSD + 1.5*fix
	}
	t.Status = "awaiting_approval"
	cp := *plan
	t.addMsgLocked(TaskMessage{Role: "assistant", Kind: "plan", Plan: &cp, Text: strings.TrimSpace(plan.Note)})
	if t.Unattended && t.Round == 1 {
		now := time.Now()
		t.Approved = &now
		t.Status = "running"
		t.addMsgLocked(TaskMessage{Role: "event", Kind: "event", Text: "Approved on its own: this task was started by a schedule."})
		t.mu.Unlock()
		t.logf("info", "Plan ready: %d subtasks; approved on its own (scheduled).", len(plan.Subtasks))
		c.startTask(t, c.execTask)
		return
	}
	t.mu.Unlock()
	c.notifyTask(t, "plan", fmt.Sprintf("Plan ready with %d subtasks. Review it and approve to start.", len(plan.Subtasks)))
	t.logf("info", "Plan ready: %d subtasks. Review it and approve to start.", len(plan.Subtasks))
}

// cleanQuestions trims the planner's questions to what the UI can show.
func cleanQuestions(in []Question) []Question {
	var out []Question
	for i, q := range in {
		if len(out) == 4 {
			break
		}
		q.Question = strings.TrimSpace(q.Question)
		var opts []string
		for _, o := range q.Options {
			if o = strings.TrimSpace(o); o != "" && len(opts) < 5 {
				opts = append(opts, o)
			}
		}
		if q.Question == "" {
			continue
		}
		q.Options = opts
		q.ID = fmt.Sprintf("q%d", i+1)
		out = append(out, q)
	}
	return out
}

// mergeSharedFiles folds subtasks that claim the same file into the first one
// that claims it, so two workers never write one file at once. Small models do
// this often (for example an index.html split into "markup" and "script").
// The merged subtask keeps every file and instruction, and anything that
// depended on a removed subtask now depends on the survivor.
func mergeSharedFiles(p *Plan) {
	norm := func(f string) string { return strings.TrimPrefix(strings.TrimSpace(f), "./") }
	for {
		owner := map[string]int{}
		from, into := -1, -1
		for i, s := range p.Subtasks {
			for _, f := range s.Files {
				f = norm(f)
				if o, ok := owner[f]; ok && o != i {
					from, into = i, o
					break
				}
				owner[f] = i
			}
			if from >= 0 {
				break
			}
		}
		if from < 0 {
			return
		}
		dst, src := &p.Subtasks[into], p.Subtasks[from]
		have := map[string]bool{}
		for _, f := range dst.Files {
			have[norm(f)] = true
		}
		for _, f := range src.Files {
			if !have[norm(f)] {
				dst.Files = append(dst.Files, f)
				have[norm(f)] = true
			}
		}
		if dst.Title != "" && src.Title != "" {
			dst.Title += " + " + src.Title
		}
		if si := strings.TrimSpace(src.Instructions); si != "" {
			dst.Instructions = strings.TrimSpace(dst.Instructions + "\n\n" + si)
		}
		if src.Kind == "" {
			dst.Kind = ""
		}
		if dst.Difficulty != src.Difficulty && src.Difficulty == "hard" {
			dst.Difficulty = "hard"
		}
		dst.DependsOn = append(dst.DependsOn, src.DependsOn...)
		p.Subtasks = append(p.Subtasks[:from], p.Subtasks[from+1:]...)
		for i := range p.Subtasks {
			s := &p.Subtasks[i]
			seen := map[string]bool{}
			var deps []string
			for _, d := range s.DependsOn {
				if d == src.ID {
					d = p.Subtasks[into].ID
				}
				if d != s.ID && !seen[d] {
					seen[d] = true
					deps = append(deps, d)
				}
			}
			s.DependsOn = deps
		}
	}
}

func validatePlan(p *Plan) error {
	if len(p.Subtasks) == 0 && len(p.Delete) == 0 && len(p.Rename) == 0 {
		return errors.New("the plan has no subtasks")
	}
	if len(p.Subtasks) > maxSubtasks {
		return fmt.Errorf("the plan has %d subtasks; use at most 12", len(p.Subtasks))
	}
	ids := map[string]bool{}
	owner := map[string]string{}
	for i := range p.Subtasks {
		s := &p.Subtasks[i]
		s.ID = strings.TrimSpace(s.ID)
		if s.ID == "" || ids[s.ID] {
			return fmt.Errorf("subtask ids must be unique and non-empty (%q)", s.ID)
		}
		ids[s.ID] = true
		s.Difficulty = normDifficulty(s.Difficulty)
		if k := strings.ToLower(strings.TrimSpace(s.Kind)); k == "investigate" {
			s.Kind = k
		} else {
			s.Kind = ""
		}
		if len(s.Files) == 0 && s.Kind != "investigate" {
			return fmt.Errorf("subtask %s lists no files", s.ID)
		}
		for j, f := range s.Files {
			f = strings.TrimPrefix(strings.TrimSpace(f), "./")
			s.Files[j] = f
			if _, err := safeJoin("/repo", f); err != nil || f == "" {
				return fmt.Errorf("subtask %s has a bad file path %q", s.ID, f)
			}
			if o, ok := owner[f]; ok {
				if o == s.ID {
					continue // listed twice by the same subtask
				}
				return fmt.Errorf("file %s is assigned to both %s and %s", f, o, s.ID)
			}
			owner[f] = s.ID
		}
	}
	if err := validateFileOps(p, owner); err != nil {
		return err
	}
	var pats []string
	for _, a := range p.Artifacts {
		a = strings.TrimPrefix(strings.TrimSpace(a), "./")
		if _, err := safeJoin("/repo", a); err != nil || a == "" {
			return fmt.Errorf("the artifact pattern %q is not a path inside the project", a)
		}
		pats = append(pats, a)
	}
	if len(pats) > 20 {
		return errors.New("the plan lists more than 20 artifact patterns")
	}
	p.Artifacts = pats
	for _, s := range p.Subtasks {
		for _, d := range s.DependsOn {
			if !ids[d] {
				return fmt.Errorf("subtask %s depends on unknown subtask %s", s.ID, d)
			}
		}
	}
	// Reject dependency cycles.
	state := map[string]int{}
	deps := map[string][]string{}
	for _, s := range p.Subtasks {
		deps[s.ID] = s.DependsOn
	}
	var visit func(string) bool
	visit = func(id string) bool {
		switch state[id] {
		case 1:
			return false
		case 2:
			return true
		}
		state[id] = 1
		for _, d := range deps[id] {
			if !visit(d) {
				return false
			}
		}
		state[id] = 2
		return true
	}
	for _, s := range p.Subtasks {
		if !visit(s.ID) {
			return errors.New("subtask dependencies form a cycle")
		}
	}
	return nil
}

func (c *Controller) failTask(t *Task, err error) {
	if errors.Is(err, errCancelled) {
		return
	}
	t.mu.Lock()
	t.Error = err.Error()
	t.addMsgLocked(TaskMessage{Role: "assistant", Kind: "error", Text: err.Error()})
	t.mu.Unlock()
	t.setStatus("failed")
	c.notifyTask(t, "failed", err.Error())
	t.logf("error", "%v", err)
}

// ---------- execution ----------

func (c *Controller) executeTask(ctx context.Context, t *Task) {
	t.mu.Lock()
	plan := t.Plan
	t.mu.Unlock()
	if plan == nil {
		c.failTask(t, errors.New("no plan"))
		return
	}
	if err := c.applyFileOps(t, plan); err != nil {
		c.failTask(t, err)
		return
	}
	specs := map[string]PlanSubtask{}
	for _, s := range plan.Subtasks {
		specs[s.ID] = s
	}
	t.mu.Lock()
	for _, st := range t.Subtasks {
		if sp, ok := specs[st.ID]; ok && st.RedoNote != "" && st.Status == "pending" {
			sp.Instructions += "\n\nNote from the user for this redo: " + st.RedoNote
			specs[st.ID] = sp
		}
	}
	t.mu.Unlock()
	type doneMsg struct {
		id  string
		err error
	}
	done := make(chan doneMsg, len(plan.Subtasks))
	running := 0
	waitingLogged := false
	readySince := map[string]time.Time{}
	bonMode := c.bestOfMode()
	groups := map[string]*bonGroup{} // best-of-N groups of running subtasks
	var groupsMu sync.Mutex
	for {
		if ctx.Err() != nil {
			return
		}
		// Pick subtasks whose dependencies are done.
		t.mu.Lock()
		state := map[string]*SubtaskState{}
		pending, failed := 0, 0
		for _, s := range t.Subtasks {
			state[s.ID] = s
			switch s.Status {
			case "pending":
				pending++
			case "failed":
				failed++
			}
		}
		var ready []string
		for _, s := range t.Subtasks {
			if s.Status != "pending" {
				continue
			}
			ok := true
			for _, d := range specs[s.ID].DependsOn {
				if state[d] == nil || state[d].Status != "done" {
					ok = false
				}
			}
			if ok {
				ready = append(ready, s.ID)
			}
		}
		t.mu.Unlock()
		if pending == 0 && running == 0 {
			if failed > 0 {
				c.failTask(t, fmt.Errorf("%d subtask(s) could not be completed", failed))
				return
			}
			break
		}
		if len(ready) == 0 && running == 0 {
			c.failTask(t, errors.New("remaining subtasks depend on subtasks that failed"))
			return
		}
		workers := c.taskWorkers(t)
		if len(workers) == 0 && len(ready) > 0 && running == 0 && !waitingLogged {
			if t.NodeLabel != "" {
				t.logf("warn", "Waiting for a node labelled %q with a loaded model.", t.NodeLabel)
			} else {
				t.logf("warn", "Waiting for a node with a loaded model. Load one on the Nodes tab.")
			}
			waitingLogged = true
			c.wakeForWork(t.NodeLabel)
		}
		// Hard subtasks choose first, so they get the strongest free node.
		rank := map[string]int{"hard": 0, "normal": 1, "easy": 2}
		sort.SliceStable(ready, func(i, j int) bool {
			return rank[normDifficulty(specs[ready[i]].Difficulty)] < rank[normDifficulty(specs[ready[j]].Difficulty)]
		})
		scores := c.scoreNodes(workers)
		placed := 0
		var newGroups []*bonGroup
		var newSpecs []PlanSubtask
		for _, id := range ready {
			if _, ok := readySince[id]; !ok {
				readySince[id] = time.Now()
			}
			t.mu.Lock()
			prefer := state[id].Prefer
			t.mu.Unlock()
			if prefer == "planner" {
				placed++
				delete(readySince, id)
				running++
				c.redoByPlanner(ctx, t, specs[id], state[id], func(err error) { done <- doneMsg{id, err} })
				continue
			}
			free := c.freeScores(scores, "")
			if len(free) == 0 {
				break
			}
			diff := normDifficulty(specs[id].Difficulty)
			if prefer == "strongest" {
				diff = "hard"
			}
			node := chooseNode(diff, free, scores, time.Since(readySince[id]))
			if prefer != "" && prefer != "strongest" {
				node = c.preferredNode(t, state[id], free, time.Since(readySince[id]))
			}
			if node == "" || !c.tryReserve(node) {
				continue // a hard subtask waiting for the strongest model, or the node was just taken
			}
			placed++
			delete(readySince, id)
			waitingLogged = false
			t.mu.Lock()
			st := state[id]
			now := time.Now()
			st.Status, st.Node, st.Started, st.Error = "running", node, &now, ""
			st.NodeName = c.nodeLabel(node)
			t.saveLocked()
			t.mu.Unlock()
			running++
			var grp *bonGroup
			if bonEligible(bonMode, specs[id]) {
				grp = &bonGroup{}
				groupsMu.Lock()
				groups[id] = grp
				groupsMu.Unlock()
				newGroups, newSpecs = append(newGroups, grp), append(newSpecs, specs[id])
			}
			go func(id, node string, grp *bonGroup) {
				var once sync.Once
				primary := node
				release := func() { once.Do(func() { c.releaseNode(primary) }) }
				var err error
				if grp != nil {
					node, err = c.runBestOf(ctx, t, specs[id], node, grp, release)
					groupsMu.Lock()
					delete(groups, id)
					groupsMu.Unlock()
				} else {
					err = c.runSubtask(ctx, t, specs[id], node)
				}
				release()
				if err == nil {
					t.mu.Lock()
					review := t.Review
					t.mu.Unlock()
					if review && specs[id].Kind != "investigate" {
						c.reviewSubtask(ctx, t, specs[id], node)
					}
					t.mu.Lock()
					st := t.subtask(id)
					now := time.Now()
					st.Status, st.Ended = "done", &now
					t.saveLocked()
					t.mu.Unlock()
				}
				done <- doneMsg{id, err}
			}(id, node, grp)
		}
		// Best-of-N: spare nodes nobody is waiting for take extra copies of a
		// subtask that just started; if a ready subtask finds no free node,
		// extra copies give theirs up.
		unplaced := len(ready) - placed
		free := c.freeScores(scores, "")
		if unplaced == 0 {
			for i, grp := range newGroups {
				picked := pickExtraNodes(free)
				var got []string
				for _, n := range picked {
					if c.tryReserve(n) {
						got = append(got, n)
					}
				}
				if len(got) < 1 {
					break
				}
				c.launchExtras(ctx, t, grp, newSpecs[i], got)
				free = c.freeScores(scores, "")
			}
		} else if len(free) == 0 {
			groupsMu.Lock()
			gs := make([]*bonGroup, 0, len(groups))
			ids := make([]string, 0, len(groups))
			for id, g := range groups {
				gs, ids = append(gs, g), append(ids, id)
			}
			groupsMu.Unlock()
			need := unplaced
			for i, g := range gs {
				if need <= 0 {
					break
				}
				need -= c.cancelExtras(t, g, ids[i], need)
			}
		}
		select {
		case <-ctx.Done():
			return
		case m := <-done:
			running--
			if errors.Is(m.err, errCancelled) {
				return
			}
			if errors.Is(m.err, errRequeue) {
				t.mu.Lock()
				st := state[m.id]
				st.Status, st.Node, st.NodeName, st.Started = "pending", "", "", nil
				t.saveLocked()
				t.mu.Unlock()
				continue
			}
			if m.err != nil {
				// Last resort: the planner writes the subtask itself.
				t.logf("warn", "Subtask %s failed on workers (%v); asking the planner to write it.", m.id, m.err)
				err := c.plannerWritesSubtask(ctx, t, specs[m.id])
				if errors.Is(err, errCancelled) {
					return
				}
				t.mu.Lock()
				st := state[m.id]
				now := time.Now()
				st.Ended = &now
				if err != nil {
					st.Status, st.Error = "failed", err.Error()
				} else {
					st.Status, st.ByPlanner, st.Error = "done", true, ""
				}
				t.saveLocked()
				t.mu.Unlock()
			}
		case <-time.After(2 * time.Second):
		}
	}
	c.visualCheck(ctx, t)
	if ctx.Err() != nil {
		return
	}
	c.testAndFix(ctx, t)
}

const workerSystem = `You are a careful coding worker on a team. Write complete, working code for the files you are assigned, following the plan's interfaces exactly so your files fit with your teammates' files.

` + fileFormatHelp + `
Output only your assigned files, with no explanations before or after.`

// subtaskResult is what a worker produced for a subtask, before it is written.
type subtaskResult struct {
	files map[string]string
	resp  *llmResponse
}

// runSubtask asks a worker node to write a subtask's files, retrying with feedback.
func (c *Controller) runSubtask(ctx context.Context, t *Task, spec PlanSubtask, node string) error {
	if spec.Kind == "investigate" {
		return c.investigate(ctx, t, spec, node)
	}
	res, err := c.generateSubtask(ctx, t, spec, node, "", true)
	if err != nil {
		return err
	}
	return c.commitSubtask(t, spec, node, res)
}

// generateSubtask gets the files for a subtask from one node without writing
// them. extra marks a best-of-N copy: it adds to the label and does not count
// as an attempt on the subtask.
func (c *Controller) generateSubtask(ctx context.Context, t *Task, spec PlanSubtask, node, extra string, primary bool) (*subtaskResult, error) {
	ctxTokens := c.nodeCtx(node)
	maxOut := 4096
	if maxOut > ctxTokens/2 {
		maxOut = ctxTokens / 2
	}
	budget := (ctxTokens-maxOut)*3 - len(workerSystem) - 1500
	feedback := ""
	var lastErr error
	for attempt := 1; attempt <= workerAttempts; attempt++ {
		user := c.subtaskPrompt(t, spec, budget) + feedback
		label := fmt.Sprintf("%s · %s%s (attempt %d)", t.Title, spec.ID, extra, attempt)
		t.logf("info", "%s: %s → %s (attempt %d)", spec.ID, spec.Title, c.nodeLabel(node), attempt)
		resp, err := c.nodeCall(ctx, t, node, llmRequest{System: workerSystem, User: user, MaxTokens: maxOut, Temperature: 0.2, Label: label, Sub: spec.ID}, false)
		if err != nil && !errors.Is(err, errCancelled) && isNodeProblem(err) {
			t.logf("warn", "%s is not ready (%v); %s goes back in the queue.", c.nodeLabel(node), err, spec.ID)
			c.coolDown(node, 15*time.Second)
			return nil, errRequeue
		}
		if primary {
			t.mu.Lock()
			t.subtask(spec.ID).Attempts++
			t.mu.Unlock()
		}
		if err != nil {
			if errors.Is(err, errCancelled) {
				return nil, err
			}
			lastErr = err
			continue
		}
		blocks := parseFileBlocks(resp.Text)
		got := map[string]string{}
		for _, b := range blocks {
			got[strings.TrimPrefix(b.Path, "./")] = b.Content
		}
		var missing []string
		for _, f := range spec.Files {
			if strings.TrimSpace(got[f]) == "" {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			lastErr = fmt.Errorf("missing %s", strings.Join(missing, ", "))
			feedback = "\n\nYour previous answer did not include complete content for: " + strings.Join(missing, ", ") +
				". Output every assigned file using the <<<FILE path>>> ... <<<END>>> format."
			continue
		}
		files := map[string]string{}
		for _, f := range spec.Files {
			files[f] = got[f]
		}
		return &subtaskResult{files: files, resp: resp}, nil
	}
	return nil, lastErr
}

// commitSubtask writes a generated result into the task's files.
func (c *Controller) commitSubtask(t *Task, spec PlanSubtask, node string, res *subtaskResult) error {
	for _, f := range spec.Files {
		if err := t.writeFile(f, res.files[f]); err != nil {
			return err
		}
	}
	t.touch(spec.Files...)
	resp := res.resp
	t.mu.Lock()
	st := t.subtask(spec.ID)
	now := time.Now()
	st.Status, st.Ended, st.TokPerSec = "written", &now, resp.TokensPerSec
	t.saveLocked()
	t.mu.Unlock()
	c.recordSpeed(node, resp.TokensPerSec)
	t.logf("info", "%s written on %s (%s output tokens, %.1f tok/s).", spec.ID, c.nodeLabel(node), fmtInt(resp.OutTokens), resp.TokensPerSec)
	return nil
}

func (t *Task) subtask(id string) *SubtaskState {
	for _, s := range t.Subtasks {
		if s.ID == id {
			return s
		}
	}
	s := &SubtaskState{ID: id}
	t.Subtasks = append(t.Subtasks, s)
	return s
}

func (c *Controller) subtaskPrompt(t *Task, spec PlanSubtask, budget int) string {
	return c.subtaskPromptWith(t, spec, budget, true)
}

// subtaskPromptWith builds a worker prompt; withCurrent adds the current
// content of existing files the subtask changes (not wanted for reviews,
// which see the new content anyway).
func (c *Controller) subtaskPromptWith(t *Task, spec PlanSubtask, budget int, withCurrent bool) string {
	t.mu.Lock()
	plan := t.Plan
	t.mu.Unlock()
	var b strings.Builder
	b.WriteString(t.instructionsBlock())
	fmt.Fprintf(&b, "Project: %s\n%s\nLanguage: %s\n", plan.Title, plan.Summary, plan.Language)
	if plan.TestCommand != "" {
		fmt.Fprintf(&b, "Tests run with: %s\n", plan.TestCommand)
	}
	b.WriteString("\nAll subtasks (other workers write the files not assigned to you):\n")
	for _, s := range plan.Subtasks {
		fmt.Fprintf(&b, "- %s %s: %s\n", s.ID, s.Title, strings.Join(s.Files, ", "))
	}
	fmt.Fprintf(&b, "\nYour subtask %s: %s\n%s\n\nWrite these files: %s\n", spec.ID, spec.Title, spec.Instructions, strings.Join(spec.Files, ", "))
	if note := t.steeringText(); note != "" {
		b.WriteString(note)
	}
	// Files this subtask changes that already exist (follow-up rounds).
	var existing []string
	for _, f := range spec.Files {
		if _, err := os.Stat(filepath.Join(t.repoDir(), filepath.FromSlash(f))); err == nil {
			existing = append(existing, f)
		}
	}
	if len(existing) > 0 && withCurrent {
		b.WriteString("\nCurrent content of the existing files you are changing. Output each one complete, with your changes:\n")
		for _, f := range existing {
			data, _ := os.ReadFile(filepath.Join(t.repoDir(), filepath.FromSlash(f)))
			s := string(data)
			if room := budget/2 - b.Len(); len(s) > room {
				if room < 200 {
					room = 200
				}
				if len(s) > room {
					s = s[:room] + "\n…(truncated)\n"
				}
			}
			fmt.Fprintf(&b, "<<<FILE %s>>>\n%s<<<END>>>\n", f, s)
		}
	}
	b.WriteString(c.dependencyReports(t, spec))
	// Dependencies' files, trimmed to fit the worker's context window.
	var depFiles []string
	for _, d := range spec.DependsOn {
		for _, s := range plan.Subtasks {
			if s.ID == d {
				depFiles = append(depFiles, s.Files...)
			}
		}
	}
	if len(depFiles) > 0 {
		b.WriteString("\nFiles you depend on (already written):\n")
		remaining := budget - b.Len()
		indexed := repoNeedsIndex(t.repoDir(), budget*2)
		for _, f := range depFiles {
			data, err := os.ReadFile(filepath.Join(t.repoDir(), filepath.FromSlash(f)))
			if err != nil {
				continue
			}
			s := string(data)
			if indexed {
				// Large project: declarations only, not whole files.
				s = declsText(t.repoDir(), f)
			}
			if remaining <= 200 {
				fmt.Fprintf(&b, "<<<FILE %s>>>\n(omitted to fit the context window)\n<<<END>>>\n", f)
				continue
			}
			if len(s) > remaining-100 {
				s = s[:remaining-100] + "\n…(truncated)\n"
			}
			fmt.Fprintf(&b, "<<<FILE %s>>>\n%s<<<END>>>\n", f, s)
			remaining -= len(s) + len(f) + 30
		}
	}
	return b.String()
}

func writeRepoFile(repo, rel, content string) error {
	if inGitDir(rel) {
		return fmt.Errorf("refusing to write inside .git: %s", rel)
	}
	p, err := safeJoin(repo, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
		mode = st.Mode().Perm() // keep an existing script executable
	}
	return writeFileAtomic(p, []byte(content), mode)
}

// repoSnapshot returns the project's files as text for a planner prompt.
func repoSnapshot(repo string, budget int) string {
	return repoSnapshotFirst(repo, budget, "")
}

// repoSnapshotFirst is repoSnapshot, but files whose names appear in hint (a
// test output) come first, so they are the last to be left out of a small
// prompt.
func repoSnapshotFirst(repo string, budget int, hint string) string {
	files := promptFiles(repo)
	if hint != "" {
		sort.SliceStable(files, func(i, j int) bool {
			return strings.Contains(hint, path.Base(files[i].Path)) && !strings.Contains(hint, path.Base(files[j].Path))
		})
	}
	var b strings.Builder
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(f.Path)))
		if err != nil {
			continue
		}
		s := string(data)
		if b.Len()+len(s) > budget {
			fmt.Fprintf(&b, "<<<FILE %s>>>\n(omitted: too large for this prompt; declarations follow)\n%s<<<END>>>\n", f.Path, declsText(repo, f.Path))
			continue
		}
		fmt.Fprintf(&b, "<<<FILE %s>>>\n%s<<<END>>>\n", f.Path, s)
	}
	return b.String()
}

func (c *Controller) plannerBudget(t *Task) int {
	t.mu.Lock()
	planner, orModel := t.Planner, t.ORModel
	t.mu.Unlock()
	if planner == "claude" {
		return 400000
	}
	if planner == "openrouter" {
		ctxTok := 128000 // unknown model: assume a common window
		if m := openRouterModels.get(orModel); m != nil && m.Ctx > 0 {
			ctxTok = m.Ctx
		}
		return min(400000, max(8000, (ctxTok-16384)*3))
	}
	ctxTok := c.nodeCtx(strings.TrimPrefix(planner, "node:"))
	return (ctxTok - 4096) * 3
}

const plannerWriteSystem = `You are the lead engineer. A worker could not complete a subtask, so you write its files yourself, following the plan's interfaces exactly.

` + fileFormatHelp

func (c *Controller) plannerWritesSubtask(ctx context.Context, t *Task, spec PlanSubtask) error {
	if spec.Kind == "investigate" {
		return c.investigate(ctx, t, spec, "")
	}
	user := c.subtaskPrompt(t, spec, c.plannerBudget(t)/2) + "\n\nCurrent project files:\n" + repoSnapshot(t.repoDir(), c.plannerBudget(t)/2)
	resp, err := c.plannerCall(ctx, t, llmRequest{System: plannerWriteSystem, User: user, MaxTokens: 16000, Temperature: 0.2, Label: "Writing " + spec.ID})
	if err != nil {
		return err
	}
	got := map[string]string{}
	for _, b := range parseFileBlocks(resp.Text) {
		got[strings.TrimPrefix(b.Path, "./")] = b.Content
	}
	for _, f := range spec.Files {
		if strings.TrimSpace(got[f]) == "" {
			return fmt.Errorf("the planner did not write %s", f)
		}
	}
	for _, f := range spec.Files {
		if err := t.writeFile(f, got[f]); err != nil {
			return err
		}
	}
	t.touch(spec.Files...)
	return nil
}

// ---------- testing and fixing ----------

const fixSystem = `You are the lead engineer integrating code written by several small models. The project's tests fail. Find the cause and fix it. Change as few files as needed; you may also add missing files.

The tests describe the intended behaviour: fix the code to match them, and change a test only when the test itself is clearly wrong. If earlier fix rounds are listed and the tests still fail, their approach did not work; look for a different cause instead of repeating it.

` + fileFormatHelp + `
Output only the files you change or add, each one complete.`

func (c *Controller) testAndFix(ctx context.Context, t *Task) {
	t.mu.Lock()
	plan := t.Plan
	t.mu.Unlock()
	cmd := strings.TrimSpace(plan.TestCommand)
	if setup := strings.TrimSpace(plan.SetupCommand); setup != "" && cmd != "" {
		cmd = setup + " && " + cmd
	}
	if cmd == "" {
		t.mu.Lock()
		t.TestStatus = "skipped"
		t.mu.Unlock()
		t.logf("info", "The plan has no automated tests.")
		if !c.runBuild(ctx, t) {
			return
		}
		c.finishTask(t)
		return
	}
	var history []string
	nodeRetries, plannerRetries := 0, 0
	t.actClear()
	for round := 0; ; round++ {
		if ctx.Err() != nil {
			return
		}
		execs := c.execNodes()
		if len(execs) == 0 {
			t.mu.Lock()
			t.TestStatus = "skipped"
			t.mu.Unlock()
			t.logf("warn", "No node has exec enabled, so the tests were not run. Enable exec on a node to test automatically.")
			c.finishTask(t)
			return
		}
		if need := toolsInCommand(cmd); len(need) > 0 {
			capable := c.execNodesWith(execs, need)
			if len(capable) == 0 {
				t.mu.Lock()
				t.TestStatus = "skipped"
				t.mu.Unlock()
				t.logf("warn", "No exec node has %s, so the tests were not run. Install it on a node (Go can be installed from the Nodes tab).", c.missingToolsNote(execs, need))
				c.finishTask(t)
				return
			}
			execs = capable
		}
		node := execs[round%len(execs)]
		t.setStatus("testing")
		t.actBegin(fmt.Sprintf("Running the tests: %s", firstLine(cmd, 100)), node)
		t.logf("info", "Running tests on %s: %s", c.nodeLabel(node), cmd)
		inputs, srcs, skipped := repoJobInputs(t.repoDir())
		if len(skipped) > 0 {
			t.logf("warn", "Not sent to the test node (over the 2 GiB limit): %s", strings.Join(skipped, ", "))
		}
		out, res, err := c.runExecJobFiles(ctx, node, c.execArgv(node, plan.SetupCommand, plan.TestCommand), inputs, srcs, nil, t.Title+" · tests", t.testLimit())
		status := ""
		if res != nil {
			status = res.Status
		}
		if errors.Is(err, errCancelled) || ctx.Err() != nil {
			return
		}
		if err != nil && isNodeProblem(err) && nodeRetries < 2 {
			// The node stopped (paused from its tray, went offline): run the same
			// round again, on another node if one is free, without using up a fix round.
			nodeRetries++
			t.actEnd("warn", fmt.Sprintf("%s stopped during the tests; running them again", c.nodeLabel(node)))
			t.logf("warn", "%s stopped running the tests (%v); trying again.", c.nodeLabel(node), err)
			c.coolDown(node, 15*time.Second)
			round--
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		var output string
		passed := false
		switch {
		case err != nil && status == "timed_out":
			output = "The test command timed out after 10 minutes."
			t.actEnd("bad", "The tests timed out after 10 minutes")
		case err != nil:
			t.actEnd("bad", "Could not run the tests: "+firstLine(err.Error(), 120))
			t.logf("warn", "Could not run the tests: %v", err)
			t.mu.Lock()
			t.TestStatus = "skipped"
			t.mu.Unlock()
			c.finishTask(t)
			return
		default:
			output = strings.TrimSpace(out.StdoutTail + "\n" + out.StderrTail)
			passed = out.ExitCode == 0
			st := "ok"
			if !passed {
				st = "bad"
			}
			t.actEnd(st, fmt.Sprintf("Ran the tests on %s: %s", c.nodeLabel(node), testSummary(output, out.ExitCode)))
		}
		if len(output) > 12000 {
			output = "…" + output[len(output)-12000:]
		}
		if !passed {
			output = withSandboxNote(output, out)
		}
		t.mu.Lock()
		t.TestOutput = output
		t.mu.Unlock()
		if passed {
			t.mu.Lock()
			t.TestStatus = "passed"
			t.mu.Unlock()
			t.logf("info", "Tests passed.")
			if !c.runBuild(ctx, t) {
				return
			}
			c.finishTask(t)
			return
		}
		if round >= t.fixLimit() {
			t.mu.Lock()
			t.TestStatus = "failing"
			t.mu.Unlock()
			t.logf("warn", "Tests still fail after %d fix rounds. The result is available, with the last test output.", t.fixLimit())
			c.finishTask(t)
			return
		}
		t.logf("warn", "Tests failed; fix round %d of %d.", round+1, t.fixLimit())
		t.setStatus("fixing")
		t.actBegin(fmt.Sprintf("Fix round %d of %d: the planner is reading the failures", round+1, t.fixLimit()), "")
		var changed []string
		agentDone := false
		if c.agentEnabled() {
			ch, err := c.agentFix(ctx, t, plan, cmd, output, history, round)
			switch {
			case errors.Is(err, errCancelled) || ctx.Err() != nil:
				return
			case err == nil:
				changed, agentDone = ch, true
			case errors.Is(err, errAgentUnsupported):
				t.logf("warn", "The planner cannot use tools here; fixing with a single request instead.")
			case errors.Is(err, errAgentToolParse):
				t.logf("warn", "The model's tool calls kept getting cut off; fixing with a single request instead.")
			case isNodeProblem(err) && c.waitPlannerBack(ctx, t, err, &plannerRetries):
				round--
				continue
			default:
				c.failTask(t, err)
				return
			}
		}
		if !agentDone {
			budget := c.plannerBudget(t)
			// A local planner has a small window: keep the test output to a third
			// of it so the code under test still fits.
			shown := output
			if limit := budget / 3; len(shown) > limit && limit > 0 {
				shown = "…" + shown[len(shown)-limit:]
			}
			var earlier strings.Builder
			if len(history) > 0 {
				earlier.WriteString("\n\nEarlier fix rounds (the tests still failed after each):\n")
				for _, h := range history {
					earlier.WriteString("- " + h + "\n")
				}
			}
			user := t.instructionsBlock() + fmt.Sprintf("Project: %s\n%s\n\nTest command: %s\n\nTest output:\n%s%s\n\nProject files:\n%s",
				plan.Title, plan.Summary, cmd, shown, earlier.String(),
				repoSnapshotFirst(t.repoDir(), budget-len(shown)-earlier.Len()-4000, output)) + t.steeringText()
			resp, err := c.plannerCall(ctx, t, llmRequest{System: fixSystem, User: user, MaxTokens: 16000, Temperature: 0.2, Label: fmt.Sprintf("Fix round %d", round+1)})
			if err != nil {
				if isNodeProblem(err) && c.waitPlannerBack(ctx, t, err, &plannerRetries) {
					round--
					continue
				}
				c.failTask(t, err)
				return
			}
			for _, b := range parseFileBlocks(resp.Text) {
				p := strings.TrimPrefix(b.Path, "./")
				if err := t.writeFile(p, b.Content); err != nil {
					t.logf("warn", "Ignored a fix for %s: %v", p, err)
					continue
				}
				changed = append(changed, p)
			}
			t.touch(changed...)
		}
		if len(changed) == 0 {
			t.mu.Lock()
			t.TestStatus = "failing"
			t.mu.Unlock()
			t.actEnd("bad", fmt.Sprintf("Fix round %d: the planner proposed no change", round+1))
			t.logf("warn", "The planner did not propose a fix. The result is available with the failing tests.")
			c.finishTask(t)
			return
		}
		t.mu.Lock()
		t.FixRounds++
		t.mu.Unlock()
		history = append(history, fmt.Sprintf("round %d changed %s", round+1, strings.Join(changed, ", ")))
		t.actEnd("ok", fmt.Sprintf("Fix round %d changed %s", round+1, strings.Join(changed, ", ")))
		t.logf("info", "Fix round %d changed: %s", round+1, strings.Join(changed, ", "))
	}
}

// waitPlannerBack handles a local planner node that stopped mid-fix (restarted,
// updated, paused). It waits for the node to be ready again and says so on the
// activity card, so the caller can run the same round again without using one
// up. It returns false when the planner is a cloud one, retries ran out, the
// node did not return within 10 minutes, or the task was stopped.
func (c *Controller) waitPlannerBack(ctx context.Context, t *Task, cause error, retries *int) bool {
	t.mu.Lock()
	planner := t.Planner
	t.mu.Unlock()
	if !strings.HasPrefix(planner, "node:") || *retries >= 3 {
		return false
	}
	*retries++
	node := strings.TrimPrefix(planner, "node:")
	name := c.nodeLabel(node)
	t.actEnd("warn", fmt.Sprintf("%s stopped while fixing (%s)", name, firstLine(cause.Error(), 80)))
	t.actBegin(fmt.Sprintf("Waiting for %s to come back with its model loaded, then repeating this fix round", name), node)
	t.logf("warn", "%s stopped while fixing (%v). Waiting for it to come back, then trying the round again.", name, cause)
	c.coolDown(node, 5*time.Second)
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
		for _, id := range c.workerNodes() {
			if id == node {
				t.actEnd("ok", name+" is back")
				t.logf("info", "%s is back; repeating the fix round.", name)
				return true
			}
		}
	}
	return false
}

func (c *Controller) finishTask(t *Task) {
	t.actClear()
	zp := filepath.Join(t.dir, "result.zip")
	os.Remove(zp)
	if err := zipDir(t.repoDir(), zp); err != nil {
		c.failTask(t, fmt.Errorf("packaging the result: %w", err))
		return
	}
	st, _ := os.Stat(zp)
	t.mu.Lock()
	if st != nil {
		t.ResultBytes = st.Size()
	}
	t.mu.Unlock()
	t.setStatus("done")
	stats := c.roundStats(t)
	t.mu.Lock()
	cost := t.CostUSD
	t.addMsgLocked(TaskMessage{Role: "assistant", Kind: "result", Text: resultText(t), Files: sortedCopy(t.Touched), Stats: stats})
	t.mu.Unlock()
	c.notifyTask(t, "done", "Finished. Download the result from the task page.")
	t.mu.Lock()
	learn := t.ProjectID != "" && t.TestStatus == "failing"
	t.mu.Unlock()
	if learn {
		go c.suggestFromFailure(t)
	}
	t.logf("info", "Finished. Cloud model cost: %s. Download the result from this page.", fmtUSD(cost))
}

// ---------- peer review ----------

const reviewSystem = `You are a careful senior engineer reviewing a teammate's code for one subtask of a larger project. Check it against the subtask instructions and the project's interfaces: names, signatures, imports, file paths, data formats, and obvious bugs.

If the files are correct, answer with exactly:
<<<OK>>>

If something is wrong, output the corrected files, each one complete, and nothing else.
` + fileFormatHelp + `
Only output files that belong to this subtask. Do not rewrite code that is already correct.`

// reviewSubtask asks a different node to check a subtask's files and fix them.
// A review never fails the subtask: if no second node is free, the reviewer
// errors, or its answer looks broken, the writer's files are kept.
func (c *Controller) reviewSubtask(ctx context.Context, t *Task, spec PlanSubtask, writer string) {
	note := func(msg string) {
		t.mu.Lock()
		t.subtask(spec.ID).ReviewNote = msg
		t.saveLocked()
		t.mu.Unlock()
	}
	reviewer := ""
	deadline := time.Now().Add(2 * time.Minute)
	for reviewer == "" {
		others := 0
		workers := c.taskWorkers(t)
		for _, w := range workers {
			if w != writer {
				others++
			}
		}
		scores := c.scoreNodes(workers)
		var ws nodeScore
		for _, s := range scores {
			if s.ID == writer {
				ws = s
			}
		}
		// A reviewer at least as strong as the writer when one is free.
		if w := pickReviewer(ws, c.freeScores(scores, writer)); w != "" && c.tryReserve(w) {
			reviewer = w
			break
		}
		if others == 0 {
			note("skipped: no second node with a model")
			return
		}
		if time.Now().After(deadline) {
			note("skipped: no second node was free for 2 minutes")
			t.logf("warn", "%s: review skipped, no second node was free.", spec.ID)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	defer c.releaseNode(reviewer)

	t.mu.Lock()
	st := t.subtask(spec.ID)
	st.Status, st.Reviewer = "reviewing", c.nodeLabel(reviewer)
	t.saveLocked()
	t.mu.Unlock()
	t.logf("info", "%s: reviewing on %s", spec.ID, c.nodeLabel(reviewer))

	ctxTokens := c.nodeCtx(reviewer)
	maxOut := 4096
	if maxOut > ctxTokens/2 {
		maxOut = ctxTokens / 2
	}
	budget := (ctxTokens-maxOut)*3 - len(reviewSystem) - 1500
	original := map[string]string{}
	var written strings.Builder
	for _, f := range spec.Files {
		b, err := os.ReadFile(filepath.Join(t.repoDir(), filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		original[f] = string(b)
		fmt.Fprintf(&written, "<<<FILE %s>>>\n%s<<<END>>>\n", f, b)
	}
	prompt := c.subtaskPromptWith(t, spec, budget-written.Len(), false)
	prompt = strings.Replace(prompt, "Write these files:", "Files in this subtask:", 1)
	user := prompt + "\nYour teammate wrote:\n" + written.String()
	resp, err := c.nodeCall(ctx, t, reviewer, llmRequest{System: reviewSystem, User: user, MaxTokens: maxOut, Temperature: 0.1,
		Label: fmt.Sprintf("%s · review %s", t.Title, spec.ID), Sub: spec.ID}, false)
	if err != nil {
		if !errors.Is(err, errCancelled) {
			note("skipped: " + err.Error())
			t.logf("warn", "%s: review on %s failed (%v); keeping the original.", spec.ID, c.nodeLabel(reviewer), err)
		}
		return
	}
	blocks := parseFileBlocks(resp.Text)
	if len(blocks) == 0 {
		if strings.Contains(resp.Text, "<<<OK>>>") {
			t.logf("info", "%s: approved by %s.", spec.ID, c.nodeLabel(reviewer))
		} else {
			note("reviewer gave no usable answer")
			t.logf("warn", "%s: %s gave no usable review; keeping the original.", spec.ID, c.nodeLabel(reviewer))
		}
		return
	}
	var changed []string
	for _, b := range blocks {
		p := strings.TrimPrefix(b.Path, "./")
		orig, ok := original[p]
		if !ok {
			continue // not part of this subtask
		}
		// Guard against a truncated rewrite from a small model.
		if strings.TrimSpace(b.Content) == "" || (len(orig) > 400 && len(b.Content) < len(orig)*2/5) {
			t.logf("warn", "%s: ignored %s's rewrite of %s (much shorter than the original, probably cut off).", spec.ID, c.nodeLabel(reviewer), p)
			continue
		}
		if b.Content == orig {
			continue
		}
		if err := t.writeFile(p, b.Content); err == nil {
			changed = append(changed, p)
		}
	}
	if len(changed) == 0 {
		t.logf("info", "%s: approved by %s.", spec.ID, c.nodeLabel(reviewer))
		return
	}
	t.mu.Lock()
	t.subtask(spec.ID).ReviewFix = true
	t.saveLocked()
	t.mu.Unlock()
	t.logf("info", "%s: %s fixed %s.", spec.ID, c.nodeLabel(reviewer), strings.Join(changed, ", "))
}

// roundHasFiles reports whether the task repo holds any files yet.
func (t *Task) roundHasFiles() bool { return len(listRepo(t.repoDir())) > 0 }
