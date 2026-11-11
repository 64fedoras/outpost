package main

// Projects (roadmap row 11).
//
// A project is a folder of files that outlives any one task, plus defaults for the tasks
// started in it. On disk (inside the controller data dir, included in backups):
//
//	projects/<id>/project.json   the Project record below (git secrets encrypted with secret.key)
//	projects/<id>/repo/          the project's current files (a git working tree when GitURL is set)
//
// A task with ProjectID set uses projects/<id>/repo as its repo, so it starts from the project's
// current files and writes straight into them; Task.dir still holds its conversation and
// result.zip. Only one task per project executes at a time: later ones wait in the "queued"
// status, because two tasks writing the same files would clobber each other. Planning is not
// queued (it only reads). After a task finishes "done", a git project commits the round
// (see projects_git.go).
//
// Existing tasks need no migration: Task.ProjectID is empty ("unfiled") and they keep their own
// <task>/repo. A task whose project was deleted becomes unfiled on load. A project with tasks
// cannot be deleted.
//
// Schema version 1. Later rows build on this: project instructions (1.1) will add an
// Instructions field next to the defaults, and the large codebase index will live under
// projects/<id>/ next to repo/.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const projectSchema = 1

// Project is the persisted record. Secrets are stored encrypted and never sent to the browser.
type Project struct {
	Schema  int       `json:"schema"`
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Created time.Time `json:"created"`

	// Defaults for tasks started in the project (a request may still override them).
	Planner     string `json:"planner,omitempty"`
	Review      *bool  `json:"review,omitempty"`
	TestCommand string `json:"test_command,omitempty"`
	FixRounds   int    `json:"fix_rounds,omitempty"`   // fix rounds after failing tests; 0 = default (3)
	TestMinutes int    `json:"test_minutes,omitempty"` // time limit for one test run; 0 = default (10)
	NodeLabel   string `json:"node_label,omitempty"`   // tasks run only on nodes with this label; "" = any node
	// Instructions are this project's standing rules, added after the ones in
	// Settings for every task started in the project.
	Instructions string `json:"instructions,omitempty"`
	// Suggestions are rules proposed after failed tasks (project_suggest.go).
	Suggestions []ProjectSuggestion `json:"suggestions,omitempty"`

	// Git. GitURL empty = a plain folder project.
	GitURL      string `json:"git_url,omitempty"`
	GitBranch   string `json:"git_branch,omitempty"` // branch each round is committed to
	GitBase     string `json:"git_base,omitempty"`   // the remote's default branch, found at clone time
	GitUser     string `json:"git_user,omitempty"`   // HTTPS user name; default x-access-token
	GitTokenEnc string `json:"git_token_enc,omitempty"`
	GitKeyEnc   string `json:"git_ssh_key_enc,omitempty"`
	GitPush     bool   `json:"git_push,omitempty"` // push after each round
	GitPR       bool   `json:"git_pr,omitempty"`   // open a GitHub pull request after pushing
	LastPush    string `json:"last_push,omitempty"`
	LastPR      string `json:"last_pr,omitempty"`

	dir  string
	slot chan struct{} // holds a token while a task executes
	mu   sync.Mutex
}

func (p *Project) repoDir() string { return filepath.Join(p.dir, "repo") }

func (c *Controller) projectsDir() string { return filepath.Join(c.dataDir, "projects") }

func (p *Project) save() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	b, _ := json.MarshalIndent(p, "", "  ")
	return writeFileAtomicBackup(filepath.Join(p.dir, "project.json"), b, 0o600)
}

func (c *Controller) loadProjects() {
	c.projects = map[string]*Project{}
	entries, _ := os.ReadDir(c.projectsDir())
	for _, e := range entries {
		dir := filepath.Join(c.projectsDir(), e.Name())
		p := &Project{}
		usedBak, err := readWithBackup(filepath.Join(dir, "project.json"), func(b []byte) error { *p = Project{}; return json.Unmarshal(b, p) })
		if err != nil || p.ID != e.Name() {
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				c.noteRecovered("project " + e.Name() + ": project.json could not be read (" + err.Error() + ")")
			}
			continue
		}
		if usedBak {
			c.noteRecovered("project " + p.Name + ": project.json was damaged; loaded the previous copy")
		}
		p.dir, p.slot = dir, make(chan struct{}, 1)
		c.projects[p.ID] = p
	}
}

func (c *Controller) getProject(id string) *Project {
	if id == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.projects[id]
}

// applyDefaults fills a new-task request from the project's defaults.
func (p *Project) applyDefaults(req createReq) createReq {
	p.mu.Lock()
	defer p.mu.Unlock()
	if req.Planner == "" {
		req.Planner = p.Planner
	}
	if req.Review == nil {
		req.Review = p.Review
	}
	if req.FixRounds == 0 {
		req.FixRounds = p.FixRounds
	}
	if req.TestMinutes == 0 {
		req.TestMinutes = p.TestMinutes
	}
	if req.NodeLabel == "" {
		req.NodeLabel = p.NodeLabel
	}
	return req
}

