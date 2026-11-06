package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTaskLimits(t *testing.T) {
	if clampFixRounds(-1) != 0 || clampFixRounds(5) != 5 || clampFixRounds(99) != 8 {
		t.Fatal("clampFixRounds")
	}
	if clampTestMinutes(0) != 0 || clampTestMinutes(30) != 30 || clampTestMinutes(1000) != 120 {
		t.Fatal("clampTestMinutes")
	}
	tk := &Task{}
	if tk.fixLimit() != maxFixRounds || tk.testLimit() != testTimeout {
		t.Fatal("defaults")
	}
	tk.MaxFix, tk.TestMinutes = 6, 25
	if tk.fixLimit() != 6 || tk.testLimit() != 25*time.Minute {
		t.Fatal("task limits not used")
	}
	p := &Project{FixRounds: 2, TestMinutes: 40}
	req := p.applyDefaults(createReq{})
	if req.FixRounds != 2 || req.TestMinutes != 40 {
		t.Fatalf("project defaults: %+v", req)
	}
	if req := p.applyDefaults(createReq{FixRounds: 5}); req.FixRounds != 5 {
		t.Fatal("a request's own value must win")
	}
}

func TestPlanSchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal(planSchema, &v); err != nil {
		t.Fatal(err)
	}
	if !parseFailed(&parseError{os.ErrInvalid}) || parseFailed(os.ErrInvalid) {
		t.Fatal("parseFailed")
	}
}

func TestLineDeltaAndRoundStats(t *testing.T) {
	if a, r := lineDelta("a\nb\nc\n", "a\nx\nc\ny\n"); a != 2 || r != 1 {
		t.Fatalf("lineDelta = +%d -%d", a, r)
	}
	if a, r := lineDelta("", "one\ntwo"); a != 2 || r != 0 {
		t.Fatalf("new file = +%d -%d", a, r)
	}
	tk := &Task{ID: "t", Round: 1, dir: t.TempDir(), FixRounds: 1,
		Subtasks: []*SubtaskState{{ID: "t1", NodeName: "beta", Attempts: 2}, {ID: "t2", NodeName: "alpha", Attempts: 1}, {ID: "t3", ByPlanner: true}}}
	approved := time.Now().Add(-2 * time.Minute)
	tk.Approved = &approved
	os.MkdirAll(tk.repoDir(), 0o755)
	os.WriteFile(filepath.Join(tk.repoDir(), "a.txt"), []byte("1\n2\n"), 0o644)
	tk.writeFile("a.txt", "1\n3\n4\n")
	tk.writeFile("b.txt", "new\n")
	st := (&Controller{}).roundStats(tk)
	if st.Files != 2 || st.Added != 3 || st.Removed != 1 || st.Retried != 1 || st.FixRounds != 1 || st.Seconds < 100 {
		t.Fatalf("stats %+v", st)
	}
	if strings.Join(st.Nodes, ",") != "alpha,beta,planner" {
		t.Fatalf("nodes %v", st.Nodes)
	}
}

func TestPreferredNode(t *testing.T) {
	c := &Controller{}
	tk := &Task{dir: t.TempDir()}
	st := &SubtaskState{ID: "t1", Prefer: "n2"}
	free := []nodeScore{{ID: "n1"}, {ID: "n2"}}
	if got := c.preferredNode(tk, st, free, 0); got != "n2" {
		t.Fatalf("got %q", got)
	}
	if got := c.preferredNode(tk, st, free[:1], time.Minute); got != "" {
		t.Fatalf("must wait for n2, got %q", got)
	}
	if got := c.preferredNode(tk, st, free[:1], preferWait+time.Second); got != "n1" || st.Prefer != "" {
		t.Fatalf("after the wait: %q prefer=%q", got, st.Prefer)
	}
}

