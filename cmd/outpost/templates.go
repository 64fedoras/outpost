package main

// Saved tasks (idea 31) and schedules (idea 32). A saved task is a stored
// new-task request (prompt and options, without images or attached files)
// that starts again with one click. It can carry a schedule (every few
// hours, daily, on chosen days, weekly or monthly), and the controller then
// starts it on its own: the planner asks no questions and the plan is
// approved without you (Task.Unattended), and the usual Finished / Failed
// notifications report the result.
//
// Safety: a scheduled task must plan on a node, never on a paid cloud planner,
// so a schedule never spends money. A run is skipped while the previous run
// of the same saved task is still going. A run missed while the controller
// was off starts when it is back if it is less than an hour late
// (catchUpWindow); older ones are skipped and noted on the saved task.

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

// maxTplRuns is how many runs each saved task remembers for the Scheduled page.
const maxTplRuns = 10

// TplRun is one entry of a saved task's run history: a task it started, or a
// scheduled time that did not start one (skipped, missed, refused).
type TplRun struct {
	At   time.Time `json:"at"`
	Task string    `json:"task,omitempty"`
	Kind string    `json:"kind"`           // scheduled | manual | late | skipped | missed | not_started
	Note string    `json:"note,omitempty"` // why it was skipped, missed, late or not started
}

// addRunLocked puts a run at the front of t.Runs and keeps the newest few.
func (t *Template) addRunLocked(r TplRun) {
	t.Runs = append([]TplRun{r}, t.Runs...)
	if len(t.Runs) > maxTplRuns {
		t.Runs = t.Runs[:maxTplRuns]
	}
}

type Schedule struct {
	Every   string `json:"every"`             // hours | daily | days | weekly | monthly
	At      string `json:"at"`                // "02:30", controller local time; for hours, the first run of the day
	Weekday int    `json:"weekday,omitempty"` // weekly: 0 = Sunday … 6 = Saturday
	Hours   int    `json:"hours,omitempty"`   // hours: run every this many hours (1, 2, 3, 4, 6, 8 or 12)
	Days    []int  `json:"days,omitempty"`    // days: the weekdays it runs on, 0 = Sunday … 6 = Saturday
	Day     int    `json:"day,omitempty"`     // monthly: day of the month, 1 to 31 (the last day in shorter months)
}

// catchUpWindow: a run missed by less than this while the controller was off
// (a restart, an update) still starts when it is back. Older ones are skipped.
const catchUpWindow = time.Hour

type Template struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Request  createReq  `json:"request"`
	Created  time.Time  `json:"created"`
	Schedule *Schedule  `json:"schedule,omitempty"`
	NextRun  *time.Time `json:"next_run,omitempty"`
	LastRun  *time.Time `json:"last_run,omitempty"`
	LastTask string     `json:"last_task,omitempty"`
	LastNote string     `json:"last_note,omitempty"` // why the last scheduled run did not start, or started late
	Paused   *time.Time `json:"paused,omitempty"`    // the schedule is kept but does not run; when it was paused
	Runs     []TplRun   `json:"runs,omitempty"`      // the last maxTplRuns runs, newest first

	lateFrom time.Time // memory only: a run missed while the controller was off, caught up by runDue
}

func (s *Schedule) check() error {
	m, err := parseHM(s.At)
	if err != nil {
		return err
	}
	keep := *s
	*s = Schedule{Every: keep.Every, At: fmt.Sprintf("%02d:%02d", m/60, m%60)}
	switch keep.Every {
	case "daily":
	case "weekly":
		if keep.Weekday < 0 || keep.Weekday > 6 {
			return errors.New("weekday is 0 (Sunday) to 6 (Saturday)")
		}
		s.Weekday = keep.Weekday
	case "hours":
		if keep.Hours < 1 || keep.Hours > 12 || 24%keep.Hours != 0 {
			return errors.New("every 1, 2, 3, 4, 6, 8 or 12 hours")
		}
		s.Hours = keep.Hours
	case "days":
		var seen [7]bool
		for _, d := range keep.Days {
			if d < 0 || d > 6 {
				return errors.New("days are 0 (Sunday) to 6 (Saturday)")
			}
			seen[d] = true
		}
		for d, ok := range seen {
			if ok {
				s.Days = append(s.Days, d)
			}
		}
		if len(s.Days) == 0 {
			return errors.New("pick at least one day")
		}
	case "monthly":
		if keep.Day < 1 || keep.Day > 31 {
			return errors.New("day of the month is 1 to 31")
		}
		s.Day = keep.Day
	default:
		return errors.New("a schedule is every few hours, daily, on chosen days, weekly or monthly")
	}
	return nil
}

