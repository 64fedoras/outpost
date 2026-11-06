package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{{"0.2.10", "0.2.9", 1}, {"0.3.0", "0.3.0", 0}, {"v1.0", "1.0.0", 0}, {"0.2.1", "0.10.0", -1}, {"0.8.1", "0.8.0", 1}, {"0.8.10", "0.8.9", 1}, {"0.9.0", "0.8.12", 1}, {"0.10.0", "0.9.0", 1}, {"0.10.0", "0.9.12", 1}}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseFileBlocks(t *testing.T) {
	text := "Sure!\n<<<FILE a/b.py>>>\n```python\nprint(1)\n```\n<<<END>>>\nnoise\n<<<FILE ./c.txt>>>\nhello<<<END>>>\n<<<FILE d.go>>>\npackage d\n"
	blocks := parseFileBlocks(text)
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks", len(blocks))
	}
	if blocks[0].Path != "a/b.py" || blocks[0].Content != "print(1)\n" {
		t.Errorf("fence not stripped: %q", blocks[0].Content)
	}
	if blocks[1].Content != "hello\n" {
		t.Errorf("block 2: %q", blocks[1].Content)
	}
	if blocks[2].Path != "d.go" || !strings.HasPrefix(blocks[2].Content, "package d") {
		t.Errorf("truncated last block: %+v", blocks[2])
	}
}

func TestValidatePlan(t *testing.T) {
	ok := &Plan{Subtasks: []PlanSubtask{{ID: "t1", Files: []string{"a.py"}}, {ID: "t2", Files: []string{"./b.py"}, DependsOn: []string{"t1"}}}}
	if err := validatePlan(ok); err != nil {
		t.Fatal(err)
	}
	if ok.Subtasks[1].Files[0] != "b.py" {
		t.Error("./ prefix not normalized")
	}
	bad := []*Plan{
		{Subtasks: []PlanSubtask{{ID: "t1", Files: []string{"../x"}}}},                                                                                      // escapes
		{Subtasks: []PlanSubtask{{ID: "t1", Files: []string{"a"}, DependsOn: []string{"t2"}}, {ID: "t2", Files: []string{"b"}, DependsOn: []string{"t1"}}}}, // cycle
		{Subtasks: []PlanSubtask{{ID: "t1", Files: []string{"a"}, DependsOn: []string{"nope"}}}},                                                            // unknown dep
		{},
	}
	for i, p := range bad {
		if validatePlan(p) == nil {
			t.Errorf("bad plan %d accepted", i)
		}
	}
}

// A planner that gives one file to two subtasks gets them merged, not rejected.
func TestValidatePlanMergesSharedFile(t *testing.T) {
	p := &Plan{Subtasks: []PlanSubtask{
		{ID: "t1", Title: "Markup", Files: []string{"index.html"}, Instructions: "page"},
		{ID: "t2", Title: "Script", Files: []string{"./index.html", "notes.md"}, Instructions: "dice logic"},
		{ID: "t3", Files: []string{"README.md"}, DependsOn: []string{"t2"}},
	}}
	mergeSharedFiles(p)
	if err := validatePlan(p); err != nil {
		t.Fatal(err)
	}
	if len(p.Subtasks) != 2 {
		t.Fatalf("want 2 subtasks, got %d", len(p.Subtasks))
	}
	a := p.Subtasks[0]
	if a.ID != "t1" || len(a.Files) != 2 || !strings.Contains(a.Instructions, "dice logic") {
		t.Errorf("merged subtask: %+v", a)
	}
	if d := p.Subtasks[1].DependsOn; len(d) != 1 || d[0] != "t1" {
		t.Errorf("dependency not redirected: %v", d)
	}
	raw := &Plan{Subtasks: []PlanSubtask{{ID: "t1", Files: []string{"a.py"}}, {ID: "t2", Files: []string{"a.py"}}}}
	if validatePlan(raw) == nil {
		t.Error("validatePlan itself must still reject a shared file (user-edited plans)")
	}
	dup := &Plan{Subtasks: []PlanSubtask{{ID: "t1", Files: []string{"a", "a"}}}}
	if err := validatePlan(dup); err != nil {
		t.Errorf("same file twice in one subtask: %v", err)
	}
}

func TestNodeVariant(t *testing.T) {
	amd := []GPUInfo{{Vendor: "amd", VRAMBytes: 16 << 30}}
	cases := []struct {
		h    Hello
		want string
	}{
		{Hello{OS: "linux", Arch: "amd64", GPUs: amd, VulkanAvailable: true}, "linux-x64-vulkan"},
		{Hello{OS: "linux", Arch: "amd64", GPUs: amd}, "linux-x64-cpu"},
		{Hello{OS: "linux", Arch: "amd64"}, "linux-x64-cpu"},
		{Hello{OS: "windows", Arch: "amd64", GPUs: amd}, "win-x64-vulkan"},
		{Hello{OS: "darwin", Arch: "arm64", GPUs: []GPUInfo{{Vendor: "apple"}}}, "macos-arm64"},
	}
	for _, c := range cases {
		if got, _ := nodeVariant(&c.h); got != c.want {
			t.Errorf("%s/%s gpus=%d: got %s want %s", c.h.OS, c.h.Arch, len(c.h.GPUs), got, c.want)
		}
	}
}

func TestModelNameFromFile(t *testing.T) {
	if n := modelNameFromFile("Q4_K_M/Qwen3-Coder-Next-Q4_K_M-00001-of-00003.gguf"); n != "qwen3-coder-next-q4_k_m" {
		t.Errorf("got %s", n)
	}
}

func TestLegacyPasswordUpgrade(t *testing.T) {
	dir := t.TempDir()
	c := &Controller{dataDir: dir}
	salt := "abc"
	legacy := `{"Salt":"abc","Hash":"` + hashSecret(salt, "old-password") + `"}`
	if err := os.WriteFile(filepath.Join(dir, "admin.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.loadAdmin(false); err != nil || c.needsSetup() {
		t.Fatalf("existing password lost: %v", err)
	}
	if c.checkPassword("wrong") {
		t.Fatal("wrong password accepted")
	}
	if !c.checkPassword("old-password") {
		t.Fatal("legacy password rejected")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "admin.json"))
	if !strings.Contains(string(b), "pbkdf2-sha256") {
		t.Fatal("hash not upgraded")
	}
	c2 := &Controller{dataDir: dir}
	c2.loadAdmin(false)
	if !c2.checkPassword("old-password") {
		t.Fatal("upgraded hash does not verify after reload")
	}
}

func TestSafeJoin(t *testing.T) {
	for _, bad := range []string{"../x", "/etc/passwd", "a/../../x", ""} {
		if _, err := safeJoin("/root", bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := safeJoin("/root", "a/b/../c.txt"); err != nil {
		t.Error(err)
	}
}

func TestUsageCost(t *testing.T) {
	// 1M input + 1M output on Sonnet 5.5 = $2 + $10.
	if c := usageCost("claude-sonnet-5-5", 1e6, 1e6, 0, 0); c < 11.99 || c > 12.01 {
		t.Errorf("sonnet cost %f", c)
	}
	// Haiku long-context pricing above 100k tokens.
	if c := usageCost("claude-haiku-5-5", 200000, 0, 0, 0); c < 0.099 || c > 0.101 {
		t.Errorf("haiku long cost %f", c)
	}
}
