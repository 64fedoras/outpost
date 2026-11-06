package main

import (
	"archive/zip"
	"bytes"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// applyPatch applies one file's unified diff to before (enough for the tests).
func applyPatch(t *testing.T, before, patch string) string {
	t.Helper()
	a := splitLines(before)
	var out []string
	pos := 0
	lines := strings.SplitAfter(patch, "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if !strings.HasPrefix(l, "@@ ") {
			continue
		}
		var sa, na, sb, nb int
		if _, err := fmtSscanf(l, &sa, &na, &sb, &nb); err != nil {
			t.Fatalf("bad hunk %q", l)
		}
		start := sa - 1
		if na == 0 {
			start = sa
		}
		out = append(out, a[pos:start]...)
		pos = start
		for i+1 < len(lines) && len(lines[i+1]) > 0 && strings.ContainsRune(" +-\\", rune(lines[i+1][0])) {
			i++
			hl := lines[i]
			if hl[0] == '\\' {
				out[len(out)-1] = strings.TrimSuffix(out[len(out)-1], "\n")
				continue
			}
			body := hl[1:]
			switch hl[0] {
			case ' ':
				if a[pos] != body && a[pos] != strings.TrimSuffix(body, "\n") {
					t.Fatalf("context mismatch at %d: %q vs %q", pos, a[pos], body)
				}
				out = append(out, body)
				pos++
			case '-':
				pos++
			case '+':
				out = append(out, body)
			}
		}
	}
	out = append(out, a[pos:]...)
	return strings.Join(out, "")
}

func fmtSscanf(l string, sa, na, sb, nb *int) (int, error) {
	f := strings.Fields(l) // @@ -a,b +c,d @@
	parse := func(s string, x, y *int) error {
		p := strings.SplitN(s[1:], ",", 2)
		var err error
		if *x, err = strconv.Atoi(p[0]); err != nil {
			return err
		}
		*y = 1
		if len(p) == 2 {
			*y, err = strconv.Atoi(p[1])
		}
		return err
	}
	if err := parse(f[1], sa, na); err != nil {
		return 0, err
	}
	return 4, parse(f[2], sb, nb)
}

func TestUnifiedDiffRoundTrip(t *testing.T) {
	cases := [][2]string{
		{"a\nb\nc\n", "a\nB\nc\n"},
		{"", "new\nfile\n"},
		{"one\ntwo\n", "one\ntwo"},
		{"x\n", "x\ny\n"},
		{"1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n", "0\n1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n"},
	}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 200; i++ {
		var a, b strings.Builder
		for j := r.Intn(30); j > 0; j-- {
			a.WriteString(strconv.Itoa(r.Intn(6)) + "\n")
		}
		for j := r.Intn(30); j > 0; j-- {
			b.WriteString(strconv.Itoa(r.Intn(6)) + "\n")
		}
		cases = append(cases, [2]string{a.String(), b.String()})
	}
	for _, c := range cases {
		d := unifiedDiff("f.txt", c[0], c[1], true, true)
		if c[0] == c[1] {
			if d != "" {
				t.Fatalf("diff for equal content: %q", d)
			}
			continue
		}
		if got := applyPatch(t, c[0], d); got != c[1] {
			t.Fatalf("round trip failed\nbefore %q\nafter %q\ngot %q\npatch:\n%s", c[0], c[1], got, d)
		}
	}
	// Edit scripts are minimal for a simple change.
	ops := myers(splitLines("a\nb\nc\n"), splitLines("a\nx\nc\n"))
	changes := 0
	for _, o := range ops {
		if o.kind != ' ' {
			changes++
		}
	}
	if changes != 2 {
		t.Fatalf("%d changes, want 2: %+v", changes, ops)
	}
	if d := unifiedDiff("gone.txt", "bye\n", "", true, false); !strings.Contains(d, "deleted file mode") || !strings.Contains(d, "+++ /dev/null") {
		t.Fatalf("delete diff: %s", d)
	}
	if d := unifiedDiff("new.txt", "", "hi\n", false, true); !strings.Contains(d, "new file mode") || !strings.Contains(d, "--- /dev/null") || !strings.Contains(d, "@@ -0,0 +1,1 @@") {
		t.Fatalf("add diff: %s", d)
	}
}

// handoffTask builds a task with two rounds: round 1 adds main.go and edits
// README.md; round 2 edits main.go again and deletes old.txt.
func handoffTask(t *testing.T, c *Controller) *Task {
	dir := t.TempDir()
	tk := &Task{ID: "task_h", Title: "Greeter tool", Status: "done", Round: 1, dir: dir, TestStatus: "failing", FixRounds: 3,
		TestOutput: "FAIL: TestGreet", Plan: &Plan{Summary: "Write a greeter.", TestCommand: "go test ./...", Language: "Go",
			Subtasks: []PlanSubtask{{ID: "t1", Title: "Greeter", Files: []string{"main.go"}}}},
		Messages: []TaskMessage{{Role: "user", Kind: "text", Text: "Make a greeter", Round: 1}, {Role: "user", Kind: "text", Text: "Now say goodbye too", Round: 2},
			{Role: "assistant", Kind: "result", Round: 2, Files: []string{"main.go", "old.txt"}}}}
	repo := tk.repoDir()
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref"), 0o644)
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("# demo\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "old.txt"), []byte("old\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("Use tabs.\n"), 0o644)
	if err := tk.writeFile("main.go", "package main\n\nfunc main() {}\n"); err != nil {
		t.Fatal(err)
	}
	if err := tk.writeFile("README.md", "# demo\n\nA greeter.\n"); err != nil {
		t.Fatal(err)
	}
	tk.Round = 2
	if err := tk.writeFile("main.go", "package main\n\nfunc main() { bye() }\n"); err != nil {
		t.Fatal(err)
	}
	if err := (&Controller{}).applyFileOps(tk, &Plan{Delete: []string{"old.txt"}}); err != nil {
		t.Fatal(err)
	}
	c.tasks[tk.ID] = tk
	return tk
}

