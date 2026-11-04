package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParseIdleDuration(t *testing.T) {
	ok := map[string]time.Duration{
		"30s": 30 * time.Second, "5m": 5 * time.Minute, "1hr": time.Hour, "2d": 48 * time.Hour,
		"1.5h": 90 * time.Minute, " 10 MIN ": 10 * time.Minute, "2 hours": 2 * time.Hour,
		"1day": 24 * time.Hour, "45sec": 45 * time.Second, ".5h": 30 * time.Minute,
		"0": 0, "never": 0, "OFF": 0, "0m": 0, "0.0h": 0,
	}
	for in, want := range ok {
		got, err := parseIdleDuration(in)
		if err != nil || got != want {
			t.Errorf("%q: got %v, %v; want %v", in, got, err, want)
		}
	}
	bad := map[string]string{
		"": "not a time", "abc": "not a time", "30": "needs a unit", "5x": "unknown unit",
		"-5m": "not a time", "5s5m": "not a time", "1s": "too short", "9s": "too short", "400d": "too long",
	}
	for in, frag := range bad {
		_, err := parseIdleDuration(in)
		if err == nil || !strings.Contains(err.Error(), frag) {
			t.Errorf("%q: error %v, want one containing %q", in, err, frag)
		}
	}
}

func TestFormatIdleDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "never", 30 * time.Second: "30s", time.Hour: "1h",
		90 * time.Minute: "1h 30m", 49 * time.Hour: "2d 1h"} {
		if got := formatIdleDuration(d); got != want {
			t.Errorf("%v: got %q want %q", d, got, want)
		}
	}
}

func TestIdleUnloadFor(t *testing.T) {
	cases := []struct {
		global, node string
		want         time.Duration
	}{
		{"", "", time.Hour}, {"5m", "", 5 * time.Minute}, {"5m", "2h", 2 * time.Hour},
		{"5m", "0", 0}, {"0", "", 0}, {"0", "30s", 30 * time.Second}, {"junk", "", time.Hour}, {"5m", "junk", 5 * time.Minute},
	}
	for _, c := range cases {
		if got := idleUnloadFor(c.global, c.node); got != c.want {
			t.Errorf("global %q node %q: got %v want %v", c.global, c.node, got, c.want)
		}
	}
}

func idleTestMgr(model string, rpc []string) *llamaMgr {
	n := &Node{stats: newStatSampler(), jobs: map[string]context.CancelFunc{}, wsLastUsed: map[string]time.Time{}}
	l := newLlamaMgr(n)
	n.llama = l
	n.rpc = newRPCMgr(n)
	l.cmd, l.ready, l.model = &exec.Cmd{}, true, model
	l.opts = ModelLoad{Name: model, RPC: rpc}
	l.idleAfter = time.Minute
	l.lastUsed = time.Now().Add(-time.Hour)
	return l
}

func TestIdleUnloadAndWake(t *testing.T) {
	l := idleTestMgr("m", nil)
	if !l.idleCheck(time.Now()) {
		t.Fatal("an idle model should be unloaded")
	}
	if m, _, _, _ := l.status(); m != "" {
		t.Errorf("process should be stopped, status says %q loaded", m)
	}
	if l.coldModel() != "m" {
		t.Errorf("cold model = %q, want m", l.coldModel())
	}
	// A request for another model does not wake it.
	if e := l.wake("other"); e != nil || l.coldModel() != "m" {
		t.Errorf("wake(other) = %v, cold %q", e, l.coldModel())
	}
	// A request for it tries to load; here llama.cpp is not installed, so it fails, and stays cold.
	e := l.wake("m")
	if e == nil || !e.Retryable || !strings.Contains(e.Message, "reloading m") {
		t.Errorf("wake(m) = %+v, want a retryable reload error", e)
	}
	if l.coldModel() != "m" {
		t.Error("a failed reload should leave the model cold so the next request tries again")
	}
	// A deliberate unload forgets it.
	l.stop()
	if l.coldModel() != "" {
		t.Error("stop() should clear the cold model")
	}
}

