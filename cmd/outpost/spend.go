package main

// Cloud spending: a small ledger of what Claude and OpenRouter cost, per day and per
// task, kept in spend.json on the controller so deleting a task does not erase what it
// cost. The Spending page (web/spending.js) reads it, and the optional monthly cap
// (Settings.MonthlyCapUSD) pauses cloud planner calls once the month's spend would pass
// it; tasks on local planners are not affected.
//
// The ledger starts the first time a controller with this code runs: it is seeded from
// the tasks on disk, each task's whole cost counted on the day it was created (earlier
// per-call times were never recorded). From then on every cloud call is added as it
// happens. Times are in the controller's local time zone.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// spendKeepMonths is how many months the ledger keeps.
const spendKeepMonths = 24

type spendDay struct {
	Claude     float64 `json:"claude,omitempty"`
	OpenRouter float64 `json:"openrouter,omitempty"`
	Calls      int     `json:"calls,omitempty"`
}

type spendTask struct {
	Title   string  `json:"title"`
	Planner string  `json:"planner"` // label, e.g. "Claude Sonnet 5.5"
	Calls   int     `json:"calls"`
	In      int     `json:"in"`
	Out     int     `json:"out"`
	USD     float64 `json:"usd"`
	Deleted bool    `json:"deleted,omitempty"`
}

type spendMonth struct {
	Days  map[string]*spendDay  `json:"days"`  // "01".."31"
	Tasks map[string]*spendTask `json:"tasks"` // by task id
}

type spendLedger struct {
	mu     sync.Mutex
	path   string
	Months map[string]*spendMonth `json:"months"` // "2026-11"
}

func monthKey(t time.Time) string { return t.Format("2006-01") }

func (l *spendLedger) monthLocked(key string) *spendMonth {
	if l.Months == nil {
		l.Months = map[string]*spendMonth{}
	}
	m := l.Months[key]
	if m == nil {
		m = &spendMonth{Days: map[string]*spendDay{}, Tasks: map[string]*spendTask{}}
		l.Months[key] = m
	}
	if m.Days == nil {
		m.Days = map[string]*spendDay{}
	}
	if m.Tasks == nil {
		m.Tasks = map[string]*spendTask{}
	}
	return m
}

// addLocked records one call (or, when seeding, a whole task) at time at.
func (l *spendLedger) addLocked(at time.Time, provider, taskID, title, planner string, calls, in, out int, usd float64) {
	m := l.monthLocked(monthKey(at))
	dk := at.Format("02")
	d := m.Days[dk]
	if d == nil {
		d = &spendDay{}
		m.Days[dk] = d
	}
	if provider == "openrouter" {
		d.OpenRouter += usd
	} else {
		d.Claude += usd
	}
	d.Calls += calls
	if taskID == "" {
		return
	}
	ts := m.Tasks[taskID]
	if ts == nil {
		ts = &spendTask{}
		m.Tasks[taskID] = ts
	}
	ts.Title, ts.Planner = title, planner
	ts.Calls += calls
	ts.In += in
	ts.Out += out
	ts.USD += usd
}

func (l *spendLedger) pruneLocked(now time.Time) {
	oldest := monthKey(time.Date(now.Year(), now.Month()-spendKeepMonths+1, 1, 0, 0, 0, 0, now.Location()))
	for k := range l.Months {
		if k < oldest {
			delete(l.Months, k)
		}
	}
}

func (l *spendLedger) saveLocked() error {
	if l.path == "" {
		return nil
	}
	b, _ := json.Marshal(l)
	return writeFileAtomicBackup(l.path, b, 0o600)
}

// monthTotal is what the cloud planners cost in the month of now.
func (l *spendLedger) monthTotal(now time.Time) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	m := l.Months[monthKey(now)]
	if m == nil {
		return 0
	}
	sum := 0.0
	for _, d := range m.Days {
		sum += d.Claude + d.OpenRouter
	}
	return sum
}

// spendLedger returns the controller's ledger, loading (or seeding) it on first use.
func (c *Controller) spendLedger() *spendLedger {
	c.spendOnce.Do(func() {
		l := &spendLedger{}
		if c.dataDir != "" {
			l.path = filepath.Join(c.dataDir, "spend.json")
		}
		if l.path != "" {
			usedBak, err := readWithBackup(l.path, func(b []byte) error {
				l.Months = nil
				return json.Unmarshal(b, l)
			})
			switch {
			case err == nil && usedBak:
				c.noteRecovered("spend.json was damaged; loaded the previous copy")
			case err != nil && errors.Is(err, os.ErrNotExist):
				c.seedSpend(l)
			case err != nil:
				l.Months = nil
				c.noteRecovered("spend.json could not be read (" + err.Error() + "); the Spending page starts again from the tasks on disk")
				c.seedSpend(l)
			}
		}
		c.spend = l
	})
	return c.spend
}

