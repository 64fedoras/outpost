package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func shotTask(t *testing.T) *Task {
	dir := t.TempDir()
	tk := &Task{ID: "task_x", dir: dir, Round: 1, Title: "Page", Status: "done", Planner: "claude"}
	os.MkdirAll(tk.repoDir(), 0o755)
	return tk
}

func TestPreviewTargets(t *testing.T) {
	tk := shotTask(t)
	write := func(p string) {
		if err := tk.writeFile(p, "<html></html>"); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"about.html", "index.html", "mockups/b.html", "mockups/a.html", "style.css"} {
		write(p)
	}
	tk.touch("about.html", "index.html", "mockups/a.html")
	got := previewTargets(tk, &Plan{})
	if len(got) != 2 || got[0].Page != "index.html" || got[1].Page != "about.html" {
		t.Fatalf("touched pages, index first: %+v", got)
	}
	if got := previewTargets(tk, &Plan{PreviewPage: "./about.html"}); len(got) != 1 || got[0].Page != "about.html" {
		t.Fatalf("preview page: %+v", got)
	}
	if got := previewTargets(tk, &Plan{PreviewPage: "missing.html"}); len(got) != 2 {
		t.Fatalf("missing preview page falls back to touched pages: %+v", got)
	}
	got = previewTargets(tk, &Plan{Mockup: true})
	if len(got) != 2 || got[0].Label != "a" || got[1].Label != "b" {
		t.Fatalf("mockup variants: %+v", got)
	}
	app := &Plan{PreviewCommand: "python3 app.py", PreviewURL: "http://127.0.0.1:8000"}
	if got := previewTargets(tk, app); len(got) != 1 || got[0].URL == "" {
		t.Fatalf("app target: %+v", got)
	}
	app.PreviewURL = "http://example.com/"
	if got := previewTargets(tk, app); len(got) == 1 && got[0].URL != "" {
		t.Fatalf("a remote URL must not be previewed: %+v", got)
	}
	// A stylesheet-only change shows the main page.
	tk2 := shotTask(t)
	tk2.writeFile("index.html", "x")
	tk2.writeFile("style.css", "x")
	tk2.touch("style.css")
	if got := previewTargets(tk2, &Plan{}); len(got) != 1 || got[0].Page != "index.html" {
		t.Fatalf("css-only change: %+v", got)
	}
	if got := previewTargets(shotTask(t), &Plan{}); len(got) != 0 {
		t.Fatalf("no pages, no targets: %+v", got)
	}
}

func TestValidateMockupPlan(t *testing.T) {
	ok := &Plan{Subtasks: []PlanSubtask{{Files: []string{"mockups/a.html"}}, {Files: []string{"mockups/b.html"}}}}
	if err := validateMockupPlan(ok); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]*Plan{
		"one variant": {Subtasks: []PlanSubtask{{Files: []string{"mockups/a.html"}}}},
		"four":        {Subtasks: []PlanSubtask{{Files: []string{"mockups/a.html", "mockups/b.html", "mockups/c.html", "mockups/d.html"}}}},
		"real code":   {Subtasks: []PlanSubtask{{Files: []string{"mockups/a.html", "mockups/b.html", "app.py"}}}},
		"nested":      {Subtasks: []PlanSubtask{{Files: []string{"mockups/x/a.html", "mockups/b.html"}}}},
	} {
		if validateMockupPlan(p) == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
}

func TestChromeArgs(t *testing.T) {
	a := chromeArgs("/usr/bin/chromium", "shot-phone.png", 390, 844, "mockups/a.html")
	j := strings.Join(a, " ")
	for _, want := range []string{"/usr/bin/chromium --headless", "--no-sandbox", "--window-size=390,844", "--screenshot=shot-phone.png", "mockups/a.html"} {
		if !strings.Contains(j, want) {
			t.Errorf("args lack %q: %s", want, j)
		}
	}
	if a[len(a)-1] != "mockups/a.html" {
		t.Errorf("the page must be last: %v", a)
	}
}

