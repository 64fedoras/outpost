package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRedactRemovesSecrets(t *testing.T) {
	s := Settings{}
	b, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	m["claude_key_enc"] = "abc"
	m["nested"] = map[string]any{"webhook_url": "https://hooks.example.local/x", "count": 3.0, "list": []any{map[string]any{"api_token": "t"}}}
	m["model"] = "keep me"
	out := string(redactedJSON(m))
	for _, bad := range []string{"abc", "hooks.example.local", `"t"`} {
		if strings.Contains(out, bad) {
			t.Fatalf("secret %q left in:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "keep me") || !strings.Contains(out, `"count": 3`) {
		t.Fatalf("ordinary fields removed:\n%s", out)
	}
}

func TestVerifyBackupCatchesTruncation(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"x":1}`), 0o600)
	os.MkdirAll(filepath.Join(dir, "tasks", "t1"), 0o700)
	os.WriteFile(filepath.Join(dir, "tasks", "t1", "task.json"), bytes.Repeat([]byte("task "), 50000), 0o600)
	var buf bytes.Buffer
	if err := writeBackup(&buf, dir, "correct horse", false); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(t.TempDir(), "good.opb")
	os.WriteFile(good, buf.Bytes(), 0o600)
	n, err := verifyBackup(good, "correct horse")
	if err != nil || n < 2 {
		t.Fatalf("good backup: n=%d err=%v", n, err)
	}
	if _, err := verifyBackup(good, "wrong pass"); err == nil {
		t.Fatal("wrong passphrase passed")
	}
	cut := filepath.Join(t.TempDir(), "cut.opb")
	os.WriteFile(cut, buf.Bytes()[:buf.Len()*2/3], 0o600)
	if _, err := verifyBackup(cut, "correct horse"); err == nil {
		t.Fatal("truncated backup passed")
	}
}

// fakeNodeServer plays a node: it answers node.ask over a real WebSocket.
func fakeNodeServer(t *testing.T, c *Controller, n *Node, answer bool) *nodeConn {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		for {
			_, b, err := ws.Read(context.Background())
			if err != nil {
				return
			}
			var env Envelope
			if json.Unmarshal(b, &env) != nil || env.Type != "node.ask" || !answer {
				continue
			}
			var req AskRequest
			_ = json.Unmarshal(env.Body, &req)
			// What answerAsk would send, delivered straight to the controller.
			lt, err := n.logTail(func() LogTailArgs { var a LogTailArgs; _ = json.Unmarshal(req.Args, &a); return a }())
			ans := AskAnswer{Req: req.Req}
			if err != nil {
				ans.Error = err.Error()
			} else {
				ans.Data, _ = json.Marshal(lt)
			}
			body, _ := json.Marshal(ans)
			c.deliverAnswer(body)
		}
	}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return &nodeConn{conn: conn, addr: "192.0.2.10:5000"}
}

func TestAskLogTailRoundTrip(t *testing.T) {
	c := &Controller{conns: map[string]*nodeConn{}}
	n := &Node{dataDir: t.TempDir(), llama: &llamaMgr{}}
	nodeLog.Write([]byte("line one\nline two\nline three\n"))
	c.conns["n1"] = fakeNodeServer(t, c, n, true)

	var lt LogTail
	if err := c.ask("n1", "log.tail", LogTailArgs{Source: "node", Lines: 2}, 5*time.Second, &lt); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(lt.Text, "line two\nline three") || strings.Count(lt.Text, "\n") != 1 {
		t.Fatalf("tail = %q", lt.Text)
	}
	if err := c.ask("n1", "log.tail", LogTailArgs{Source: "tray"}, 5*time.Second, &lt); err != nil || lt.Note == "" {
		t.Fatalf("missing tray log should be a note, got %+v err=%v", lt, err)
	}
	if err := c.ask("n1", "log.tail", LogTailArgs{Source: "llama"}, 5*time.Second, &lt); err != nil || lt.Note == "" {
		t.Fatalf("llama never ran should be a note, got %+v err=%v", lt, err)
	}
	if err := c.ask("n1", "log.tail", LogTailArgs{Source: "bogus"}, 5*time.Second, &lt); err == nil {
		t.Fatal("unknown source should fail")
	}
	if err := c.ask("gone", "log.tail", LogTailArgs{}, time.Second, &lt); err == nil {
		t.Fatal("offline node should fail")
	}
}

func TestAskOldNodeTimesOut(t *testing.T) {
	c := &Controller{conns: map[string]*nodeConn{}}
	c.conns["old"] = fakeNodeServer(t, c, nil, false)
	if err := c.ask("old", "log.tail", LogTailArgs{}, 200*time.Millisecond, nil); err != errOldNode {
		t.Fatalf("err = %v, want errOldNode", err)
	}
}

func TestRingLogKeepsWholeLines(t *testing.T) {
	r := &ringLog{max: 64}
	for i := 0; i < 50; i++ {
		r.Write([]byte("0123456789\n"))
	}
	s := r.String()
	if len(s) > 64 || !strings.HasPrefix(s, "0123456789") {
		t.Fatalf("ring = %q", s)
	}
}

func TestClusterCheckOfflineAndVersion(t *testing.T) {
	c := &Controller{
		nodes:     map[string]*NodeRec{"a": {ID: "a", Name: "alpha"}, "b": {ID: "b", Name: "beta", Hello: &Hello{AgentVersion: "0.0.1", ModelsFreeBytes: 1 << 30, OS: "linux"}}},
		conns:     map[string]*nodeConn{"b": {}},
		lastSeen:  map[string]time.Time{"a": time.Date(2026, 1, 2, 3, 4, 0, 0, time.Local)},
		lastHB:    map[string]*Heartbeat{},
		clockSkew: map[string]time.Duration{"b": 2 * time.Minute},
	}
	rows := c.checkRowsInit()
	if len(rows) != 2 || rows[0].Name != "beta" || !rows[0].Online || rows[1].Online || !strings.Contains(rows[1].Offline, "Jan 2 03:04") {
		t.Fatalf("rows: %+v %+v", rows[0], rows[1])
	}
	c.check.Rows = rows
	c.checkNode(rows[0])
	st := rows[0].Steps
	if st["version"].State != "warn" || st["disk"].State != "warn" || st["clock"].State != "warn" {
		t.Fatalf("steps: %+v", st)
	}
	if st["model"].State != "skip" || st["command"].State != "skip" || st["port"].State != "skip" {
		t.Fatalf("no model, exec off and no peer port should skip: %+v", st)
	}
	var fixes []string
	for _, n := range rows[0].Notes {
		fixes = append(fixes, n.Fix)
	}
	if len(rows[0].Notes) != 3 || !strings.Contains(strings.Join(fixes, " "), "timedatectl") {
		t.Fatalf("notes: %+v", rows[0].Notes)
	}
}

func TestDiagnosticsZip(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), nodes: map[string]*NodeRec{"a": {ID: "a", Name: "alpha"}}, conns: map[string]*nodeConn{},
		lastHB: map[string]*Heartbeat{}, lastSeen: map[string]time.Time{}, models: map[string]*ModelRec{}, tasks: map[string]*Task{},
		updates: map[string]*UpdateStatus{}}
	c.settings.AnthropicKeyEnc = "sekrit-value"
	c.tasks["t1"] = &Task{ID: "t1", Title: "demo", Prompt: "private prompt text", Status: "done", Created: time.Now(),
		Events: []TaskEvent{{Time: time.Now(), Level: "info", Text: "planned"}}}
	ctlLog.Write([]byte("controller started\n"))
	var buf bytes.Buffer
	if err := c.writeDiagnostics(&buf, diagOptions{NodeLogs: true}); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	all := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		var b bytes.Buffer
		b.ReadFrom(rc)
		rc.Close()
		all[f.Name] = b.String()
	}
	for _, want := range []string{"summary.txt", "controller.log", "settings.json", "nodes.json", "models.json", "tasks/t1.txt"} {
		if _, ok := all[want]; !ok {
			t.Fatalf("zip lacks %s; has %v", want, all)
		}
	}
	for name, body := range all {
		if strings.Contains(body, "sekrit-value") {
			t.Fatalf("%s contains the Claude key", name)
		}
		if strings.Contains(body, "private prompt text") {
			t.Fatalf("%s contains the prompt although prompts were not asked for", name)
		}
	}
	if !strings.Contains(all["tasks/t1.txt"], "planned") || !strings.Contains(all["summary.txt"], "alpha") {
		t.Fatalf("task or summary incomplete:\n%s\n%s", all["tasks/t1.txt"], all["summary.txt"])
	}
}
