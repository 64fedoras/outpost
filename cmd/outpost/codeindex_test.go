package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, c := range files {
		if err := writeRepoFile(dir, rel, c); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestIgnoreSet(t *testing.T) {
	s := &ignoreSet{}
	for _, l := range []string{"# c", "vendor/", "*.log", "/dist", "docs/*.md", "!keep.log", "**/gen/"} {
		s.add(l)
	}
	for rel, want := range map[string]bool{
		"vendor/x/y.go": true, "a/vendor/y.go": true, "a.log": true, "keep.log": false,
		"dist/app": true, "sub/dist/app": false, "docs/a.md": true, "docs/x/a.md": false,
		"a/gen/z.go": true, "main.go": false, "vendorx.go": false,
	} {
		if got := s.ignored(rel); got != want {
			t.Errorf("ignored(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestDeclarations(t *testing.T) {
	g := declarations("a.go", []byte("package a\n\n// Doc\nfunc (s *S) Run(x int) (string, error) {\n\treturn \"\", nil\n}\ntype S struct{ A int }\ntype I interface{ M() }\nvar Exported = 1\nvar hidden = 2\n"))
	joined := strings.Join(g, "|")
	for _, want := range []string{"func (s *S) Run(x int) (string, error)", "type S struct", "type I interface", "var Exported"} {
		if !strings.Contains(joined, want) {
			t.Errorf("go decls %q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "hidden") || strings.Contains(joined, "return") {
		t.Errorf("go decls leak bodies or unexported vars: %q", joined)
	}
	js := strings.Join(declarations("a.js", []byte("export async function load(a, b) {\n}\nclass Foo extends Bar {\n}\nconst f = (x) => x\n  const inner = () => 1\n")), "|")
	if !strings.Contains(js, "function load(a, b)") || !strings.Contains(js, "class Foo extends Bar") || !strings.Contains(js, "const f = (x) =>") || strings.Contains(js, "inner") {
		t.Errorf("js decls: %q", js)
	}
	py := strings.Join(declarations("a.py", []byte("def f(a):\n    pass\nclass K(Base):\n    def m(self): pass\n")), "|")
	if !strings.Contains(py, "def f(a)") || !strings.Contains(py, "class K(Base)") || strings.Contains(py, "def m") {
		t.Errorf("py decls: %q", py)
	}
	css := strings.Join(declarations("a.css", []byte(".a, .b {\n  color: red;\n}\n@media (x) {\n}\n")), "|")
	if css != ".a, .b" {
		t.Errorf("css decls: %q", css)
	}
	if got := declarations("broken.go", []byte("package a\nfunc (")); len(got) != 0 {
		t.Errorf("broken go: %v", got)
	}
}

func TestIndexIgnoreAndRender(t *testing.T) {
	dir := writeTree(t, map[string]string{
		".outpostignore": "vendor/\n",
		"main.go":        "// Entry point.\npackage main\nfunc main() {}\n",
		"vendor/dep.go":  "package dep\nfunc Dep() {}\n",
		"bin.dat":        "a\x00b",
	})
	idx := buildIndex(dir)
	var paths []string
	for _, e := range idx {
		paths = append(paths, e.Path)
	}
	if got := strings.Join(paths, ","); got != ".outpostignore,main.go" {
		t.Fatalf("index paths = %s", got)
	}
	out := renderIndex(idx, 10000)
	if !strings.Contains(out, "main.go") || !strings.Contains(out, "Entry point.") || !strings.Contains(out, "func main()") || strings.Contains(out, "vendor") {
		t.Errorf("render:\n%s", out)
	}
	// vendor files still exist for builds
	if _, err := os.Stat(filepath.Join(dir, "vendor", "dep.go")); err != nil {
		t.Fatal(err)
	}
	if small := renderIndex(idx, 40); !strings.Contains(small, "more files not listed") {
		t.Errorf("small render: %q", small)
	}
}

func TestPlannerPromptUsesIndexForLargeRepo(t *testing.T) {
	big := strings.Repeat("x := 1\n", 3000)
	dir := writeTree(t, map[string]string{
		"a.go": "package a\nfunc A() {}\n" + big,
		"b.go": "package b\nfunc B() {}\n",
	})
	task := &Task{Round: 2, dir: filepath.Dir(dir)}
	// repoDir is <dir>/repo, so build the tree there instead
	repo := task.repoDir()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.go", "b.go"} {
		data, _ := os.ReadFile(filepath.Join(dir, n))
		_ = os.WriteFile(filepath.Join(repo, n), data, 0o644)
	}
	c := &Controller{}
	small := c.plannerPrompt(task, 100000, nil)
	if strings.Contains(small, "Project index") || !strings.Contains(small, "<<<FILE a.go>>>") {
		t.Errorf("small budget should paste files whole:\n%.300s", small)
	}
	large := c.plannerPrompt(task, 20000, []string{"b.go", "../x", "missing.go"})
	if !strings.Contains(large, "Project index") || !strings.Contains(large, "func A()") {
		t.Errorf("index missing:\n%.400s", large)
	}
	if strings.Contains(large, "x := 1") || !strings.Contains(large, "<<<FILE b.go>>>") {
		t.Errorf("only asked-for files should be pasted")
	}
}

func TestDeclsTextFallback(t *testing.T) {
	dir := writeTree(t, map[string]string{"notes.txt": "hello", "a.go": "package a\nfunc A() {}\n"})
	if got := declsText(dir, "notes.txt"); got != "hello" {
		t.Errorf("fallback = %q", got)
	}
	if got := declsText(dir, "a.go"); !strings.Contains(got, "func A()") || strings.Contains(got, "package") {
		t.Errorf("go = %q", got)
	}
}
