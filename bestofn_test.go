package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBonEligible(t *testing.T) {
	hard := PlanSubtask{Difficulty: "hard"}
	normal := PlanSubtask{Difficulty: "normal"}
	inv := PlanSubtask{Difficulty: "hard", Kind: "investigate"}
	cases := []struct {
		mode string
		s    PlanSubtask
		want bool
	}{
		{bonOff, hard, false}, {bonHard, hard, true}, {bonHard, normal, false},
		{bonAll, normal, true}, {bonAll, inv, false}, {bonHard, inv, false},
	}
	for _, tc := range cases {
		if got := bonEligible(tc.mode, tc.s); got != tc.want {
			t.Errorf("%s/%s/%s: got %v", tc.mode, tc.s.Difficulty, tc.s.Kind, got)
		}
	}
	if normBestOf("") != bonHard || normBestOf("OFF") != bonOff || normBestOf("junk") != bonHard {
		t.Error("normBestOf defaults")
	}
}

func TestPickExtraNodes(t *testing.T) {
	ns := func(specs ...nodeScore) []nodeScore { return specs }
	if got := pickExtraNodes(ns(nodeScore{ID: "a", Tier: 3})); got != nil {
		t.Errorf("one idle node must not start extras: %v", got)
	}
	got := pickExtraNodes(ns(
		nodeScore{ID: "small", Tier: 1, Speed: 50}, nodeScore{ID: "big", Tier: 3, Speed: 10},
		nodeScore{ID: "mid", Tier: 2, Speed: 20}, nodeScore{ID: "mid2", Tier: 2, Speed: 30}))
	if strings.Join(got, ",") != "big,mid2" {
		t.Errorf("got %v, want the two strongest", got)
	}
	if got := pickExtraNodes(ns(nodeScore{ID: "a"}, nodeScore{ID: "b"})); len(got) != 2 {
		t.Errorf("two idle nodes: %v", got)
	}
}

func TestSyntaxCommands(t *testing.T) {
	files := map[string]string{"b.js": "", "a.py": "", "c.go": "", "d.json": "{}", "README.md": "x"}
	cmds, bad := syntaxCommands(files, "linux")
	if bad != "" {
		t.Fatal(bad)
	}
	want := `python3 -m py_compile "a.py"|node --check "b.js"|gofmt -e -l "c.go"`
	if strings.Join(cmds, "|") != want {
		t.Errorf("cmds = %q", strings.Join(cmds, "|"))
	}
	if cmds, _ := syntaxCommands(map[string]string{"a.py": ""}, "windows"); cmds[0] != `python -m py_compile "a.py"` {
		t.Errorf("windows: %v", cmds)
	}
	if _, bad := syntaxCommands(map[string]string{"x.json": "{nope"}, "linux"); !strings.Contains(bad, "x.json") {
		t.Errorf("bad json not caught: %q", bad)
	}
}

func TestParseJudge(t *testing.T) {
	idx, reason, ok := parseJudge("<<<PICK B>>>\nREASON: B handles the empty list.", 3)
	if !ok || idx != 1 || reason != "B handles the empty list." {
		t.Errorf("got %d %q %v", idx, reason, ok)
	}
	if _, _, ok := parseJudge("<<<PICK D>>>", 3); ok {
		t.Error("a letter past the last candidate must be rejected")
	}
	if _, _, ok := parseJudge("I like the second one", 3); ok {
		t.Error("no pick must be rejected")
	}
	if idx, _, ok := parseJudge("<<<pick a>>>", 2); !ok || idx != 0 {
		t.Error("lower case pick")
	}
}

// bonRig is a controller with fake nodes that answer inference from a map.
type bonRig struct {
	c      *Controller
	task   *Task
	spec   PlanSubtask
	mu     sync.Mutex
	judged int
	answer func(node string) string // the file each node writes
	pick   string                   // what the judge says
}

func newBonRig(t *testing.T, nodes map[string]string) *bonRig {
	t.Helper()
	c := &Controller{dataDir: t.TempDir(), conns: map[string]*nodeConn{}, lastHB: map[string]*Heartbeat{}, nodes: map[string]*NodeRec{},
		models: map[string]*ModelRec{}, reserved: map[string]bool{}, pipelines: map[string]*pipeline{}, cooldown: map[string]time.Time{}}
	for id, model := range nodes {
		c.conns[id] = &nodeConn{}
		c.lastHB[id] = &Heartbeat{InferenceRdy: true, LoadedModel: model}
		c.nodes[id] = &NodeRec{ID: id, Name: id, Bench: &NodeBench{Model: model, TokPerSec: 10}}
	}
	dir := filepath.Join(c.tasksDir(), "t1")
	os.MkdirAll(dir, 0o755)
	spec := PlanSubtask{ID: "s1", Title: "dice", Files: []string{"dice.py"}, Instructions: "write roll()", Difficulty: "hard"}
	tk := &Task{ID: "t1", Title: "demo", dir: dir, Plan: &Plan{Subtasks: []PlanSubtask{spec}}, Subtasks: []*SubtaskState{{ID: "s1", Status: "running"}}}
	r := &bonRig{c: c, task: tk, spec: spec, pick: "<<<PICK B>>>\nREASON: B has the edge cases."}
	r.answer = func(node string) string { return "code from " + node + "\n" }
	c.inferStub = func(ctx context.Context, node string, spec InferenceSpec) (*InferenceOutput, error) {
		if spec.Messages[0].Content == judgeSystem {
			r.mu.Lock()
			r.judged++
			r.mu.Unlock()
			return &InferenceOutput{Text: r.pick, TokensPerSec: 5}, nil
		}
		return &InferenceOutput{Text: "<<<FILE dice.py>>>\n" + r.answer(node) + "<<<END>>>", OutputTokens: 10, TokensPerSec: 20}, nil
	}
	return r
}

