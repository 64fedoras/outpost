package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func at(day, h, m int) time.Time { return time.Date(2026, 11, day, h, m, 0, 0, time.Local) } // Nov 1 2026 is a Sunday

func TestWorkHours(t *testing.T) {
	var none *WorkHours
	if !none.open(at(2, 3, 0)) {
		t.Fatal("no hours = always open")
	}
	night := &WorkHours{From: "22:00", To: "7:00", Days: []int{5}} // Friday night into Saturday
	if err := night.check(); err != nil || night.To != "07:00" {
		t.Fatal(err, night.To)
	}
	cases := []struct {
		t    time.Time
		want bool
	}{
		{at(6, 21, 59), false}, // Friday before
		{at(6, 22, 0), true},
		{at(7, 6, 59), true}, // Saturday morning belongs to Friday's window
		{at(7, 7, 0), false},
		{at(7, 23, 0), false}, // Saturday night: not a listed day
		{at(5, 23, 0), false}, // Thursday
	}
	for _, c := range cases {
		if got := night.open(c.t); got != c.want {
			t.Errorf("%v: got %v", c.t, got)
		}
	}
	day := &WorkHours{From: "09:00", To: "17:30"}
	if !day.open(at(3, 12, 0)) || day.open(at(3, 17, 30)) || day.open(at(3, 8, 0)) {
		t.Fatal("daytime window")
	}
	for _, bad := range []WorkHours{{From: "25:00", To: "07:00"}, {From: "09:00", To: "09:00"}, {From: "09:00", To: "10:00", Days: []int{7}}} {
		if bad.check() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	all := &WorkHours{From: "1:00", To: "2:00", Days: []int{0, 1, 2, 3, 4, 5, 6, 6}}
	if all.check(); all.Days != nil {
		t.Fatal("seven days should mean every day")
	}
}

func TestWorkerNodesRespectHoursAndLabels(t *testing.T) {
	closed := &WorkHours{From: "00:00", To: "00:01", Days: []int{(int(time.Now().Weekday()) + 3) % 7}}
	c := &Controller{conns: map[string]*nodeConn{"a": {}, "b": {}, "c": {}},
		lastHB:    map[string]*Heartbeat{"a": {InferenceRdy: true, LoadedModel: "m"}, "b": {InferenceRdy: true, LoadedModel: "m"}, "c": {InferenceRdy: true, LoadedModel: "m"}},
		nodes:     map[string]*NodeRec{"a": {ID: "a", Labels: []string{"gpu"}}, "b": {ID: "b", Labels: []string{"gpu"}, Hours: closed}, "c": {ID: "c"}},
		pipelines: map[string]*pipeline{}, cooldown: map[string]time.Time{}, models: map[string]*ModelRec{}}
	if w := c.workerNodes(); len(w) != 2 {
		t.Fatalf("off-hours node should be left out: %v", w)
	}
	if w := c.taskWorkers(&Task{NodeLabel: "gpu"}); len(w) != 1 || w[0] != "a" {
		t.Fatalf("label filter: %v", w)
	}
	if s := c.nodeSettingsLocked(c.nodes["b"]); s.IdleUnloadS != int(minIdleUnload/time.Second) {
		t.Fatalf("off hours should unload soon, got %ds", s.IdleUnloadS)
	}
	if got := normLabels([]string{" GPU ", "gpu", "Office PC", "", "x!y"}); strings.Join(got, ",") != "gpu,office-pc,xy" {
		t.Fatalf("labels %v", got)
	}
}

func TestWakeOnLAN(t *testing.T) {
	p, err := magicPacket("aa:bb:cc:dd:ee:ff")
	if err != nil || len(p) != 102 || p[0] != 0xFF || p[6] != 0xAA || p[101] != 0xFF || p[96] != 0xAA {
		t.Fatalf("packet %x %v", p, err)
	}
	if _, err := magicPacket("nope"); err == nil {
		t.Fatal("bad MAC accepted")
	}
	if got := wakeTargets("192.0.2.17:51000"); len(got) != 2 || got[1] != "192.0.2.255:9" {
		t.Fatalf("targets %v", got)
	}
	var mu sync.Mutex
	var sent []string
	old := sendWake
	sendWake = func(target string, pkt []byte) error { mu.Lock(); sent = append(sent, target); mu.Unlock(); return nil }
	defer func() { sendWake = old }()
	c := &Controller{conns: map[string]*nodeConn{"on": {}},
		nodes: map[string]*NodeRec{
			"off":   {ID: "off", LastAddr: "192.0.2.17:5000", Hello: &Hello{MACs: []string{"aa:bb:cc:dd:ee:ff"}}, Labels: []string{"gpu"}},
			"other": {ID: "other", Hello: &Hello{MACs: []string{"aa:bb:cc:dd:ee:00"}}},
			"nomac": {ID: "nomac", Hello: &Hello{}},
			"on":    {ID: "on", Hello: &Hello{MACs: []string{"aa:bb:cc:dd:ee:01"}}}}}
	if err := c.wakeNode("on"); err == nil {
		t.Fatal("online node woken")
	}
	if err := c.wakeNode("nomac"); err == nil || !strings.Contains(err.Error(), "network card") {
		t.Fatalf("no MAC: %v", err)
	}
	c.wakeForWork("gpu") // setting off: nothing
	if len(sent) != 0 {
		t.Fatal("woke with the setting off")
	}
	c.settings.WakeForWork = true
	c.wakeForWork("gpu")
	if len(sent) != 2 { // only "off" (labelled), to both targets
		t.Fatalf("sent %v", sent)
	}
	c.wakeForWork("gpu") // within wakeEvery: not again
	if len(sent) != 2 {
		t.Fatalf("woke twice: %v", sent)
	}
}

func TestScheduleNext(t *testing.T) {
	d := &Schedule{Every: "daily", At: "2:30"}
	if err := d.check(); err != nil || d.At != "02:30" {
		t.Fatal(err)
	}
	if got := d.next(at(3, 1, 0)); !got.Equal(at(3, 2, 30)) {
		t.Fatalf("daily same day %v", got)
	}
	if got := d.next(at(3, 2, 30)); !got.Equal(at(4, 2, 30)) {
		t.Fatalf("daily next day %v", got)
	}
	w := &Schedule{Every: "weekly", At: "23:00", Weekday: 1}  // Monday
	if got := w.next(at(3, 9, 0)); !got.Equal(at(9, 23, 0)) { // Tue the 3rd -> Mon the 9th
		t.Fatalf("weekly %v", got)
	}
	if (&Schedule{Every: "hourly", At: "01:00"}).check() == nil {
		t.Fatal("hourly accepted")
	}
}

func TestTemplatesAndSchedules(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), tasks: map[string]*Task{}, projects: map[string]*Project{}}
	c.settings.DefaultPlanner = "claude"
	m := http.NewServeMux()
	m.HandleFunc("POST /api/templates", c.handleNewTemplate)
	m.HandleFunc("POST /api/templates/{id}", c.handleEditTemplate)
	m.HandleFunc("DELETE /api/templates/{id}", c.handleDeleteTemplate)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	// The default planner is Claude: a schedule is refused, a plain saved task is fine.
	if rec := do("POST", "/api/templates", `{"request":{"prompt":"bump deps"},"schedule":{"every":"daily","at":"02:00"}}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "must plan on a node") {
		t.Fatalf("cloud schedule: %d %s", rec.Code, rec.Body)
	}
	rec := do("POST", "/api/templates", `{"request":{"prompt":"bump deps\nand run tests","node_label":"GPU"}}`)
	var tpl Template
	json.Unmarshal(rec.Body.Bytes(), &tpl)
	if rec.Code != 200 || tpl.Name != "bump deps" || tpl.Request.NodeLabel != "gpu" || tpl.Schedule != nil {
		t.Fatalf("new %d %s", rec.Code, rec.Body)
	}
	c.settings.DefaultPlanner = "node:n1"
	if rec := do("POST", "/api/templates/"+tpl.ID, `{"schedule":{"every":"weekly","at":"03:00","weekday":6}}`); rec.Code != 200 {
		t.Fatalf("schedule %d %s", rec.Code, rec.Body)
	}
	// Saved, and loaded again with a future next run.
	c2 := &Controller{dataDir: c.dataDir}
	c2.loadTemplates()
	if len(c2.templates) != 1 || c2.templates[0].NextRun == nil || !c2.templates[0].NextRun.After(time.Now()) || c2.templates[0].NextRun.Weekday() != time.Saturday {
		t.Fatalf("loaded %+v", c2.templates)
	}
	// A due run whose previous task is still going is skipped and noted.
	c.tasks["t0"] = &Task{ID: "t0", Status: "running"}
	c.mu.Lock()
	past := time.Now().Add(-time.Minute)
	c.templates[0].NextRun, c.templates[0].LastTask = &past, "t0"
	c.mu.Unlock()
	c.runDue(time.Now())
	if tp := c.templates[0]; !strings.Contains(tp.LastNote, "still going") || !tp.NextRun.After(time.Now()) {
		t.Fatalf("skip: %+v", tp)
	}
	if rec := do("POST", "/api/templates/"+tpl.ID, `{"schedule":{"every":""}}`); rec.Code != 200 || c.templates[0].Schedule != nil {
		t.Fatal("clear schedule")
	}
	if rec := do("DELETE", "/api/templates/"+tpl.ID, ""); rec.Code != 200 || len(c.templates) != 0 {
		t.Fatal("delete")
	}
	if _, err := os.Stat(filepath.Join(c.dataDir, "templates.json")); err != nil {
		t.Fatal(err)
	}
}

// A scheduled task asks no questions and approves its own plan.
func TestUnattendedPlanApprovesItself(t *testing.T) {
	r := newBonRig(t, map[string]string{"n1": "big-32b"})
	c := r.c
	c.settings = Settings{}
	tk := r.task
	tk.Planner, tk.Status, tk.Round, tk.Plan, tk.Unattended = "node:n1", "planning", 1, nil, true
	tk.Subtasks = nil
	tk.Messages = []TaskMessage{{ID: "m1", Round: 1, Role: "user", Kind: "text", Text: "make a dice roller"}}
	c.tasks = map[string]*Task{tk.ID: tk}
	const planJSON = `{"title":"Dice","summary":"s","language":"python","subtasks":[{"id":"s1","title":"dice","files":["dice.py"],"instructions":"write roll()","difficulty":"easy"}]}`
	c.inferStub = func(ctx context.Context, node string, spec InferenceSpec) (*InferenceOutput, error) {
		if strings.Contains(spec.Messages[0].Content+spec.Messages[len(spec.Messages)-1].Content, "dice.py") {
			return &InferenceOutput{Text: "```python\ndef roll():\n    return 4\n```"}, nil
		}
		return &InferenceOutput{Text: planJSON}, nil
	}
	c.startTask(tk, c.planTask)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk.mu.Lock()
		ok := tk.Approved != nil
		tk.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	tk.mu.Lock()
	approved, cancel := tk.Approved, tk.cancel
	var auto bool
	for _, m := range tk.Messages {
		auto = auto || strings.Contains(m.Text, "started by a schedule")
	}
	tk.mu.Unlock()
	if cancel != nil {
		defer cancel()
	}
	if approved == nil || !auto {
		t.Fatalf("plan not approved on its own (approved %v)", approved)
	}
}