// seedSpend fills a new ledger from the tasks on disk.
func (c *Controller) seedSpend(l *spendLedger) {
	c.mu.Lock()
	tasks := make([]*Task, 0, len(c.tasks))
	for _, t := range c.tasks {
		tasks = append(tasks, t)
	}
	c.mu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, t := range tasks {
		t.mu.Lock()
		id, title, planner, model, orModel, created := t.ID, t.Title, t.Planner, t.ClaudeModel, t.ORModel, t.Created
		calls, in, out, usd := t.ClaudeCalls, t.ClaudeIn+t.ClaudeCacheR+t.ClaudeCacheW, t.ClaudeOut, t.CostUSD
		if title == "" {
			title = firstLine(t.Prompt, 80)
		}
		t.mu.Unlock()
		if usd <= 0 {
			continue
		}
		provider := "claude" // a task that switched planners is counted under the one it has now
		if planner == "openrouter" {
			provider = "openrouter"
		}
		l.addLocked(created, provider, id, title, c.spendPlannerLabel(provider, model, orModel), calls, in, out, usd)
	}
	l.pruneLocked(time.Now())
	_ = l.saveLocked()
}

func (c *Controller) spendPlannerLabel(provider, model, orModel string) string {
	if provider == "openrouter" {
		return "OpenRouter · " + openRouterLabel(orModel)
	}
	if p, ok := claudePrices[model]; ok {
		return p.Label
	}
	return "Claude"
}

// recordSpend adds one cloud call to the ledger.
func (c *Controller) recordSpend(t *Task, provider string, resp *llmResponse) {
	if resp == nil {
		return
	}
	t.mu.Lock()
	id, title, model, orModel := t.ID, t.Title, t.ClaudeModel, t.ORModel
	if title == "" {
		title = firstLine(t.Prompt, 80)
	}
	t.mu.Unlock()
	if err := c.recordSpendFor(provider, id, title, c.spendPlannerLabel(provider, model, orModel), resp); err != nil {
		t.logf("warn", "Could not save the spending ledger: %v", err)
	}
}

// recordSpendFor adds one cloud call under any id: a task, or a project
// conversation ("projchat_<project id>").
func (c *Controller) recordSpendFor(provider, id, title, label string, resp *llmResponse) error {
	if resp == nil {
		return nil
	}
	l := c.spendLedger()
	now := time.Now()
	l.mu.Lock()
	l.addLocked(now, provider, id, title, label, 1, resp.InTokens+resp.CacheRead+resp.CacheWrite, resp.OutTokens, resp.CostUSD)
	l.pruneLocked(now)
	err := l.saveLocked()
	l.mu.Unlock()
	return err
}

// spendRename and spendDeleted keep the ledger's task titles in step with the sidebar.
func (c *Controller) spendRename(id, title string) { c.spendMark(id, title, false) }
func (c *Controller) spendDeleted(id string)       { c.spendMark(id, "", true) }

func (c *Controller) spendMark(id, title string, deleted bool) {
	l := c.spendLedger()
	l.mu.Lock()
	defer l.mu.Unlock()
	changed := false
	for _, m := range l.Months {
		if ts := m.Tasks[id]; ts != nil {
			if title != "" {
				ts.Title = title
			}
			if deleted {
				ts.Deleted = true
			}
			changed = true
		}
	}
	if changed {
		_ = l.saveLocked()
	}
}

// monthlyCap returns the monthly cap (0 = off) and what this month has cost so far.
func (c *Controller) monthlyCap() (limit, spent float64) {
	c.mu.Lock()
	limit = c.settings.MonthlyCapUSD
	c.mu.Unlock()
	if limit <= 0 {
		return 0, 0
	}
	return limit, c.spendLedger().monthTotal(time.Now())
}

// untilNextMonth is how long until the first moment of next month.
func untilNextMonth(now time.Time) time.Duration {
	next := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location())
	return next.Sub(now)
}