func (s *Schedule) onDay(d time.Weekday) bool {
	switch s.Every {
	case "weekly":
		return int(d) == s.Weekday
	case "days":
		for _, x := range s.Days {
			if x == int(d) {
				return true
			}
		}
		return false
	}
	return true
}

// next is the first scheduled time strictly after t (wall clock, so a run
// stays at 02:30 across daylight saving changes).
func (s *Schedule) next(t time.Time) time.Time {
	m, _ := parseHM(s.At)
	loc := t.Location()
	if s.Every == "monthly" {
		for i := 0; i < 14; i++ {
			first := time.Date(t.Year(), t.Month()+time.Month(i), 1, 0, 0, 0, 0, loc)
			day := min(s.Day, first.AddDate(0, 1, -1).Day())
			if d := time.Date(first.Year(), first.Month(), day, m/60, m%60, 0, 0, loc); d.After(t) {
				return d
			}
		}
	}
	step := 24 * 60
	if s.Every == "hours" && s.Hours > 0 {
		step = s.Hours * 60
	}
	for i := 0; i < 9; i++ {
		day := time.Date(t.Year(), t.Month(), t.Day()+i, 0, 0, 0, 0, loc)
		if !s.onDay(day.Weekday()) {
			continue
		}
		for mm := m % step; mm < 24*60; mm += step {
			if d := time.Date(day.Year(), day.Month(), day.Day(), mm/60, mm%60, 0, 0, loc); d.After(t) {
				return d
			}
		}
	}
	return t.Add(24 * time.Hour) // not reached for a checked schedule
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
		if t.Schedule == nil || t.Schedule.check() != nil {
			t.Schedule, t.NextRun = nil, nil
			continue
		}
		if t.NextRun != nil && !t.NextRun.After(now) {
			if now.Sub(*t.NextRun) <= catchUpWindow {
				t.lateFrom = *t.NextRun // runDue starts it at once and says it is late
				continue
			}
			t.LastNote = fmt.Sprintf("Missed the run at %s: the controller was off.", t.NextRun.Format("Mon 15:04"))
			t.addRunLocked(TplRun{At: *t.NextRun, Kind: "missed", Note: "The controller was off."})
		}
		n := t.Schedule.next(now)
		t.NextRun = &n
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

// templateView adds the last run's status, and each run's task status and
// running time, for the UI.
type templateView struct {
	*Template
	LastStatus string       `json:"last_status,omitempty"`
	Runs       []tplRunView `json:"runs,omitempty"`
}

type tplRunView struct {
	TplRun
	Status  string `json:"status,omitempty"`  // the task's status, or "" when it no longer exists
	Seconds int    `json:"seconds,omitempty"` // from start to end, or to now while it works
}

func (c *Controller) templateViewsLocked() []templateView {
	out := make([]templateView, 0, len(c.templates))
	now := time.Now()
	for _, t := range c.templates {
		v := templateView{Template: t}
		if tk := c.tasks[t.LastTask]; tk != nil {
			tk.mu.Lock()
			v.LastStatus = tk.Status
			tk.mu.Unlock()
		}
		for _, r := range t.Runs {
			rv := tplRunView{TplRun: r}
			if tk := c.tasks[r.Task]; tk != nil && r.Task != "" {
				tk.mu.Lock()
				rv.Status = tk.Status
				end := now
				if tk.Ended != nil {
					end = *tk.Ended
				}
				rv.Seconds = int(end.Sub(tk.Created).Seconds())
				tk.mu.Unlock()
			}
			v.Runs = append(v.Runs, rv)
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// scheduleAllowed refuses a schedule whose planner would cost money.
func (c *Controller) scheduleAllowed(req createReq) error {
	p := req.Planner
	explicit := p != ""
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
	if p == "" || (!explicit && p == "claude" && c.anthropicKey() == "") {
		p = "auto" // the stock default without a key: a schedule plans on a node
	}
	if isCloudPlanner(p) || p == "auto_api" {
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
	mux.HandleFunc("POST /api/schedules/pause", c.auth(c.handlePauseAll))
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
		Name     *string    `json:"name"`
		Schedule *Schedule  `json:"schedule"` // {"every": ""} clears it
		Request  *createReq `json:"request"`  // replaces the saved request
		Paused   *bool      `json:"paused"`   // pause or resume the schedule
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
	if req.Request != nil {
		nr := *req.Request
		nr.Prompt = strings.TrimSpace(nr.Prompt)
		nr.Images = nil
		nr.NodeLabel = normLabel(nr.NodeLabel)
		if nr.Prompt == "" {
			writeJSON(w, 400, map[string]string{"error": "a saved task needs a request"})
			return
		}
		cp.Request = nr
		if cp.Schedule != nil && req.Schedule == nil {
			if err := c.scheduleAllowed(cp.Request); err != nil {
				writeJSON(w, 400, map[string]string{"error": err.Error()})
				return
			}
		}
	}
	if req.Schedule != nil {
		if err := c.setSchedule(&cp, req.Schedule); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
	}
	if req.Paused != nil {
		switch {
		case *req.Paused && cp.Paused == nil:
			now := time.Now()
			cp.Paused = &now
		case !*req.Paused && cp.Paused != nil:
			cp.Paused = nil
			if cp.Schedule != nil { // resume from now: what was paused is not made up
				n := cp.Schedule.next(time.Now())
				cp.NextRun = &n
			}
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
	if t := c.templateLocked(t.ID); t != nil { // it may have been deleted meanwhile
		t.LastRun, t.LastTask, t.LastNote = &now, tk.ID, ""
		t.addRunLocked(TplRun{At: now, Task: tk.ID, Kind: "manual"})
		c.saveTemplatesLocked()
	}
	c.mu.Unlock()
	writeJSON(w, 200, map[string]string{"id": tk.ID})
}

// runDue starts every scheduled template whose time has come.
func (c *Controller) runDue(now time.Time) {
	type due struct {
		id   string
		req  createReq
		last string
		late time.Time
	}
	var list []due
	c.mu.Lock()
	if c.settings.SchedulesPaused {
		c.mu.Unlock()
		return
	}
	for _, t := range c.templates {
		if t.Schedule == nil || t.NextRun == nil || t.NextRun.After(now) || t.Paused != nil {
			continue
		}
		n := t.Schedule.next(now)
		t.NextRun = &n
		list = append(list, due{t.ID, t.Request, t.LastTask, t.lateFrom})
		t.lateFrom = time.Time{}
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
		if taskID != "" && !d.late.IsZero() {
			note = fmt.Sprintf("Started late: the controller was off at %s.", d.late.Format("15:04"))
		}
		run := TplRun{At: now, Task: taskID, Kind: "scheduled"}
		switch {
		case taskID != "" && !d.late.IsZero():
			run.Kind, run.Note = "late", note
		case strings.HasPrefix(note, "Skipped"):
			run.Kind, run.Note = "skipped", "The previous run was still going."
		case note != "":
			run.Kind, run.Note = "not_started", strings.TrimPrefix(note, "Did not start: ")
		}
		c.mu.Lock()
		if t := c.templateLocked(d.id); t != nil {
			t.LastNote = note
			if taskID != "" {
				t.LastRun, t.LastTask = &now, taskID
			}
			t.addRunLocked(run)
			c.saveTemplatesLocked()
		}
		c.mu.Unlock()
		if note != "" && taskID == "" {
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

// handlePauseAll stops (or starts again) every schedule at once without
// changing them. Run now still works. On resume each next run is counted from
// now, so what was paused is not made up.
func (c *Controller) handlePauseAll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paused bool `json:"paused"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad json"})
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	was := c.settings.SchedulesPaused
	c.settings.SchedulesPaused = req.Paused
	if was && !req.Paused {
		now := time.Now()
		for _, t := range c.templates {
			if t.Schedule != nil {
				n := t.Schedule.next(now)
				t.NextRun, t.lateFrom = &n, time.Time{}
			}
		}
		c.saveTemplatesLocked()
	}
	if err := c.saveSettingsLocked(); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]bool{"paused": req.Paused})
}

// utcOffsetMinutes is the controller's offset from UTC now, so the web UI can
// show schedule times by the controller's clock whatever the browser's zone.
func utcOffsetMinutes() int {
	_, off := time.Now().Zone()
	return off / 60
}