func TestNoChromiumNode(t *testing.T) {
	c := &Controller{conns: map[string]*nodeConn{"a": {}}, lastHB: map[string]*Heartbeat{},
		nodes: map[string]*NodeRec{"a": {ID: "a", ExecEnabled: true, Hello: &Hello{Shell: shellSh, Tools: []ToolInfo{{Name: "go"}}}}}}
	if _, _, ok := c.chromiumNode(); ok {
		t.Fatal("a node without Chromium must not be chosen")
	}
	tk := shotTask(t)
	tk.writeFile("index.html", "x")
	_, err := c.renderShots(context.Background(), tk, []shotTarget{{Label: "index", Page: "index.html"}})
	if err == nil || !strings.Contains(err.Error(), "Chromium") {
		t.Fatalf("want the Chromium note, got %v", err)
	}
	c.nodes["a"].Hello.Tools = append(c.nodes["a"].Hello.Tools, ToolInfo{Name: "chromium", Cmd: "chromium", Path: "/usr/bin/chromium"})
	if id, tl, ok := c.chromiumNode(); !ok || id != "a" || tl.Path != "/usr/bin/chromium" {
		t.Fatalf("chromium node: %v %v %v", id, tl, ok)
	}
}

// fakeExecNode connects a websocket "node" that runs each exec job for real
// in a temp folder and reports the collected files back.
func fakeExecNode(t *testing.T, c *Controller, id string) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.mu.Lock()
		c.conns[id] = &nodeConn{conn: conn}
		c.mu.Unlock()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(64 << 20)
	go func() {
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var env Envelope
			json.Unmarshal(b, &env)
			if env.Type != "job.dispatch" {
				continue
			}
			var jd JobDispatch
			json.Unmarshal(env.Body, &jd)
			dir := t.TempDir()
			for _, in := range jd.Inputs {
				data, _ := base64.StdEncoding.DecodeString(in.InlineB64)
				os.MkdirAll(filepath.Dir(filepath.Join(dir, in.Path)), 0o755)
				os.WriteFile(filepath.Join(dir, in.Path), data, 0o644)
			}
			var spec ExecSpec
			json.Unmarshal(jd.Spec, &spec)
			cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
			cmd.Dir = dir
			var errb strings.Builder
			cmd.Stderr = &errb
			cmd.Run()
			files := []FileEntry{}
			for _, pat := range jd.Collect {
				if data, err := os.ReadFile(filepath.Join(dir, pat)); err == nil {
					files = append(files, inlineFile(pat, data))
				}
			}
			out, _ := json.Marshal(ExecOutput{ExitCode: cmd.ProcessState.ExitCode(), StderrTail: errb.String()})
			res, _ := json.Marshal(JobResult{JobID: jd.JobID, Status: "succeeded", Output: out, Files: files})
			c.handleNodeMessage(id, &Envelope{Type: "job.result", Body: res})
		}
	}()
	for i := 0; i < 100; i++ {
		c.mu.Lock()
		ok := c.conns[id] != nil
		c.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fake node did not connect")
}

func newShotController(bin string) *Controller {
	return &Controller{
		conns: map[string]*nodeConn{}, lastHB: map[string]*Heartbeat{}, lastSeen: map[string]time.Time{}, jobs: map[string]*JobRec{}, waiters: map[string]chan *JobResult{},
		nodes: map[string]*NodeRec{"n": {ID: "n", Name: "n", ExecEnabled: true,
			Hello: &Hello{Shell: shellSh, Tools: []ToolInfo{{Name: "chromium", Cmd: "chromium", Path: bin}}}}},
	}
}