func TestTaskPatch(t *testing.T) {
	c := &Controller{tasks: map[string]*Task{}}
	tk := handoffTask(t, c)
	all, n := tk.taskPatch(0)
	if n != 3 {
		t.Fatalf("%d files in the full patch, want 3:\n%s", n, all)
	}
	for _, want := range []string{"new file mode 100644\n--- /dev/null\n+++ b/main.go", "+func main() { bye() }", "+A greeter.", "deleted file mode", "-old"} {
		if !strings.Contains(all, want) {
			t.Errorf("full patch lacks %q:\n%s", want, all)
		}
	}
	r1, n1 := tk.taskPatch(1)
	if n1 != 2 || !strings.Contains(r1, "+func main() {}") || strings.Contains(r1, "bye") {
		t.Fatalf("round 1 patch must end at round 1's state (%d files):\n%s", n1, r1)
	}
	r2, n2 := tk.taskPatch(2)
	if n2 != 2 || !strings.Contains(r2, "-func main() {}\n+func main() { bye() }") || strings.Contains(r2, "README") {
		t.Fatalf("round 2 patch (%d files):\n%s", n2, r2)
	}
	// git itself accepts the patch.
	if _, err := exec.LookPath("git"); err == nil {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "README.md"), []byte("# demo\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "old.txt"), []byte("old\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "p.patch"), []byte(all), 0o644)
		cmd := exec.Command("git", "apply", "p.patch")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git apply: %v\n%s", err, out)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "main.go")); string(b) != "package main\n\nfunc main() { bye() }\n" {
			t.Fatalf("applied main.go = %q", b)
		}
		if _, err := os.Stat(filepath.Join(dir, "old.txt")); !os.IsNotExist(err) {
			t.Fatal("old.txt not deleted by the patch")
		}
	}
}

func TestTaskHandoffZip(t *testing.T) {
	c := &Controller{tasks: map[string]*Task{}}
	tk := handoffTask(t, c)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tasks/{id}/handoff", c.handleTaskHandoff)
	mux.HandleFunc("GET /api/tasks/{id}/patch", c.handleTaskPatch)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/tasks/task_h/handoff", nil))
	if rec.Code != 200 {
		t.Fatalf("handoff: %d %s", rec.Code, rec.Body)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[strings.TrimPrefix(f.Name, "greeter-tool-handoff/")] = string(b)
	}
	for _, name := range []string{"main.go", "README.md", "HANDOFF.md", "outpost-changes.patch", "AGENTS.md", "CLAUDE.md", "GEMINI.md", ".github/copilot-instructions.md", ".cursor/rules/outpost-handoff.mdc"} {
		if _, ok := files[name]; !ok {
			t.Errorf("zip lacks %s (has %v)", name, len(files))
		}
	}
	for name := range files {
		if strings.HasPrefix(name, ".git/") {
			t.Errorf("zip includes %s", name)
		}
	}
	if !strings.HasPrefix(files["AGENTS.md"], "Use tabs.\n\n## Outpost hand-off") {
		t.Errorf("existing AGENTS.md not kept: %q", files["AGENTS.md"])
	}
	if !strings.HasPrefix(files[".cursor/rules/outpost-handoff.mdc"], "---\n") || !strings.Contains(files[".cursor/rules/outpost-handoff.mdc"], "alwaysApply: true") {
		t.Errorf("cursor rule: %q", files[".cursor/rules/outpost-handoff.mdc"])
	}
	h := files["HANDOFF.md"]
	for _, want := range []string{"# Hand-off: Greeter tool", "**still fail**", "`go test ./...`", "> Make a greeter", "> Now say goodbye too", "FAIL: TestGreet", "Request 2 (Outpost): main.go, old.txt", "**Greeter**"} {
		if !strings.Contains(h, want) {
			t.Errorf("HANDOFF.md lacks %q:\n%s", want, h)
		}
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/tasks/task_h/patch?round=2", nil))
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), "greeter-tool-request-2.patch") {
		t.Fatalf("patch: %d %v", rec.Code, rec.Header())
	}
	// A running task cannot be handed off.
	tk.mu.Lock()
	tk.Status = "running"
	tk.mu.Unlock()
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/tasks/task_h/handoff", nil))
	if rec.Code != 409 {
		t.Fatalf("running task: %d", rec.Code)
	}
}