// wakeMonthlyPaused nudges tasks paused by the monthly cap so they check it again
// (after the cap is raised or turned off).
func (c *Controller) wakeMonthlyPaused() {
	c.mu.Lock()
	tasks := make([]*Task, 0, len(c.tasks))
	for _, t := range c.tasks {
		tasks = append(tasks, t)
	}
	c.mu.Unlock()
	for _, t := range tasks {
		t.mu.Lock()
		ch, hit := t.resume, t.Status == "paused_cap" && t.CapKind == "monthly"
		t.mu.Unlock()
		if hit && ch != nil {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

// ---------- page ----------

type spendDayView struct {
	Day        int     `json:"day"`
	Claude     float64 `json:"claude"`
	OpenRouter float64 `json:"openrouter"`
}

type spendTaskView struct {
	ID string `json:"id"`
	spendTask
}

type spendView struct {
	Month      string          `json:"month"`  // "2026-11"
	Months     []string        `json:"months"` // newest first, always includes the current month
	Total      float64         `json:"total"`
	Claude     float64         `json:"claude"`
	OpenRouter float64         `json:"openrouter"`
	Calls      int             `json:"calls"`
	Tasks      int             `json:"tasks"` // tasks with any cloud cost this month
	Days       []spendDayView  `json:"days"`  // one per day of the month
	Top        []spendTaskView `json:"top"`   // most expensive first, at most 20
	CapUSD     float64         `json:"cap_usd"`
	Paused     int             `json:"paused"` // tasks waiting at the monthly cap
	Current    bool            `json:"current"`
}

func (c *Controller) spendViewFor(month string, now time.Time) spendView {
	cur := monthKey(now)
	if month == "" {
		month = cur
	}
	v := spendView{Month: month, Current: month == cur, Days: []spendDayView{}, Top: []spendTaskView{}}
	c.mu.Lock()
	v.CapUSD = c.settings.MonthlyCapUSD
	tasks := make([]*Task, 0, len(c.tasks))
	for _, t := range c.tasks {
		tasks = append(tasks, t)
	}
	c.mu.Unlock()
	for _, t := range tasks {
		t.mu.Lock()
		if t.Status == "paused_cap" && t.CapKind == "monthly" {
			v.Paused++
		}
		t.mu.Unlock()
	}
	first, err := time.ParseInLocation("2006-01", month, now.Location())
	if err != nil {
		first, v.Month, v.Current = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()), cur, true
	}
	ndays := first.AddDate(0, 1, -1).Day()
	l := c.spendLedger()
	l.mu.Lock()
	defer l.mu.Unlock()
	seen := map[string]bool{cur: true}
	v.Months = append(v.Months, cur)
	keys := make([]string, 0, len(l.Months))
	for k := range l.Months {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			v.Months = append(v.Months, k)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(v.Months)))
	m := l.Months[v.Month]
	for d := 1; d <= ndays; d++ {
		dv := spendDayView{Day: d}
		if m != nil {
			if x := m.Days[fmt.Sprintf("%02d", d)]; x != nil {
				dv.Claude, dv.OpenRouter = x.Claude, x.OpenRouter
				v.Calls += x.Calls
			}
		}
		v.Claude += dv.Claude
		v.OpenRouter += dv.OpenRouter
		v.Days = append(v.Days, dv)
	}
	v.Total = v.Claude + v.OpenRouter
	if m != nil {
		for id, ts := range m.Tasks {
			if ts.USD <= 0 {
				continue
			}
			v.Tasks++
			v.Top = append(v.Top, spendTaskView{ID: id, spendTask: *ts})
		}
	}
	sort.Slice(v.Top, func(i, j int) bool {
		if v.Top[i].USD != v.Top[j].USD {
			return v.Top[i].USD > v.Top[j].USD
		}
		return v.Top[i].ID < v.Top[j].ID
	})
	if len(v.Top) > 20 {
		v.Top = v.Top[:20]
	}
	return v
}

func (c *Controller) handleSpending(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, c.spendViewFor(r.URL.Query().Get("month"), time.Now()))
}

// handleSpendingCap sets the monthly cap; 0 turns it off.
func (c *Controller) handleSpendingCap(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CapUSD *float64 `json:"cap_usd"`
	}
	if err := readJSONBody(w, r, &req); err != nil || req.CapUSD == nil || *req.CapUSD < 0 || *req.CapUSD > 100000 {
		writeJSON(w, 400, map[string]string{"error": "give a monthly cap in dollars, or 0 to turn it off"})
		return
	}
	c.mu.Lock()
	c.settings.MonthlyCapUSD = *req.CapUSD
	err := c.saveSettingsLocked()
	c.mu.Unlock()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	c.wakeMonthlyPaused()
	writeJSON(w, 200, c.spendViewFor("", time.Now()))
}
