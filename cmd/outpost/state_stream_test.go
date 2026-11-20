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
	second := next()
	if !strings.Contains(second, "gpu-box") {
		t.Fatalf("change not pushed: %.300s", second)
	}
	if strings.Contains(second, `"tasks"`) || strings.Contains(second, `"projects"`) {
		t.Fatalf("unchanged task list or projects sent again: %.300s", second)
	}
	// A new browser notification is announced, so tabs know to fetch it.
	c.nt.mu.Lock()
	c.nt.seq++
	c.nt.mu.Unlock()
	if third := next(); !strings.Contains(third, `"notify_last":1`) {
		t.Fatalf("notification not announced: %.300s", third)
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

// A payload leaves out the lists the tab already has.
func TestStatePayloadLeavesOutUnchangedParts(t *testing.T) {
	lists := func(tasks string) map[string][]byte {
		return map[string][]byte{"tasks": []byte(tasks), "projects": []byte(`[]`), "groups": []byte(`[]`), "chats": []byte(`[{"id":"c1"}]`)}
	}
	p := &stateParts{state: []byte(`{"a":1}`), lists: lists(`[{"id":"t1"}]`), notifyLast: 3}
	full := string(p.payload(nil))
	if full != `{"state":{"a":1},"tasks":[{"id":"t1"}],"projects":[],"groups":[],"chats":[{"id":"c1"}],"notify_last":3}` {
		t.Fatalf("full: %s", full)
	}
	sent := stateSent{}
	p.sentAll(sent)
	if got := string(p.payload(sent)); got != `{"state":{"a":1},"notify_last":3}` {
		t.Fatalf("unchanged: %s", got)
	}
	p2 := &stateParts{state: p.state, lists: lists(`[{"id":"t2"}]`), notifyLast: 3}
	if got := string(p2.payload(sent)); got != `{"state":{"a":1},"tasks":[{"id":"t2"}],"notify_last":3}` {
		t.Fatalf("tasks changed: %s", got)
	}
}