func (c *Controller) projectTestCommand(t *Task) string {
	p := c.getProject(t.ProjectID)
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.TestCommand
}

// execTask runs a task's execution phase. Tasks in a project take the project's slot first, so
// one runs at a time; a finished round is then committed (and pushed) before the slot is freed.
func (c *Controller) execTask(ctx context.Context, t *Task) {
	p := c.getProject(t.ProjectID)
	if p == nil {
		c.executeTask(ctx, t)
		return
	}
	select {
	case p.slot <- struct{}{}:
	default:
		t.setStatus("queued")
		t.logf("info", "Waiting: another task is working in project %q.", p.Name)
		t.addMsg(TaskMessage{Role: "event", Kind: "event", Text: "Another task is working in this project. This one starts when it finishes."})
		select {
		case p.slot <- struct{}{}:
		case <-ctx.Done():
			return
		}
		if ctx.Err() != nil {
			<-p.slot
			return
		}
		t.setStatus("running")
	}
	defer func() { <-p.slot }()
	c.executeTask(ctx, t)
	t.mu.Lock()
	done := t.Status == "done"
	t.mu.Unlock()
	if done && p.GitURL != "" {
		c.exportRound(t, p)
	}
}

// ---------- HTTP ----------

type projectView struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Created     time.Time           `json:"created"`
	Planner     string              `json:"planner"`
	Review      *bool               `json:"review"`
	TestCommand string              `json:"test_command"`
	FixRounds   int                 `json:"fix_rounds"`
	TestMinutes int                 `json:"test_minutes"`
	NodeLabel   string              `json:"node_label"`
	Instr       string              `json:"instructions"`
	Suggest     []ProjectSuggestion `json:"suggestions"`
	GitURL      string              `json:"git_url"`
	GitBranch   string              `json:"git_branch"`
	GitBase     string              `json:"git_base"`
	GitUser     string              `json:"git_user"`
	GitToken    bool                `json:"git_token_set"`
	GitKey      bool                `json:"git_ssh_key_set"`
	GitPush     bool                `json:"git_push"`
	GitPR       bool                `json:"git_pr"`
	LastPush    string              `json:"last_push"`
	LastPR      string              `json:"last_pr"`
	Files       int                 `json:"files"`
	Tasks       int                 `json:"tasks"`
}

func (c *Controller) projectViews() []projectView {
	c.mu.Lock()
	ps := make([]*Project, 0, len(c.projects))
	for _, p := range c.projects {
		ps = append(ps, p)
	}
	counts := map[string]int{}
	for _, t := range c.tasks {
		if t.ProjectID != "" {
			counts[t.ProjectID]++
		}
	}
	c.mu.Unlock()
	out := make([]projectView, 0, len(ps))
	for _, p := range ps {
		p.mu.Lock()
		out = append(out, projectView{ID: p.ID, Name: p.Name, Created: p.Created, Planner: p.Planner, Review: p.Review,
			TestCommand: p.TestCommand, FixRounds: p.FixRounds, TestMinutes: p.TestMinutes, NodeLabel: p.NodeLabel, Instr: p.Instructions, Suggest: append([]ProjectSuggestion(nil), p.Suggestions...), GitURL: p.GitURL, GitBranch: p.GitBranch, GitBase: p.GitBase, GitUser: p.GitUser,
			GitToken: p.GitTokenEnc != "", GitKey: p.GitKeyEnc != "", GitPush: p.GitPush, GitPR: p.GitPR,
			LastPush: p.LastPush, LastPR: p.LastPR, Tasks: counts[p.ID]})
		p.mu.Unlock()
		out[len(out)-1].Files = len(listRepo(p.repoDir()))
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (c *Controller) projectRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects", c.auth(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, c.projectViews()) }))
	mux.HandleFunc("POST /api/projects", c.auth(c.handleCreateProject))
	mux.HandleFunc("POST /api/projects/{id}", c.auth(c.handleUpdateProject))
	mux.HandleFunc("DELETE /api/projects/{id}", c.auth(c.handleDeleteProject))
	mux.HandleFunc("POST /api/projects/{id}/suggestions/{sid}/{act}", c.auth(c.handleProjectSuggestion))
	mux.HandleFunc("GET /api/projects/{id}/download", c.auth(c.handleDownloadProject))
	mux.HandleFunc("GET /api/projects/{id}/overview", c.auth(c.handleProjectOverview))
}

type projectReq struct {
	Name        string  `json:"name"`
	Planner     *string `json:"planner"`
	Review      *bool   `json:"review"`
	TestCommand *string `json:"test_command"`
	FixRounds   *int    `json:"fix_rounds"`
	TestMinutes *int    `json:"test_minutes"`
	NodeLabel   *string `json:"node_label"`
	Instr       *string `json:"instructions"`
	GitURL      string  `json:"git_url"`
	GitBranch   *string `json:"git_branch"`
	GitUser     *string `json:"git_user"`
	GitToken    *string `json:"git_token"`   // "" keeps the stored one; "-" removes it
	GitSSHKey   *string `json:"git_ssh_key"` // same
	GitPush     *bool   `json:"git_push"`
	GitPR       *bool   `json:"git_pr"`
}

// apply copies the request onto p (the git URL is only set at creation). Secrets are encrypted.
func (c *Controller) applyProjectReq(p *Project, req projectReq) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := strings.TrimSpace(req.Name); n != "" {
		p.Name = n
	}
	if req.Planner != nil {
		p.Planner = strings.TrimSpace(*req.Planner)
	}
	if req.Review != nil {
		v := *req.Review
		p.Review = &v
	}
	if req.TestCommand != nil {
		p.TestCommand = strings.TrimSpace(*req.TestCommand)
	}
	if req.FixRounds != nil {
		p.FixRounds = clampFixRounds(*req.FixRounds)
	}
	if req.TestMinutes != nil {
		p.TestMinutes = clampTestMinutes(*req.TestMinutes)
	}
	if req.NodeLabel != nil {
		p.NodeLabel = normLabel(*req.NodeLabel)
	}
	if req.Instr != nil {
		p.Instructions = clipInstructions(*req.Instr)
	}
	if req.GitBranch != nil {
		p.GitBranch = strings.TrimSpace(*req.GitBranch)
	}
	if req.GitUser != nil {
		p.GitUser = strings.TrimSpace(*req.GitUser)
	}
	if req.GitPush != nil {
		p.GitPush = *req.GitPush
	}
	if req.GitPR != nil {
		p.GitPR = *req.GitPR
	}
	for _, s := range []struct {
		in  *string
		out *string
	}{{req.GitToken, &p.GitTokenEnc}, {req.GitSSHKey, &p.GitKeyEnc}} {
		switch {
		case s.in == nil || *s.in == "":
		case *s.in == "-":
			*s.out = ""
		default:
			enc, err := c.encrypt(strings.TrimSpace(*s.in) + "\n")
			if err != nil {
				return err
			}
			*s.out = enc
		}
	}
	return nil
}

