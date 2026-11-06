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
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
