package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func testController(t *testing.T) *Controller {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	allowLocalGit = true
	t.Cleanup(func() { allowLocalGit = false })
	return &Controller{dataDir: t.TempDir(), projects: map[string]*Project{}, tasks: map[string]*Task{}}
}

func TestGitExportRound(t *testing.T) {
	c := testController(t)
	// A bare remote with one commit on main.
	seed := t.TempDir()
	gitT(t, seed, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0o644)
	gitT(t, seed, "add", "-A")
	gitT(t, seed, "commit", "-q", "-m", "init")
	bare := filepath.Join(t.TempDir(), "remote.git")
	gitT(t, seed, "clone", "-q", "--bare", seed, bare)

	p := &Project{Schema: 1, ID: "proj_x", Name: "demo", GitURL: bare, GitBranch: "feature/x", GitPush: true, slot: make(chan struct{}, 1)}
	p.dir = filepath.Join(c.projectsDir(), p.ID)
	os.MkdirAll(p.dir, 0o700)
	if err := c.cloneProject(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if p.GitBase != "main" {
		t.Fatalf("base = %q", p.GitBase)
	}
	if got := listRepo(p.repoDir()); len(got) != 1 || got[0].Path != "README.md" {
		t.Fatalf("clone files: %+v (.git must be hidden)", got)
	}
	c.projects[p.ID] = p

	task := &Task{ID: "task_1", ProjectID: p.ID, Prompt: "Add a greeter\nwith tests", Round: 1, dir: t.TempDir(), repo: p.repoDir()}
	if err := writeRepoFile(task.repoDir(), "greet.py", "print('hi')\n"); err != nil {
		t.Fatal(err)
	}
	c.exportRound(task, p)

	if got := gitT(t, bare, "log", "-1", "--format=%s", "feature/x"); got != "Add a greeter" {
		t.Fatalf("pushed commit subject = %q", got)
	}
	if body := gitT(t, bare, "log", "-1", "--format=%B", "feature/x"); !strings.Contains(body, "with tests") || !strings.Contains(body, "Outpost task task_1, round 1") {
		t.Fatalf("commit body = %q", body)
	}
	if gitT(t, bare, "show", "feature/x:greet.py") == "" {
		t.Fatal("greet.py not pushed")
	}
	if p.LastPush == "" {
		t.Fatal("LastPush not recorded")
	}

	// A round that changes nothing makes no commit.
	before := gitT(t, bare, "rev-parse", "feature/x")
	c.exportRound(task, p)
	if gitT(t, bare, "rev-parse", "feature/x") != before {
		t.Fatal("empty round pushed a commit")
	}

	// Second round, pushed on top; a new clone of the same branch resumes from it.
	task.Round = 2
	task.Messages = []TaskMessage{{Round: 2, Role: "user", Kind: "text", Text: "Add a farewell"}}
	writeRepoFile(task.repoDir(), "bye.py", "print('bye')\n")
	c.exportRound(task, p)
	if got := gitT(t, bare, "log", "-1", "--format=%s", "feature/x"); got != "Add a farewell" {
		t.Fatalf("round 2 subject = %q", got)
	}
	if n := gitT(t, bare, "rev-list", "--count", "feature/x"); n != "3" {
		t.Fatalf("want 3 commits, got %s", n)
	}
	p2 := &Project{ID: "proj_y", GitURL: bare, GitBranch: "feature/x", slot: make(chan struct{}, 1)}
	p2.dir = filepath.Join(c.projectsDir(), p2.ID)
	os.MkdirAll(p2.dir, 0o700)
	if err := c.cloneProject(context.Background(), p2); err != nil {
		t.Fatal(err)
	}
	if len(listRepo(p2.repoDir())) != 3 {
		t.Fatalf("second clone should start from the pushed branch: %+v", listRepo(p2.repoDir()))
	}
}

func TestGitRejectsUnsafe(t *testing.T) {
	for _, u := range []string{"", "-oProxyCommand=x", "ext::sh -c id", "file:///etc", "/etc/passwd", "http://example.com/r.git", "https://a b"} {
		if validGitURL(u) {
			t.Errorf("%q should be rejected", u)
		}
	}
	for _, u := range []string{"https://github.com/o/r.git", "ssh://git@host/r.git", "git@github.com:o/r.git"} {
		if !validGitURL(u) {
			t.Errorf("%q should be accepted", u)
		}
	}
	for _, b := range []string{"", "-x", "a..b", "a b", "x.lock", "a//b", "/a"} {
		if validBranch(b) {
			t.Errorf("branch %q should be rejected", b)
		}
	}
	if !validBranch("outpost/feature-1") {
		t.Error("good branch rejected")
	}
	if err := writeRepoFile(t.TempDir(), ".git/hooks/pre-commit", "x"); err == nil {
		t.Error("write into .git allowed")
	}
	if r, ok := githubRepoOf("git@github.com:64fedoras/outpost.git"); !ok || r != "64fedoras/outpost" {
		t.Errorf("githubRepoOf = %q %v", r, ok)
	}
	if _, ok := githubRepoOf("https://gitlab.com/o/r"); ok {
		t.Error("gitlab is not github")
	}
}

func zipOf(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, c := range files {
		w, _ := zw.Create(n)
		w.Write([]byte(c))
	}
	zw.Close()
	return buf.Bytes()
}

func TestUnpackAttachedZip(t *testing.T) {
	dest := t.TempDir()
	n, err := unpackAttachedZip(zipOf(t, map[string]string{"proj-main/a.go": "a", "proj-main/sub/b.go": "b", "proj-main/.git/config": "evil", "__MACOSX/x": "junk"}), dest)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "sub", "b.go")); err != nil {
		t.Fatal("top folder not stripped:", err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); err == nil {
		t.Fatal(".git extracted")
	}
	if _, err := unpackAttachedZip(zipOf(t, map[string]string{"../evil": "x", "ok": "y"}), t.TempDir()); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestCreateTaskWithAttachments(t *testing.T) {
	c := testController(t)
	c.settings = defaultSettings()
	c.settings.DefaultPlanner = "node:none"
	task := &Task{ID: "task_a", dir: t.TempDir()}
	os.MkdirAll(task.repoDir(), 0o755)
	err := c.applyAttachments(task, []attachedFile{
		{Name: "start.zip", Data: zipOf(t, map[string]string{"main.py": "print(1)\n"})},
		{Name: "notes.txt", Data: []byte("use sqlite")},
	})
	if err != nil || !task.Seeded || len(task.Attachments) != 2 {
		t.Fatalf("err=%v seeded=%v att=%+v", err, task.Seeded, task.Attachments)
	}
	if ctx := attachmentContext(task, 10000); !strings.Contains(ctx, "use sqlite") || strings.Contains(ctx, "main.py") {
		t.Fatalf("context = %q", ctx)
	}
	if err := c.applyAttachments(&Task{dir: t.TempDir()}, []attachedFile{{Name: "pic.png", Data: []byte{0x89, 'P', 0, 1}}}); err == nil {
		t.Fatal("binary file accepted")
	}
}

func TestProjectQueueAndMigration(t *testing.T) {
	c := testController(t)
	// An old task (no project_id in its JSON) loads unfiled and keeps its own repo.
	old := filepath.Join(c.tasksDir(), "task_old")
	os.MkdirAll(filepath.Join(old, "repo"), 0o755)
	os.WriteFile(filepath.Join(old, "task.json"), []byte(`{"id":"task_old","prompt":"x","status":"done","planner":"claude","subtasks":[],"events":[],"messages":[]}`), 0o644)
	// A task whose project vanished becomes unfiled.
	gone := filepath.Join(c.tasksDir(), "task_gone")
	os.MkdirAll(gone, 0o755)
	os.WriteFile(filepath.Join(gone, "task.json"), []byte(`{"id":"task_gone","prompt":"x","status":"done","project_id":"proj_missing","subtasks":[],"events":[],"messages":[]}`), 0o644)
	c.loadProjects()
	c.loadTasks()
	for _, id := range []string{"task_old", "task_gone"} {
		tk := c.tasks[id]
		if tk == nil || tk.ProjectID != "" || tk.repoDir() != filepath.Join(c.tasksDir(), id, "repo") {
			t.Fatalf("%s: %+v", id, tk)
		}
	}

	// Project persistence and the one-at-a-time slot.
	p := &Project{Schema: 1, ID: "proj_q", Name: "q", Created: time.Now(), slot: make(chan struct{}, 1)}
	p.dir = filepath.Join(c.projectsDir(), p.ID)
	os.MkdirAll(p.repoDir(), 0o755)
	if err := p.save(); err != nil {
		t.Fatal(err)
	}
	c.loadProjects()
	if c.projects["proj_q"] == nil {
		t.Fatal("project not reloaded")
	}
	p = c.projects["proj_q"]
	p.slot <- struct{}{} // another task is executing
	tk := &Task{ID: "task_q", ProjectID: p.ID, dir: t.TempDir(), repo: p.repoDir(), Status: "running", Plan: nil}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.execTask(ctx, tk); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		tk.mu.Lock()
		s := tk.Status
		tk.mu.Unlock()
		if s == "queued" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task never queued, status %s", s)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled queued task did not return")
	}
}

