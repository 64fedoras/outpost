package main

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func newGenTestController(t *testing.T) *Controller {
	c := &Controller{dataDir: t.TempDir(), tasks: map[string]*Task{}, nodes: map[string]*NodeRec{}, conns: map[string]*nodeConn{},
		lastHB: map[string]*Heartbeat{}, pipelines: map[string]*pipeline{}, cooldown: map[string]time.Time{}, models: map[string]*ModelRec{}}
	c.gens = &genStore{recs: map[string]*GenRec{}, busy: map[string]bool{}, prep: map[string]*sync.Mutex{}, kick: make(chan struct{}, 1)}
	return c
}

func TestGenRecMatches(t *testing.T) {
	g := &GenRec{Prompt: "A lighthouse at dusk", Model: "sdxl", Items: []*GenItem{{Seed: 4242}}}
	for _, q := range []string{"lighthouse", "DUSK", "sdxl", "4242"} {
		if !g.matches(q) {
			t.Errorf("%q should match", q)
		}
	}
	if g.matches("castle") || g.matches("42") {
		t.Error("false match")
	}
}

func TestImageSettingsPersistAPIAndPause(t *testing.T) {
	c := newGenTestController(t)
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"api_enabled":true,"pause_for_tasks":true}`))
	w := httptest.NewRecorder()
	c.handleImageSettings(w, r)
	if w.Code != 200 || !c.gens.settings.APIEnabled || !c.gens.settings.PauseForTasks {
		t.Fatalf("settings not saved: %d %+v", w.Code, c.gens.settings)
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(`{"max_gb":5}`))
	c.handleImageSettings(httptest.NewRecorder(), r)
	if !c.gens.settings.APIEnabled {
		t.Fatal("a request without the field must leave it alone")
	}
}

func TestNodeMakingPictureAndTaskWorking(t *testing.T) {
	c := newGenTestController(t)
	if c.nodeMakingPicture("n1") {
		t.Fatal("idle node")
	}
	c.gens.busy["n1"] = true
	c.mu.Lock()
	ok := c.nodeMakingPicture("n1") // must not deadlock with c.mu held
	c.mu.Unlock()
	if !ok {
		t.Fatal("rendering node not seen")
	}
	if c.anyTaskWorking() {
		t.Fatal("no tasks")
	}
	c.tasks["t"] = &Task{ID: "t", Status: "awaiting_approval"}
	if c.anyTaskWorking() {
		t.Fatal("waiting is not working")
	}
	c.tasks["t"].Status = "fixing"
	if !c.anyTaskWorking() {
		t.Fatal("fixing is working")
	}
}