func (c *Controller) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req projectReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeJSON(w, 400, map[string]string{"error": "give the project a name"})
		return
	}
	p := &Project{Schema: projectSchema, ID: newID("proj"), Created: time.Now(), slot: make(chan struct{}, 1)}
	p.dir = filepath.Join(c.projectsDir(), p.ID)
	p.GitURL = strings.TrimSpace(req.GitURL)
	if err := c.applyProjectReq(p, req); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	fail := func(code int, err error) {
		os.RemoveAll(p.dir)
		writeJSON(w, code, map[string]string{"error": err.Error()})
	}
	if p.GitURL == "" {
		if p.GitPush || p.GitPR {
			fail(400, errors.New("pushing needs a git URL"))
			return
		}
		if err := os.MkdirAll(p.repoDir(), 0o755); err != nil {
			fail(500, err)
			return
		}
	} else if err := c.cloneProject(r.Context(), p); err != nil {
		fail(400, err)
		return
	}
	if err := p.save(); err != nil {
		fail(500, err)
		return
	}
	c.mu.Lock()
	c.projects[p.ID] = p
	c.mu.Unlock()
	writeJSON(w, 200, map[string]string{"id": p.ID})
}

func (c *Controller) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	p := c.getProject(r.PathValue("id"))
	if p == nil {
		writeJSON(w, 404, map[string]string{"error": "no such project"})
		return
	}
	var req projectReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	if err := c.applyProjectReq(p, req); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	p.mu.Lock()
	bad := (p.GitPush || p.GitPR) && p.GitURL == ""
	if p.GitBranch != "" && p.GitURL != "" && !validBranch(p.GitBranch) {
		bad = true
	}
	p.mu.Unlock()
	if bad {
		writeJSON(w, 400, map[string]string{"error": "check the git branch name; pushing needs a git URL"})
		return
	}
	if err := p.save(); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	p := c.getProject(r.PathValue("id"))
	if p == nil {
		writeJSON(w, 404, map[string]string{"error": "no such project"})
		return
	}
	c.mu.Lock()
	for _, t := range c.tasks {
		if t.ProjectID == p.ID {
			c.mu.Unlock()
			writeJSON(w, 409, map[string]string{"error": "this project still has tasks; delete them first"})
			return
		}
	}
	delete(c.projects, p.ID)
	c.mu.Unlock()
	pc := p.chatLog()
	pc.mu.Lock()
	if pc.cancel != nil {
		pc.cancel()
	}
	pc.mu.Unlock()
	projectChats.Delete(pc.path)
	os.RemoveAll(p.dir)
	c.spendDeleted("projchat_" + p.ID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleDownloadProject(w http.ResponseWriter, r *http.Request) {
	p := c.getProject(r.PathValue("id"))
	if p == nil {
		writeJSON(w, 404, map[string]string{"error": "no such project"})
		return
	}
	zp := filepath.Join(p.dir, "download.zip")
	if err := zipDir(p.repoDir(), zp); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer os.Remove(zp)
	name := slug(p.Name)
	if name == "" {
		name = p.ID
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.zip"`)
	http.ServeFile(w, r, zp)
}