func (r *bonRig) written(t *testing.T) string {
	b, err := os.ReadFile(filepath.Join(r.task.repoDir(), "dice.py"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (r *bonRig) logs() string {
	var b strings.Builder
	for _, e := range r.task.Events {
		b.WriteString(e.Text + "\n")
	}
	return b.String()
}

// Three nodes write the subtask, the judge picks the second candidate.
func TestRunBestOfJudgePicks(t *testing.T) {
	r := newBonRig(t, map[string]string{"n1": "big-32b", "n2": "mid-14b", "n3": "mid-14b"})
	c := r.c
	if !c.tryReserve("n1") || !c.tryReserve("n2") || !c.tryReserve("n3") {
		t.Fatal("reserve")
	}
	g := &bonGroup{}
	c.launchExtras(context.Background(), r.task, g, r.spec, []string{"n2", "n3"})
	win, err := c.runBestOf(context.Background(), r.task, r.spec, "n1", g, func() { c.releaseNode("n1") })
	if err != nil {
		t.Fatal(err)
	}
	if win != "n2" || r.written(t) != "code from n2\n" {
		t.Fatalf("winner %s, file %q (candidate B is the first extra, n2 or n3)", win, r.written(t))
	}
	if r.judged != 1 {
		t.Errorf("judge asked %d times", r.judged)
	}
	if st := r.task.subtask("s1"); !strings.Contains(st.BestOf, "n2 won") || !strings.Contains(st.BestOf, "B has the edge cases") || st.Node != "n2" {
		t.Errorf("state: %+v", st)
	}
	if !strings.Contains(r.logs(), "best of 3") {
		t.Errorf("log should say who won:\n%s", r.logs())
	}
	for _, id := range []string{"n1", "n2", "n3"} {
		if c.isReserved(id) {
			t.Errorf("%s still reserved", id)
		}
	}
}

// A candidate whose syntax check fails is dropped; the survivor wins without a judge.
func TestRunBestOfDropsFailedSyntax(t *testing.T) {
	r := newBonRig(t, map[string]string{"n1": "big-32b", "n2": "mid-14b", "n3": "mid-14b"})
	c := r.c
	c.nodes["n3"].ExecEnabled = true
	c.nodes["n3"].Hello = &Hello{OS: "linux", Tools: []ToolInfo{{Name: "python"}}}
	r.spec.Files = []string{"dice.py"}
	r.answer = func(node string) string {
		if node == "n1" || node == "n2" {
			return "def roll(:  # SYNTAXERR\n"
		}
		return "def roll():\n    return 4\n"
	}
	var checked []string
	c.execStub = func(ctx context.Context, node string, argv []string, inputs []FileEntry) (*ExecOutput, error) {
		if node != "n3" {
			t.Errorf("syntax check ran on %s, want the exec node", node)
		}
		checked = append(checked, argv[len(argv)-1])
		d, _ := decodeInline(inputs[0])
		if strings.Contains(d, "SYNTAXERR") {
			return &ExecOutput{ExitCode: 1, StderrTail: "SyntaxError: invalid syntax"}, nil
		}
		return &ExecOutput{}, nil
	}
	c.tryReserve("n1")
	c.tryReserve("n2")
	c.tryReserve("n3")
	g := &bonGroup{}
	c.launchExtras(context.Background(), r.task, g, r.spec, []string{"n2", "n3"})
	win, err := c.runBestOf(context.Background(), r.task, r.spec, "n1", g, func() { c.releaseNode("n1") })
	if err != nil {
		t.Fatal(err)
	}
	if win != "n3" || r.written(t) != "def roll():\n    return 4\n" {
		t.Fatalf("winner %s file %q", win, r.written(t))
	}
	if r.judged != 0 {
		t.Error("a single survivor needs no judge")
	}
	if len(checked) != 3 || !strings.Contains(checked[0], "py_compile") {
		t.Errorf("checks: %v", checked)
	}
	if lg := r.logs(); !strings.Contains(lg, "dropped") || !strings.Contains(lg, "the only copy that passed") {
		t.Errorf("log:\n%s", lg)
	}
}

// If the judge cannot answer, the copy from the strongest node is kept.
func TestRunBestOfJudgeGarbage(t *testing.T) {
	r := newBonRig(t, map[string]string{"n1": "big-32b", "n2": "tiny-3b", "n3": "tiny-3b"})
	r.pick = "no idea"
	c := r.c
	c.tryReserve("n1")
	c.tryReserve("n2")
	c.tryReserve("n3")
	g := &bonGroup{}
	c.launchExtras(context.Background(), r.task, g, r.spec, []string{"n2", "n3"})
	win, err := c.runBestOf(context.Background(), r.task, r.spec, "n1", g, func() { c.releaseNode("n1") })
	if err != nil || win != "n1" || r.written(t) != "code from n1\n" {
		t.Fatalf("win %s err %v file %q", win, err, r.written(t))
	}
}

// With no extra copies running, the primary's result is written as before.
func TestRunBestOfAlone(t *testing.T) {
	r := newBonRig(t, map[string]string{"n1": "big-32b"})
	c := r.c
	c.tryReserve("n1")
	win, err := c.runBestOf(context.Background(), r.task, r.spec, "n1", &bonGroup{}, func() { c.releaseNode("n1") })
	if err != nil || win != "n1" || r.written(t) != "code from n1\n" || r.judged != 0 {
		t.Fatalf("win %s err %v", win, err)
	}
	if r.task.subtask("s1").BestOf != "" {
		t.Error("no best-of note without extra copies")
	}
}

// A scheduler that needs a node cancels the extra copies; they free the node and
// the primary still finishes with its own result.
func TestCancelExtrasFreesNodes(t *testing.T) {
	r := newBonRig(t, map[string]string{"n1": "big-32b", "n2": "mid-14b", "n3": "mid-14b"})
	c := r.c
	started := make(chan string, 2)
	base := c.inferStub
	c.inferStub = func(ctx context.Context, node string, spec InferenceSpec) (*InferenceOutput, error) {
		if node == "n2" || node == "n3" {
			started <- node
			<-ctx.Done() // slow extra copy
			return nil, ctx.Err()
		}
		return base(ctx, node, spec)
	}
	c.tryReserve("n1")
	c.tryReserve("n2")
	c.tryReserve("n3")
	g := &bonGroup{}
	c.launchExtras(context.Background(), r.task, g, r.spec, []string{"n2", "n3"})
	<-started
	<-started
	if n := c.cancelExtras(r.task, g, "s1", 1); n != 1 {
		t.Fatalf("cancelled %d, want 1", n)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(g.running()) != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(g.running()) != 1 {
		t.Fatalf("running extras = %d, want 1 after cancelling one", len(g.running()))
	}
	if c.cancelExtras(r.task, g, "s1", 0) != 1 {
		t.Fatal("cancel the rest")
	}
	win, err := c.runBestOf(context.Background(), r.task, r.spec, "n1", g, func() { c.releaseNode("n1") })
	if err != nil || win != "n1" || r.written(t) != "code from n1\n" {
		t.Fatalf("win %s err %v", win, err)
	}
	for _, id := range []string{"n1", "n2", "n3"} {
		if c.isReserved(id) {
			t.Errorf("%s still reserved", id)
		}
	}
}

// A primary that is requeued takes the extra copies down with it.
func TestRunBestOfRequeue(t *testing.T) {
	r := newBonRig(t, map[string]string{"n1": "big-32b", "n2": "mid-14b", "n3": "mid-14b"})
	c := r.c
	base := c.inferStub
	c.inferStub = func(ctx context.Context, node string, spec InferenceSpec) (*InferenceOutput, error) {
		if node == "n1" {
			return nil, &ErrInfo{Code: "inference_unavailable"}
		}
		if node != "" && spec.Messages[0].Content != judgeSystem {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return base(ctx, node, spec)
	}
	c.tryReserve("n1")
	c.tryReserve("n2")
	c.tryReserve("n3")
	g := &bonGroup{}
	c.launchExtras(context.Background(), r.task, g, r.spec, []string{"n2", "n3"})
	_, err := c.runBestOf(context.Background(), r.task, r.spec, "n1", g, func() { c.releaseNode("n1") })
	if err != errRequeue {
		t.Fatalf("err = %v, want errRequeue", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for (c.isReserved("n2") || c.isReserved("n3")) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.isReserved("n2") || c.isReserved("n3") {
		t.Error("extras should have been cancelled and released")
	}
	if _, err := os.Stat(filepath.Join(r.task.repoDir(), "dice.py")); err == nil {
		t.Error("nothing should be written")
	}
}

func TestSettingsBestOfN(t *testing.T) {
	c := &Controller{}
	if c.bestOfMode() != bonHard {
		t.Error("default is hard subtasks")
	}
	c.settings.BestOfN = "off"
	if c.bestOfMode() != bonOff || c.settingsView()["best_of_n"] != "off" {
		t.Error("off")
	}
}

func decodeInline(f FileEntry) (string, error) {
	b, err := base64.StdEncoding.DecodeString(f.InlineB64)
	return string(b), err
}