// With a real headless Chromium available the whole path runs: inputs are
// sent, the browser renders, PNGs come back and are stored.
func TestRenderShotsWithBrowser(t *testing.T) {
	bin := os.Getenv("OUTPOST_TEST_CHROME")
	if bin == "" {
		if m, _ := filepath.Glob("/opt/pw-browsers/chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell"); len(m) > 0 {
			bin = m[0]
		}
	}
	if bin == "" {
		t.Skip("no headless Chromium; set OUTPOST_TEST_CHROME")
	}
	c := newShotController(bin)
	fakeExecNode(t, c, "n")
	tk := shotTask(t)
	tk.writeFile("index.html", `<html><body style="background:#246"><h1>Hello</h1></body></html>`)
	shots, err := c.renderShots(context.Background(), tk, []shotTarget{{Label: "index", Page: "index.html"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(shots) != 1 || shots[0].Desktop == "" || shots[0].Phone == "" {
		t.Fatalf("shots: %+v", shots)
	}
	for _, f := range []string{shots[0].Desktop, shots[0].Phone} {
		raw, err := os.ReadFile(filepath.Join(tk.shotsDir(), f))
		if err != nil || !isPNG(raw) {
			t.Fatalf("%s: %v", f, err)
		}
	}
}

func TestPickVariantAndShotServing(t *testing.T) {
	c := &Controller{tasks: map[string]*Task{}}
	tk := shotTask(t)
	tk.Mockup = true
	tk.Prompt = "A landing page for a bakery"
	tk.Plan = &Plan{Mockup: true}
	os.MkdirAll(tk.shotsDir(), 0o755)
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg==")
	os.WriteFile(filepath.Join(tk.shotsDir(), "a.png"), png, 0o644)
	tk.Messages = []TaskMessage{{ID: "m1", Kind: "mockup", Shots: []Shot{{Label: "a", Page: "mockups/a.html", Desktop: "a.png", Phone: "a.png"}}}}
	c.tasks[tk.ID] = tk

	req := httptest.NewRequest("GET", "/api/tasks/task_x/shot?name=../a.png", nil)
	req.SetPathValue("id", tk.ID)
	rec := httptest.NewRecorder()
	c.handleTaskShot(rec, req)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("serve shot: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}

	pick := func(page string) int {
		req := httptest.NewRequest("POST", "/api/tasks/task_x/pick", strings.NewReader(`{"page":"`+page+`"}`))
		req.SetPathValue("id", tk.ID)
		rec := httptest.NewRecorder()
		c.handlePickVariant(rec, req)
		return rec.Code
	}
	if code := pick("mockups/zzz.html"); code != 409 {
		t.Fatalf("unknown variant: %d", code)
	}
	if !tk.Mockup {
		t.Fatal("a rejected pick must not change the task")
	}
}

// The full check: render, show the shots, let the (stub) planner see them,
// apply its fix, render again, and stop when it says the page is right.
func TestVisualCheckFixesWhatItSees(t *testing.T) {
	bin := os.Getenv("OUTPOST_TEST_CHROME")
	if bin == "" {
		if m, _ := filepath.Glob("/opt/pw-browsers/chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell"); len(m) > 0 {
			bin = m[0]
		}
	}
	if bin == "" {
		t.Skip("no headless Chromium; set OUTPOST_TEST_CHROME")
	}
	var calls, images int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content []map[string]any `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		for _, p := range body.Messages[0].Content {
			if p["type"] == "image" {
				images++
			}
		}
		calls++
		text := "<<<OK>>>"
		if calls == 1 {
			text = "ISSUES: the heading is unreadable on the phone\n<<<FILE index.html>>>\n<html><body><h1>Fixed</h1></body></html>\n<<<END>>>"
		}
		json.NewEncoder(w).Encode(map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 2}})
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_ANTHROPIC_URL", srv.URL)

	c := newShotController(bin)
	c.dataDir = t.TempDir()
	enc, err := c.encrypt("test-key")
	if err != nil {
		t.Fatal(err)
	}
	c.settings.AnthropicKeyEnc = enc
	fakeExecNode(t, c, "n")
	tk := shotTask(t)
	tk.Status, tk.Prompt, tk.CapUSD, tk.ClaudeModel = "running", "a page with a heading", 5, "claude-sonnet-5-5"
	tk.Plan = &Plan{Title: "Page", Summary: "one page", PreviewPage: "index.html"}
	tk.writeFile("index.html", "<html><body><h1>Broken</h1></body></html>")
	tk.touch("index.html")
	c.visualCheck(context.Background(), tk)

	if calls != 2 {
		t.Fatalf("planner calls: %d (one fix, one OK); events %+v; messages %+v", calls, tk.Events, tk.Messages)
	}
	if images != 4 { // desktop and phone, twice
		t.Errorf("images sent: %d", images)
	}
	got, _ := os.ReadFile(filepath.Join(tk.repoDir(), "index.html"))
	if !strings.Contains(string(got), "Fixed") {
		t.Errorf("the fix was not written: %s", got)
	}
	kinds := map[string]int{}
	for _, m := range tk.Messages {
		kinds[m.Kind]++
	}
	if kinds["shots"] != 2 || kinds["event"] < 2 {
		t.Errorf("messages: %v", kinds)
	}
}
