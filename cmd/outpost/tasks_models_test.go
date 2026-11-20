package main

import (
	"testing"
)

const gb = int64(1 << 30)

func TestPickTaskModels(t *testing.T) {
	cands := []pickCand{
		{Node: "gpu", OptIn: true, Current: "coder-14b", CurBytes: 9 * gb, Best: "coder-80b", BestBytes: 45 * gb},
		{Node: "cpu", OptIn: true, Best: "coder-30b", BestBytes: 18 * gb, OnNode: true},
		{Node: "vm", OptIn: false, Current: "coder-3b", CurBytes: 2 * gb, Best: "coder-3b", BestBytes: 2 * gb},
		{Node: "same", OptIn: true, Current: "coder-7b", CurBytes: 5 * gb, Best: "coder-7b", BestBytes: 5 * gb},
		{Node: "bigger", OptIn: true, Current: "coder-32b", CurBytes: 20 * gb, Best: "coder-14b", BestBytes: 9 * gb},
		{Node: "busy", OptIn: true, Busy: true, Best: "coder-30b", BestBytes: 18 * gb},
		{Node: "none", OptIn: true},
	}
	swaps, kept := pickTaskModels(cands, 10, 60*gb)
	if len(swaps) != 2 || swaps[0].Node != "gpu" || swaps[1].Node != "cpu" {
		t.Fatalf("swaps = %+v", swaps)
	}
	if !swaps[0].Download || swaps[0].Prev != "coder-14b" || swaps[1].Download {
		t.Fatalf("swap details wrong: %+v", swaps)
	}
	why := map[string]string{}
	for _, k := range kept {
		why[k.Node] = k.Skip
	}
	for node, want := range map[string]string{"vm": "not allowed to change model", "same": "already has the best model",
		"bigger": "its model is already as big", "busy": "busy setting up or pooling a model", "none": "no model fits"} {
		if why[node] != want {
			t.Errorf("%s kept for %q, want %q", node, why[node], want)
		}
	}
}

func TestPickTaskModelsLimits(t *testing.T) {
	cands := []pickCand{
		{Node: "a", OptIn: true, Best: "m45", BestBytes: 45 * gb},
		{Node: "b", OptIn: true, Best: "m30", BestBytes: 30 * gb},
		{Node: "c", OptIn: true, Best: "m18", BestBytes: 18 * gb, OnNode: true},
		{Node: "d", OptIn: true, Best: "m9", BestBytes: 9 * gb},
	}
	// Cap 60 GB: a (45) fits, b (30) would pass it, c is on the node, d (9) fits.
	swaps, _ := pickTaskModels(cands, 10, 60*gb)
	got := ""
	for _, s := range swaps {
		got += s.Node
	}
	if got != "acd" {
		t.Fatalf("with cap: %q, want acd", got)
	}
	// Two subtasks: only the two biggest picks switch.
	swaps, kept := pickTaskModels(cands, 2, 0)
	if len(swaps) != 2 || swaps[0].Node != "a" || swaps[1].Node != "b" {
		t.Fatalf("two subtasks: %+v", swaps)
	}
	if len(kept) != 2 || kept[0].Skip != "not needed: fewer subtasks than nodes" {
		t.Fatalf("kept: %+v", kept)
	}
	// Nothing opted in: no swaps at all.
	for i := range cands {
		cands[i].OptIn = false
	}
	if swaps, _ := pickTaskModels(cands, 10, 0); len(swaps) != 0 {
		t.Fatalf("opted out but swapped: %+v", swaps)
	}
}

func TestTaskEnded(t *testing.T) {
	for st, want := range map[string]bool{"done": true, "failed": true, "cancelled": true, "running": false, "awaiting_answers": false, "interrupted": false} {
		if taskEnded(st) != want {
			t.Errorf("taskEnded(%q) = %v", st, !want)
		}
	}
}

func TestRestoreLeavesHandChangedModel(t *testing.T) {
	c := &Controller{nodes: map[string]*NodeRec{"n1": {ID: "n1"}}, lastHB: map[string]*Heartbeat{"n1": {LoadedModel: "picked-by-hand"}}}
	tk := &Task{ID: "t", AutoModels: true, dir: t.TempDir(),
		ModelSwaps: []ModelSwap{{Node: "n1", Model: "task-model", Prev: "old", State: "loaded"}}}
	c.restoreTaskModels(tk)
	if tk.ModelSwaps[0].State != "kept" {
		t.Fatalf("state = %q, want kept", tk.ModelSwaps[0].State)
	}
	// KeepModels: nothing is touched.
	tk.ModelSwaps[0].State, tk.KeepModels = "loaded", true
	c.restoreTaskModels(tk)
	if tk.ModelSwaps[0].State != "loaded" {
		t.Fatalf("KeepModels changed state to %q", tk.ModelSwaps[0].State)
	}
}
