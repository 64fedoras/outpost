package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPauseFromWebIsRememberedWithItsEnd(t *testing.T) {
	l := idleTestMgr("m", nil)
	n := l.n
	n.dataDir = t.TempDir()
	n.applyPause(NodePause{Mode: "drain", Minutes: 60})
	p, by, until := n.pauseInfo()
	if !p || by != "web" || until == nil || time.Until(*until) < 59*time.Minute {
		t.Fatalf("pauseInfo = %v %q %v", p, by, until)
	}
	hb := n.buildHeartbeat()
	if !hb.Paused || hb.PausedBy != "web" || hb.PausedUntil == nil {
		t.Fatalf("heartbeat %+v", hb)
	}
	// A restart reads it back.
	m := &Node{dataDir: n.dataDir}
	m.loadPaused()
	if !m.paused || m.pausedBy != "web" || m.pausedUntil.IsZero() {
		t.Fatalf("after restart: %v %q %v", m.paused, m.pausedBy, m.pausedUntil)
	}
	n.applyPause(NodePause{Mode: "resume"})
	if p, _, _ := n.pauseInfo(); p {
		t.Fatal("resume did not resume")
	}
	if _, err := os.Stat(pausedPath(n.dataDir)); !os.IsNotExist(err) {
		t.Fatal("paused file left behind")
	}
}

func TestOldPausedFileReadsAsTray(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(pausedPath(dir), []byte("paused from the tray icon\n"), 0o600)
	n := &Node{dataDir: dir}
	n.loadPaused()
	if !n.paused || n.pausedBy != "tray" || !n.pausedUntil.IsZero() {
		t.Fatalf("%v %q %v", n.paused, n.pausedBy, n.pausedUntil)
	}
}

func TestPauseNowFromWebCancelsAsRetryable(t *testing.T) {
	l := idleTestMgr("m", nil)
	n := l.n
	n.dataDir = t.TempDir()
	cancelled := false
	n.jobs["j1"] = func() { cancelled = true }
	n.applyPause(NodePause{Mode: "now"})
	if !cancelled || !n.isPaused() {
		t.Fatalf("cancelled %v paused %v", cancelled, n.isPaused())
	}
	res := &JobResult{JobID: "j1", Status: "cancelled"}
	n.markStoppedByPause(res)
	if res.Status != "failed" || res.Error == nil || !res.Error.Retryable {
		t.Fatalf("result %+v", res)
	}
}

func TestPauseHandlerChecksNode(t *testing.T) {
	c := &Controller{
		nodes: map[string]*NodeRec{"old": {ID: "old", Hello: &Hello{}}, "new": {ID: "new", Hello: &Hello{Features: nodeFeatures}}},
		conns: map[string]*nodeConn{"old": {}},
	}
	call := func(id, body string) (int, string) {
		r := httptest.NewRequest("POST", "/api/nodes/"+id+"/pause", strings.NewReader(body))
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		c.handlePauseNode(w, r)
		return w.Code, w.Body.String()
	}
	if code, _ := call("new", `{"mode":"sleep"}`); code != http.StatusBadRequest {
		t.Fatalf("bad mode: %d", code)
	}
	if code, _ := call("new", `{"mode":"drain","minutes":-1}`); code != http.StatusBadRequest {
		t.Fatalf("bad minutes: %d", code)
	}
	if code, body := call("new", `{"mode":"drain"}`); code != 409 || !strings.Contains(body, "offline") {
		t.Fatalf("offline: %d %s", code, body)
	}
	if code, body := call("old", `{"mode":"drain"}`); code != 409 || !strings.Contains(body, "tray") {
		t.Fatalf("old node: %d %s", code, body)
	}
}