func TestIdleCheckSkips(t *testing.T) {
	now := time.Now()
	for name, mod := range map[string]func(*llamaMgr){
		"never":      func(l *llamaMgr) { l.idleAfter = 0 },
		"busy":       func(l *llamaMgr) { l.busy = 1 },
		"recent":     func(l *llamaMgr) { l.lastUsed = now },
		"not ready":  func(l *llamaMgr) { l.ready = false },
		"not loaded": func(l *llamaMgr) { l.cmd = nil },
	} {
		l := idleTestMgr("m", nil)
		mod(l)
		if l.idleCheck(now) || l.cold != nil {
			t.Errorf("%s: should not unload", name)
		}
	}
	l := idleTestMgr("m", []string{"192.0.2.5:50052"})
	if l.idleCheck(now) {
		t.Error("a pooled model must never be unloaded for idleness")
	}
}

func TestColdHeartbeat(t *testing.T) {
	l := idleTestMgr("m", nil)
	if hb := l.n.buildHeartbeat(); hb.IdleUnloaded {
		t.Fatal("a loaded model is not idle-unloaded")
	}
	l.idleCheck(time.Now())
	hb := l.n.buildHeartbeat()
	if hb.LoadedModel != "m" || !hb.InferenceRdy || !hb.IdleUnloaded {
		t.Errorf("a cold node should still offer its model: %+v", hb)
	}
	l.stop()
	if hb := l.n.buildHeartbeat(); hb.LoadedModel != "" || hb.IdleUnloaded {
		t.Errorf("after a deliberate unload nothing is offered: %+v", hb)
	}
}

func TestIdleUnloadSettingsAPI(t *testing.T) {
	c := poolTestController(t)
	c.settings = defaultSettings()
	c.nodes["a"] = &NodeRec{ID: "a"}
	post := func(path, body string, h http.HandlerFunc) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
		req.SetPathValue("id", "a")
		w := httptest.NewRecorder()
		h(w, req)
		return w
	}
	if w := post("/api/settings", `{"idle_unload":"soon"}`, c.handleSaveSettings); w.Code != 400 || !strings.Contains(w.Body.String(), "not a time") {
		t.Errorf("bad global value: %d %s", w.Code, w.Body)
	}
	if c.settings.IdleUnload != "" {
		t.Error("a rejected value must not be stored")
	}
	if w := post("/api/settings", `{"idle_unload":"30m"}`, c.handleSaveSettings); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if v := c.settingsView(); v["idle_unload"] != "30m" || v["idle_unload_effective"] != "30m" {
		t.Errorf("view: %v", v)
	}
	if w := post("/api/nodes/a/settings", `{"idle_unload":"7"}`, c.handleNodeSettings); w.Code != 400 {
		t.Errorf("node value without a unit: %d", w.Code)
	}
	if w := post("/api/nodes/a/settings", `{"idle_unload":"0"}`, c.handleNodeSettings); w.Code != 200 {
		t.Fatalf("node save: %d %s", w.Code, w.Body)
	}
	c.mu.Lock()
	got := c.nodeSettingsLocked(c.nodes["a"]).IdleUnloadS
	c.nodes["a"].IdleUnload = ""
	inherit := c.nodeSettingsLocked(c.nodes["a"]).IdleUnloadS
	c.mu.Unlock()
	if got != 0 || inherit != 1800 {
		t.Errorf("node override never = %d (want 0), inherited = %d (want 1800)", got, inherit)
	}
}