func TestProjectHTTP(t *testing.T) {
	c := testController(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"plain","test_command":"go test"}`))
	c.handleCreateProject(w, r)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if v := c.projectViews(); len(v) != 1 || v[0].TestCommand != "go test" || v[0].GitURL != "" {
		t.Fatalf("views: %+v", v)
	}
	w = httptest.NewRecorder()
	c.handleCreateProject(w, httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"bad","git_url":"file:///etc"}`)))
	allowLocalGit = false
	w = httptest.NewRecorder()
	c.handleCreateProject(w, httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"bad","git_url":"file:///etc"}`)))
	if w.Code != 400 || len(c.projects) != 1 {
		t.Fatalf("local git URL accepted: %d %s", w.Code, w.Body)
	}
}

// The project menu renames, pins and edits instructions through POST /api/projects/{id};
// fields left out of the request keep their value.
func TestProjectPinRename(t *testing.T) {
	c := testController(t)
	w := httptest.NewRecorder()
	c.handleCreateProject(w, httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"first","test_command":"go test"}`)))
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	id := c.projectViews()[0].ID
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{id}", c.handleUpdateProject)
	post := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/projects/"+id, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("update %s: %d %s", body, w.Code, w.Body)
		}
	}
	post(`{"pinned":true}`)
	v := c.projectViews()[0]
	if !v.Pinned || v.Name != "first" || v.TestCommand != "go test" {
		t.Fatalf("after pin: %+v", v)
	}
	post(`{"name":"  second  ","instructions":"use pytest"}`)
	v = c.projectViews()[0]
	if !v.Pinned || v.Name != "second" || v.Instr != "use pytest" {
		t.Fatalf("after rename: %+v", v)
	}
	post(`{"name":"","pinned":false}`)
	v = c.projectViews()[0]
	if v.Pinned || v.Name != "second" {
		t.Fatalf("after unpin: %+v", v)
	}
	// the pin survives a restart
	post(`{"pinned":true}`)
	c.loadProjects()
	if v := c.projectViews(); len(v) != 1 || !v[0].Pinned || v[0].Name != "second" {
		t.Fatalf("after reload: %+v", v)
	}
}

