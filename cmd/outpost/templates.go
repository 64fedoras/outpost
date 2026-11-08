package main

// Saved tasks (idea 31) and schedules (idea 32). A saved task is a stored
// new-task request (prompt and options, without images or attached files)
// that starts again with one click. It can carry a schedule (daily or weekly
// at a time), and the controller then starts it on its own: the planner asks
// no questions and the plan is approved without you (Task.Unattended), and
// the usual Finished / Failed notifications report the result.
//
// Safety: a scheduled task must plan on a node, never on a paid cloud planner,
// so a schedule never spends money. A run is skipped while the previous run
// of the same saved task is still going, and runs missed while the controller
// was off are not made up (the next one is at the next scheduled time).

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxTemplates = 100

type Schedule struct {
	Every   string `json:"every"`             // daily | weekly
	At      string `json:"at"`                // "02:30", controller local time
	Weekday int    `json:"weekday,omitempty"` // weekly: 0 = Sunday … 6 = Saturday
}

type Template struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Request  createReq  `json:"request"`
	Created  time.Time  `json:"created"`
	Schedule *Schedule  `json:"schedule,omitempty"`
	NextRun  *time.Time `json:"next_run,omitempty"`
	LastRun  *time.Time `json:"last_run,omitempty"`
	LastTask string     `json:"last_task,omitempty"`
	LastNote string     `json:"last_note,omitempty"` // why the last scheduled run did not start
}

func (s *Schedule) check() error {
	switch s.Every {
	case "daily", "weekly":
	default:
		return errors.New("a schedule is daily or weekly")
	}
	m, err := parseHM(s.At)
	if err != nil {
		return err
	}
	s.At = fmt.Sprintf("%02d:%02d", m/60, m%60)
	if s.Weekday < 0 || s.Weekday > 6 {
		return errors.New("weekday is 0 (Sunday) to 6 (Saturday)")
	}
	if s.Every == "daily" {
		s.Weekday = 0
	}
	return nil
}

// next is the first scheduled time strictly after t.
func (s *Schedule) next(t time.Time) time.Time {
	m, _ := parseHM(s.At)
	d := time.Date(t.Year(), t.Month(), t.Day(), m/60, m%60, 0, 0, t.Location())
	for i := 0; i < 8; i++ {
		if d.After(t) && (s.Every == "daily" || int(d.Weekday()) == s.Weekday) {
			return d
		}
		d = time.Date(d.Year(), d.Month(), d.Day()+1, m/60, m%60, 0, 0, t.Location())
	}
	return d
}

func (c *Controller) templatesPath() string { return filepath.Join(c.dataDir, "templates.json") }

func (c *Controller) loadTemplates() {
	b, err := os.ReadFile(c.templatesPath())
	if err != nil {
		return
	}
	var list []*Template
	if err := json.Unmarshal(b, &list); err != nil {
		log.Printf("templates.json unreadable: %v", err)
		return
	}
	now := time.Now()
	for _, t := range list {
		if t.Schedule != nil && t.Schedule.check() == nil {
			n := t.Schedule.next(now) // missed runs are not made up
			t.NextRun = &n
		} else {
			t.Schedule, t.NextRun = nil, nil
		}
	}
	c.mu.Lock()
	c.templates = list
	c.mu.Unlock()
}

func (c *Controller) saveTemplatesLocked() {
	b, _ := json.MarshalIndent(c.templates, "", "  ")
	if err := writeFileAtomic(c.templatesPath(), b, 0o600); err != nil {
		log.Printf("saving templates: %v", err)
	}
}

