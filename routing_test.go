package main

import (
	"testing"
	"time"
)

func TestGuessTier(t *testing.T) {
	cases := []struct {
		name  string
		bytes int64
		want  int
	}{
		{"llama-3.2-3b-instruct-q4km", 0, tierSmall},
		{"qwen2.5-coder-14b-q4km", 0, tierMedium},
		{"qwen3-coder-30b-a3b-q4km", 0, tierLarge},
		{"gemma-4-26b-a4b-q4km", 0, tierLarge},
		{"mystery-model", 2_000_000_000, tierSmall},
		{"mystery-model", 9_000_000_000, tierMedium},
		{"mystery-model", 40_000_000_000, tierLarge},
		{"mystery-model", 0, tierMedium},
	}
	for _, tc := range cases {
		if got := guessTier(tc.name, tc.bytes); got != tc.want {
			t.Errorf("guessTier(%q, %d) = %d, want %d", tc.name, tc.bytes, got, tc.want)
		}
	}
}

func TestNormDifficulty(t *testing.T) {
	for in, want := range map[string]string{"Hard": "hard", " easy ": "easy", "": "normal", "weird": "normal"} {
		if got := normDifficulty(in); got != want {
			t.Errorf("normDifficulty(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChooseNodeHard(t *testing.T) {
	big := nodeScore{"big", tierLarge, 10}
	small := nodeScore{"small", tierSmall, 80}
	all := []nodeScore{big, small}
	if got := chooseNode("hard", []nodeScore{small}, all, time.Second); got != "" {
		t.Errorf("hard should wait for the strong node, got %q", got)
	}
	if got := chooseNode("hard", []nodeScore{small}, all, 3*time.Minute); got != "small" {
		t.Errorf("hard should take any free node after the wait, got %q", got)
	}
	if got := chooseNode("hard", []nodeScore{big, small}, all, 0); got != "big" {
		t.Errorf("hard should take the strongest free node, got %q", got)
	}
}

func TestChooseNodeEasyAndNormal(t *testing.T) {
	big := nodeScore{"big", tierLarge, 10}
	small := nodeScore{"small", tierSmall, 80}
	all := []nodeScore{big, small}
	if got := chooseNode("easy", all, all, 0); got != "small" {
		t.Errorf("easy should take the fastest node, got %q", got)
	}
	if got := chooseNode("normal", all, all, 0); got != "big" {
		t.Errorf("normal should favour the stronger tier, got %q", got)
	}
	// A fast medium node beats a very slow large one.
	med := nodeScore{"med", tierMedium, 50}
	slowLarge := nodeScore{"slow", tierLarge, 2}
	if got := chooseNode("normal", []nodeScore{med, slowLarge}, nil, 0); got != "med" {
		t.Errorf("normal should weigh speed too, got %q", got)
	}
	// No speeds known: the tier decides.
	if got := chooseNode("normal", []nodeScore{{"a", tierSmall, 0}, {"b", tierMedium, 0}}, nil, 0); got != "b" {
		t.Errorf("got %q", got)
	}
	if got := chooseNode("easy", nil, all, 0); got != "" {
		t.Errorf("no free node should return empty, got %q", got)
	}
}

func TestPickReviewer(t *testing.T) {
	writer := nodeScore{"w", tierMedium, 20}
	weak := nodeScore{"weak", tierSmall, 90}
	strong := nodeScore{"strong", tierLarge, 10}
	same := nodeScore{"same", tierMedium, 30}
	if got := pickReviewer(writer, []nodeScore{weak, strong, same}); got != "same" {
		t.Errorf("want the fastest node at least as strong, got %q", got)
	}
	if got := pickReviewer(writer, []nodeScore{weak}); got != "weak" {
		t.Errorf("want fallback to any free node, got %q", got)
	}
	if got := pickReviewer(writer, nil); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestRecordSpeed(t *testing.T) {
	c := &Controller{nodes: map[string]*NodeRec{"n": {ID: "n"}}, lastHB: map[string]*Heartbeat{"n": {LoadedModel: "m"}}, dataDir: t.TempDir()}
	c.recordSpeed("n", 20)
	if b := c.nodes["n"].Bench; b == nil || b.TokPerSec != 20 {
		t.Fatalf("first sample should seed the figure: %+v", b)
	}
	c.nodes["n"].Bench = &NodeBench{Model: "m", GenTPS: 10, TokPerSec: 10}
	c.recordSpeed("n", 20)
	if got := c.nodes["n"].Bench.TokPerSec; got < 12.9 || got > 13.1 {
		t.Errorf("rolling average = %v, want 13", got)
	}
	// A different model resets the figure.
	c.lastHB["n"].LoadedModel = "other"
	c.recordSpeed("n", 40)
	if b := c.nodes["n"].Bench; b.Model != "other" || b.TokPerSec != 40 {
		t.Errorf("model change should reset: %+v", b)
	}
}

func TestValidatePlanDifficulty(t *testing.T) {
	p := &Plan{Subtasks: []PlanSubtask{{ID: "t1", Files: []string{"a.py"}, Difficulty: "HARD"}, {ID: "t2", Files: []string{"b.py"}}}}
	if err := validatePlan(p); err != nil {
		t.Fatal(err)
	}
	if p.Subtasks[0].Difficulty != "hard" || p.Subtasks[1].Difficulty != "normal" {
		t.Errorf("difficulties = %q, %q", p.Subtasks[0].Difficulty, p.Subtasks[1].Difficulty)
	}
}

func TestLightBench(t *testing.T) {
	cpu := &NodeRec{Hello: &Hello{CPU: CPUInfo{Threads: 4}}}
	if !lightBench(cpu, "m") {
		t.Error("a 4-thread CPU-only node should use the short benchmark")
	}
	big := &NodeRec{Hello: &Hello{CPU: CPUInfo{Threads: 62}}}
	if lightBench(big, "m") {
		t.Error("a big CPU node should use the full benchmark")
	}
	gpu := &NodeRec{Hello: &Hello{CPU: CPUInfo{Threads: 4}, GPUs: []GPUInfo{{Name: "x"}}}}
	if lightBench(gpu, "m") {
		t.Error("a GPU node should use the full benchmark")
	}
	big.BenchFail = &BenchFail{Model: "m"}
	if !lightBench(big, "m") || lightBench(big, "other") {
		t.Error("a failure should switch to the short benchmark for that model only")
	}
	full, _ := benchPromptFor(false)
	if p, n := benchPromptFor(true); len(p) >= len(full) || n >= 256 {
		t.Error("the short benchmark should be smaller")
	}
}

func TestBenchBlocked(t *testing.T) {
	c := &Controller{
		conns: map[string]*nodeConn{"n": {}}, nodes: map[string]*NodeRec{"n": {ID: "n"}},
		lastHB:   map[string]*Heartbeat{"n": {LoadedModel: "m", InferenceRdy: true}},
		reserved: map[string]bool{}, pipelines: map[string]*pipeline{},
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if why := c.benchBlockedLocked("n"); why != "" {
		t.Fatalf("idle ready node blocked: %s", why)
	}
	c.reserved["n"] = true
	if c.benchBlockedLocked("n") == "" {
		t.Error("a reserved node should be busy")
	}
	delete(c.reserved, "n")
	c.nodes["n"].Benching = true
	if c.benchBlockedLocked("n") == "" {
		t.Error("a running benchmark should block another")
	}
	c.nodes["n"].Benching = false
	c.lastHB["n"].InferenceRdy = false
	if c.benchBlockedLocked("n") == "" {
		t.Error("a node with no ready model should be blocked")
	}
	if c.benchBlockedLocked("missing") == "" {
		t.Error("an unknown node should be blocked")
	}
}
