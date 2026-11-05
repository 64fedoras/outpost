package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The biggest loaded model gets work first, whatever the node IDs are.
func TestWorkerNodesStrongestFirst(t *testing.T) {
	hb := func(model string) *Heartbeat { return &Heartbeat{InferenceRdy: true, LoadedModel: model} }
	c := &Controller{
		conns:  map[string]*nodeConn{"a-small": {}, "b-big": {}, "c-unknown": {}, "d-mid": {}},
		lastHB: map[string]*Heartbeat{"a-small": hb("qwen-3b"), "b-big": hb("qwen-32b"), "c-unknown": hb("mystery"), "d-mid": hb("qwen-14b")},
		models: map[string]*ModelRec{
			"qwen-3b":  {Files: []ModelFile{{Bytes: 2 << 30}}},
			"qwen-32b": {Files: []ModelFile{{Bytes: 10 << 30}, {Bytes: 10 << 30}}},
		},
		nodes: map[string]*NodeRec{"d-mid": {Hello: &Hello{Models: []ModelInfo{{Name: "qwen-14b", Bytes: 9 << 30}}}}},
	}
	got := strings.Join(c.workerNodes(), ",")
	if want := "b-big,d-mid,a-small,c-unknown"; got != want {
		t.Fatalf("order %s, want %s", got, want)
	}
}

// Files named in the test output survive a small prompt budget.
func TestRepoSnapshotFirst(t *testing.T) {
	dir := t.TempDir()
	for name, size := range map[string]int{"a_big.py": 3000, "dice_roller.py": 500, "z.py": 500} {
		os.WriteFile(filepath.Join(dir, name), []byte(strings.Repeat("x", size)), 0o644)
	}
	snap := repoSnapshotFirst(dir, 1200, `File "/work/dice_roller.py", line 4`)
	if !strings.HasPrefix(snap, "<<<FILE dice_roller.py>>>\nxxx") {
		t.Fatalf("dice_roller.py should come first with its content:\n%.80s", snap)
	}
	if !strings.Contains(snap, "<<<FILE a_big.py>>>\n(omitted") {
		t.Fatal("a_big.py should be omitted")
	}
}

// "Auto" plans on the strongest ready node and errors when none is ready.
func TestAutoPlanner(t *testing.T) {
	hb := func(model string) *Heartbeat { return &Heartbeat{InferenceRdy: true, LoadedModel: model} }
	c := &Controller{
		conns:  map[string]*nodeConn{"a": {}, "b": {}},
		lastHB: map[string]*Heartbeat{"a": hb("small"), "b": hb("big")},
		models: map[string]*ModelRec{"small": {Files: []ModelFile{{Bytes: 1 << 30}}}, "big": {Files: []ModelFile{{Bytes: 9 << 30}}}},
		nodes:  map[string]*NodeRec{},
	}
	if p, err := c.autoPlanner(); err != nil || p != "node:b" {
		t.Fatalf("got %q, %v", p, err)
	}
	c.lastHB = map[string]*Heartbeat{}
	if _, err := c.autoPlanner(); err == nil {
		t.Fatal("want an error with no ready node")
	}
}