func TestSuggestFromFailure(t *testing.T) {
	answer := "Use the testing package only; do not add testify.\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.Marshal(answer)
		w.Write([]byte(`{"content":[{"type":"text","text":` + string(b) + `}],"usage":{"input_tokens":100,"output_tokens":20}}`))
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_ANTHROPIC_URL", srv.URL)
	c := &Controller{dataDir: t.TempDir(), projects: map[string]*Project{}, tasks: map[string]*Task{}}
	c.settings = defaultSettings()
	enc, _ := c.encrypt("sk-test")
	c.settings.AnthropicKeyEnc = enc
	p := &Project{ID: "proj_1", Name: "demo", dir: t.TempDir()}
	c.projects[p.ID] = p
	tk := &Task{ID: "task_1", ProjectID: p.ID, Planner: "claude", ClaudeModel: "claude-sonnet-5-5", CapUSD: 5, dir: t.TempDir(), TestStatus: "failing",
		TestOutput: "cannot find package testify", Messages: []TaskMessage{{Role: "user", Kind: "text", Text: "Add a parser"}}}
	c.suggestFromFailure(tk)
	if len(p.Suggestions) != 1 || p.Suggestions[0].Text != strings.TrimSpace(answer) || p.Suggestions[0].TaskID != "task_1" {
		t.Fatalf("suggestions %+v", p.Suggestions)
	}
	c.suggestFromFailure(tk) // the same rule is not added twice
	if len(p.Suggestions) != 1 {
		t.Fatalf("duplicate: %+v", p.Suggestions)
	}
	// Over the cap: no call.
	tk.CostUSD = 5
	answer = "Another rule."
	c.suggestFromFailure(tk)
	if len(p.Suggestions) != 1 {
		t.Fatal("called over the cap")
	}
	tk.CostUSD = 0
	answer = "NONE"
	c.suggestFromFailure(tk)
	if len(p.Suggestions) != 1 {
		t.Fatal("NONE added a suggestion")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{id}/suggestions/{sid}/{act}", c.handleProjectSuggestion)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/projects/proj_1/suggestions/"+p.Suggestions[0].ID+"/add", nil))
	if rec.Code != 200 || len(p.Suggestions) != 0 || !strings.Contains(p.Instructions, "testify") {
		t.Fatalf("add: %d %q %+v", rec.Code, p.Instructions, p.Suggestions)
	}
	if b, _ := os.ReadFile(filepath.Join(p.dir, "project.json")); !strings.Contains(string(b), "testify") {
		t.Fatal("not saved")
	}
	p.addSuggestion(ProjectSuggestion{ID: "sg_2", Text: "Keep files small."})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/projects/proj_1/suggestions/sg_2/dismiss", nil))
	if rec.Code != 200 || len(p.Suggestions) != 0 || strings.Contains(p.Instructions, "small") {
		t.Fatalf("dismiss: %d %q", rec.Code, p.Instructions)
	}
	if cleanSuggestion("- `Run gofmt.`\nmore") != "Run gofmt." || cleanSuggestion("none.") != "" {
		t.Fatal("cleanSuggestion")
	}
}

func TestHandEdit(t *testing.T) {
	c := &Controller{tasks: map[string]*Task{}}
	tk := &Task{ID: "task_e", Status: "done", Round: 1, dir: t.TempDir()}
	os.MkdirAll(tk.repoDir(), 0o755)
	os.WriteFile(filepath.Join(tk.repoDir(), "a.go"), []byte("old\n"), 0o644)
	os.WriteFile(filepath.Join(tk.dir, "result.zip"), []byte("zip"), 0o644)
	c.tasks[tk.ID] = tk
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/tasks/{id}/file", c.handleSaveTaskFile)
	save := func(path, content string) int {
		b, _ := json.Marshal(map[string]string{"path": path, "content": content})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/tasks/task_e/file", strings.NewReader(string(b))))
		return rec.Code
	}
	if code := save("a.go", "new\n"); code != 200 {
		t.Fatalf("save: %d", code)
	}
	if code := save("b.go", "added\n"); code != 200 {
		t.Fatalf("save 2: %d", code)
	}
	if tk.Round != 2 || len(tk.Messages) != 1 || tk.Messages[0].Kind != "hand_edit" || len(tk.Messages[0].Files) != 2 {
		t.Fatalf("round %d, messages %+v", tk.Round, tk.Messages)
	}
	m := readManifest(tk.roundDir(2))
	if m.Files["a.go"] != "modified" || m.Files["b.go"] != "added" {
		t.Fatalf("manifest %+v", m.Files)
	}
	if _, err := os.Stat(filepath.Join(tk.dir, "result.zip")); !os.IsNotExist(err) {
		t.Fatal("stale result.zip kept")
	}
	if p, _ := tk.taskPatch(2); !strings.Contains(p, "-old\n+new") {
		t.Fatalf("patch of the edit:\n%s", p)
	}
	for _, bad := range []string{"../x", ".git/config", ""} {
		if code := save(bad, "x"); code != 400 {
			t.Errorf("%q: %d", bad, code)
		}
	}
	tk.Status = "running"
	if code := save("a.go", "x"); code != 409 {
		t.Fatalf("running task: %d", code)
	}
}