func TestUnloadNowGoesColdAndRefuses(t *testing.T) {
	l := idleTestMgr("m", nil)
	l.busy = 1
	if _, err := l.unloadNow("test"); err != errUnloadBusy || l.cold != nil {
		t.Fatalf("busy: err %v, cold %v", err, l.cold)
	}
	l.busy, l.ready = 0, false
	if _, err := l.unloadNow("test"); err != errUnloadLoading {
		t.Fatalf("loading: err %v", err)
	}
	l.ready = true
	if name, err := l.unloadNow("test"); err != nil || name != "m" {
		t.Fatalf("unload: %q %v", name, err)
	}
	if l.coldModel() != "m" {
		t.Fatalf("cold model = %q, want m (reloads on next request)", l.coldModel())
	}
	if hb := l.n.buildHeartbeat(); hb.LoadedModel != "m" || !hb.IdleUnloaded {
		t.Errorf("heartbeat after tray unload: %+v", hb)
	}
	if _, err := l.unloadNow("again"); err != errUnloadNothing {
		t.Errorf("second unload: %v", err)
	}

	p := idleTestMgr("big", []string{"192.0.2.5:50052"})
	if _, err := p.unloadNow("test"); err != errUnloadPooled || p.cold != nil {
		t.Errorf("pooled: err %v cold %v", err, p.cold)
	}
}

func TestUnloadFromTrayRefusesDuringJob(t *testing.T) {
	l := idleTestMgr("m", nil)
	l.n.jobs["j1"] = func() {}
	if err := l.n.unloadFromTray(); err != errUnloadJobs || l.cold != nil {
		t.Fatalf("with a running job: err %v cold %v", err, l.cold)
	}
	delete(l.n.jobs, "j1")
	if err := l.n.unloadFromTray(); err != nil || l.coldModel() != "m" {
		t.Fatalf("idle: err %v cold %q", err, l.coldModel())
	}
}

func TestUnloadItemState(t *testing.T) {
	cases := []struct {
		st   controlStatus
		ok   bool
		frag string
	}{
		{controlStatus{}, false, "Unload model"},
		{controlStatus{Model: "m", ModelReady: true}, true, "Unload model"},
		{controlStatus{Model: "m"}, false, "loading"},
		{controlStatus{Model: "m", ModelReady: true, RunningJobs: 1}, false, "running job"},
		{controlStatus{Model: "m", ModelReady: true, Pooled: true}, false, "pooled"},
	}
	for _, c := range cases {
		title, ok := unloadItemState(&c.st)
		if ok != c.ok || !strings.Contains(title, c.frag) {
			t.Errorf("%+v: %q %v", c.st, title, ok)
		}
	}
}

func TestPauseNowCancelsAsRetryable(t *testing.T) {
	l := idleTestMgr("m", nil)
	n := l.n
	n.dataDir = t.TempDir()
	cancelled := false
	n.jobs["j1"] = func() { cancelled = true }
	n.pauseNow()
	if !cancelled || !n.isPaused() {
		t.Fatalf("pauseNow: cancelled %v paused %v", cancelled, n.isPaused())
	}
	res := JobResult{JobID: "j1", Status: "cancelled"}
	n.markStoppedByPause(&res)
	if res.Status != "failed" || res.Error == nil || res.Error.Code != "busy" || !isNodeProblem(res.Error) {
		t.Fatalf("result after pause now: %+v %+v", res, res.Error)
	}
	// Only once, and only for jobs pauseNow cancelled.
	other := JobResult{JobID: "j1", Status: "cancelled"}
	n.markStoppedByPause(&other)
	if other.Status != "cancelled" {
		t.Errorf("second result: %+v", other)
	}
}

func TestWorkMode(t *testing.T) {
	for _, c := range []struct {
		st   controlStatus
		want string
	}{
		{controlStatus{}, "ready"},
		{controlStatus{RunningJobs: 2}, "ready"},
		{controlStatus{Paused: true, RunningJobs: 1}, "finish"},
		{controlStatus{Paused: true}, "paused"},
	} {
		if got := workMode(&c.st); got != c.want {
			t.Errorf("%+v: %s want %s", c.st, got, c.want)
		}
	}
}