func (c *Controller) templateLocked(id string) *Template {
	for _, t := range c.templates {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// templateView adds the last run's status for the UI.
type templateView struct {
	*Template
	LastStatus string `json:"last_status,omitempty"`
}

func (c *Controller) templateViewsLocked() []templateView {
	out := make([]templateView, 0, len(c.templates))
	for _, t := range c.templates {
		v := templateView{Template: t}
		if tk := c.tasks[t.LastTask]; tk != nil {
			tk.mu.Lock()
			v.LastStatus = tk.Status
			tk.mu.Unlock()
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// scheduleAllowed refuses a schedule whose planner would cost money.
func (c *Controller) scheduleAllowed(req createReq) error {
	p := req.Planner
	if p == "" {
		if proj := c.getProject(req.Project); proj != nil {
			p = proj.applyDefaults(req).Planner
		}
	}
	c.mu.Lock()
	if p == "" {
		p = c.settings.DefaultPlanner
	}
	c.mu.Unlock()
	if isCloudPlanner(p) {
		return errors.New("a schedule runs without you, so it must plan on a node: pick a node (or Auto) as the planner, not Claude or OpenRouter")
	}
	return nil
}

func (c *Controller) templateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/templates", c.auth(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		writeJSON(w, 200, c.templateViewsLocked())
	}))
	mux.HandleFunc("POST /api/templates", c.auth(c.handleNewTemplate))
	mux.HandleFunc("POST /api/templates/{id}", c.auth(c.handleEditTemplate))
	mux.HandleFunc("DELETE /api/templates/{id}", c.auth(c.handleDeleteTemplate))
	mux.HandleFunc("POST /api/templates/{id}/run", c.auth(c.handleRunTemplate))
}

func (c *Controller) handleNewTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string    `json:"name"`
		Request  createReq `json:"request"`
		Schedule *Schedule `json:"schedule"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad json"})
		return
	}
	req.Request.Prompt = strings.TrimSpace(req.Request.Prompt)
	req.Request.Images = nil
	req.Request.NodeLabel = normLabel(req.Request.NodeLabel)
	if req.Request.Prompt == "" {
		writeJSON(w, 400, map[string]string{"error": "a saved task needs a request"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = firstLine(req.Request.Prompt, 60)
	}
	t := &Template{ID: newID("tpl"), Name: name, Request: req.Request, Created: time.Now()}
	if err := c.setSchedule(t, req.Schedule); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	c.mu.Lock()
	if len(c.templates) >= maxTemplates {
		c.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": fmt.Sprintf("at most %d saved tasks; delete one first", maxTemplates)})
		return
	}
	c.templates = append(c.templates, t)
	c.saveTemplatesLocked()
	c.mu.Unlock()
	writeJSON(w, 200, t)
}

// setSchedule sets or clears (nil) a template's schedule.
func (c *Controller) setSchedule(t *Template, s *Schedule) error {
	if s == nil || s.Every == "" {
		t.Schedule, t.NextRun = nil, nil
		return nil
	}
	if err := s.check(); err != nil {
		return err
	}
	if err := c.scheduleAllowed(t.Request); err != nil {
		return err
	}
	n := s.next(time.Now())
	t.Schedule, t.NextRun = s, &n
	return nil
}

func (c *Controller) handleEditTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     *string   `json:"name"`
		Schedule *Schedule `json:"schedule"` // {"every": ""} clears it
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad json"})
		return
	}
	c.mu.Lock()
	t := c.templateLocked(r.PathValue("id"))
	if t == nil {
		c.mu.Unlock()
		writeJSON(w, 404, map[string]string{"error": "no such saved task"})
		return
	}
	cp := *t
	c.mu.Unlock()
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		cp.Name = strings.TrimSpace(*req.Name)
	}
	if req.Schedule != nil {
		if err := c.setSchedule(&cp, req.Schedule); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
	}
	c.mu.Lock()
	if t := c.templateLocked(cp.ID); t != nil {
		*t = cp
		c.saveTemplatesLocked()
	}
	c.mu.Unlock()
	writeJSON(w, 200, cp)
}

func (c *Controller) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, t := range c.templates {
		if t.ID == id {
			c.templates = append(c.templates[:i], c.templates[i+1:]...)
			c.saveTemplatesLocked()
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
	}
	writeJSON(w, 404, map[string]string{"error": "no such saved task"})
}

// handleRunTemplate starts a saved task now, as if typed in (questions and
// plan approval as usual).
func (c *Controller) handleRunTemplate(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	t := c.templateLocked(r.PathValue("id"))
	var req createReq
	if t != nil {
		req = t.Request
	}
	c.mu.Unlock()
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such saved task"})
		return
	}
	tk, code, err := c.createTask(req, nil, createOpts{Template: t.ID})
	if err != nil {
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	c.mu.Lock()
	now := time.Now()
	t.LastRun, t.LastTask, t.LastNote = &now, tk.ID, ""
	c.saveTemplatesLocked()
	c.mu.Unlock()
	writeJSON(w, 200, map[string]string{"id": tk.ID})
}

// runDue starts every scheduled template whose time has come.
func (c *Controller) runDue(now time.Time) {
	type due struct {
		id   string
		req  createReq
		last string
	}
	var list []due
	c.mu.Lock()
	for _, t := range c.templates {
		if t.Schedule == nil || t.NextRun == nil || t.NextRun.After(now) {
			continue
		}
		n := t.Schedule.next(now)
		t.NextRun = &n
		list = append(list, due{t.ID, t.Request, t.LastTask})
	}
	if len(list) > 0 {
		c.saveTemplatesLocked()
	}
	c.mu.Unlock()
	for _, d := range list {
		note := ""
		var taskID string
		if prev := c.getTask(d.last); prev != nil && taskActive(prev) {
			note = "Skipped: the previous run was still going."
		} else if tk, _, err := c.createTask(d.req, nil, createOpts{Unattended: true, Template: d.id}); err != nil {
			note = "Did not start: " + err.Error()
		} else {
			taskID = tk.ID
		}
		c.mu.Lock()
		if t := c.templateLocked(d.id); t != nil {
			t.LastNote = note
			if taskID != "" {
				t.LastRun, t.LastTask = &now, taskID
			}
			c.saveTemplatesLocked()
		}
		c.mu.Unlock()
		if note != "" {
			log.Printf("saved task %s: %s", d.id, note)
			go c.deliverNotification("failed", "Scheduled task", note, "")
		}
	}
}

// taskActive: the task has not reached a resting state.
func taskActive(t *Task) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch t.Status {
	case "done", "failed", "cancelled", "interrupted", "awaiting_answers", "awaiting_approval", "paused_cap":
		return false
	}
	return true
}

func (c *Controller) templateLoop() {
	for {
		now := time.Now()
		time.Sleep(time.Until(now.Truncate(time.Minute).Add(time.Minute + time.Second)))
		if !c.safeMode {
			c.runDue(time.Now())
		}
	}
}
