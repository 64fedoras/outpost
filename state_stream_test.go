package main

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStateStreamPushesChanges(t *testing.T) {
	old := statePushEvery
	statePushEvery = 20 * time.Millisecond
	defer func() { statePushEvery = old }()
	c := &Controller{dataDir: t.TempDir(), nodes: map[string]*NodeRec{}, conns: map[string]*nodeConn{}, lastHB: map[string]*Heartbeat{},
		lastSeen: map[string]time.Time{}, jobs: map[string]*JobRec{}, releases: map[string]*Release{}, updates: map[string]*UpdateStatus{},
		tasks: map[string]*Task{}, projects: map[string]*Project{}, models: map[string]*ModelRec{}}
	srv := httptest.NewServer(http.HandlerFunc(c.handleStateEvents))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	next := func() string {
		for sc.Scan() {
			if l := sc.Text(); strings.HasPrefix(l, "data: ") {
				return l
			}
		}
		t.Fatal("stream ended")
		return ""
	}
	first := next()
	if !strings.Contains(first, `"state"`) || !strings.Contains(first, `"tasks"`) {
		t.Fatalf("first payload %.200s", first)
	}
	c.mu.Lock()
	c.nodes["n1"] = &NodeRec{ID: "n1", Name: "gpu-box"}
	c.mu.Unlock()
	if second := next(); !strings.Contains(second, "gpu-box") {
		t.Fatalf("change not pushed: %.300s", second)
	}
	// Nothing changes: nothing more is sent for a while (only pings every 20 s).
	got := make(chan string, 1)
	go func() {
		if sc.Scan() {
			got <- sc.Text()
		}
	}()
	select {
	case l := <-got:
		if l != "" && l != "event: state" {
			t.Fatalf("unexpected line while idle: %.100q", l)
		}
		if l == "event: state" {
			t.Fatal("unchanged state was sent again")
		}
	case <-time.After(200 * time.Millisecond):
	}
}