// Duplicate copies files, settings and instructions; not tasks, chat, pin or push settings.
func TestProjectDuplicate(t *testing.T) {
	c := testController(t)
	w := httptest.NewRecorder()
	c.handleCreateProject(w, httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"Dice","test_command":"pytest"}`)))
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	src := c.projectViews()[0].ID
	p := c.getProject(src)
	p.Instructions, p.Pinned, p.FixRounds = "use pytest", true, 5
	p.Suggestions = []ProjectSuggestion{{ID: "sg_1", Text: "x"}}
	os.MkdirAll(filepath.Join(p.repoDir(), "pkg"), 0o755)
	os.WriteFile(filepath.Join(p.repoDir(), "pkg", "main.py"), []byte("print(1)\n"), 0o644)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{id}/duplicate", c.handleDuplicateProject)
	dup := func() (int, map[string]string) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/projects/"+src+"/duplicate", nil))
		var out map[string]string
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, out := dup()
	if code != 200 || out["name"] != "Dice (copy)" {
		t.Fatalf("duplicate: %d %v", code, out)
	}
	cp := c.getProject(out["id"])
	if cp == nil || cp.Instructions != "use pytest" || cp.TestCommand != "pytest" || cp.FixRounds != 5 || cp.Pinned || len(cp.Suggestions) != 0 || cp.GitPush || cp.GitPR {
		t.Fatalf("copy: %+v", cp)
	}
	if b, err := os.ReadFile(filepath.Join(cp.repoDir(), "pkg", "main.py")); err != nil || string(b) != "print(1)\n" {
		t.Fatalf("copied file: %q %v", b, err)
	}
	// a task working in the project holds its slot: the copy is refused, not half-made
	p.slot <- struct{}{}
	if code, _ := dup(); code != 409 {
		t.Fatalf("busy project duplicated: %d", code)
	}
	<-p.slot
	if n := len(c.projectViews()); n != 2 {
		t.Fatalf("projects: %d", n)
	}
	// detach: no .git, no git settings; the files stay
	p.GitURL, p.GitBranch, p.GitTokenEnc = "https://example.local/a.git", "outpost", "enc"
	os.MkdirAll(filepath.Join(p.repoDir(), ".git"), 0o755)
	os.WriteFile(filepath.Join(p.repoDir(), ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	for _, detach := range []bool{false, true} {
		w := httptest.NewRecorder()
		body := `{}`
		if detach {
			body = `{"detach":true}`
		}
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/projects/"+src+"/duplicate", strings.NewReader(body)))
		var o map[string]string
		json.Unmarshal(w.Body.Bytes(), &o)
		d := c.getProject(o["id"])
		if w.Code != 200 || d == nil {
			t.Fatalf("duplicate detach=%v: %d %s", detach, w.Code, w.Body)
		}
		_, gitErr := os.Stat(filepath.Join(d.repoDir(), ".git", "HEAD"))
		_, fileErr := os.Stat(filepath.Join(d.repoDir(), "pkg", "main.py"))
		if fileErr != nil || (gitErr == nil) == detach || (d.GitURL == "") != detach || (d.GitTokenEnc == "") != detach {
			t.Fatalf("detach=%v: git=%v file=%v %+v", detach, gitErr, fileErr, d)
		}
	}
	for in, want := range map[string]string{"A": "A (copy)", "A (copy)": "A (copy 2)", "A (copy 2)": "A (copy 3)", "A (copy x)": "A (copy x) (copy)"} {
		if got := copyName(in); got != want {
			t.Errorf("copyName(%q) = %q, want %q", in, got, want)
		}
	}
}
