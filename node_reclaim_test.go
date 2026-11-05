package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDisk makes the models disk hold capacity bytes in total.
func fakeDisk(t *testing.T, n *Node, capacity int64) {
	t.Helper()
	old := modelsDiskFree
	modelsDiskFree = func(string) uint64 {
		f := capacity - dirBytes(n.modelsDir())
		if f < 1 {
			f = 1
		}
		return uint64(f)
	}
	t.Cleanup(func() { modelsDiskFree = old })
}

func age(t *testing.T, n *Node, name string, d time.Duration) {
	t.Helper()
	n.touchModel(name)
	p := filepath.Join(n.modelsDir(), name, lastUsedFile)
	when := time.Now().Add(-d)
	os.Chtimes(p, when, when)
}

func smallHeadroom(t *testing.T, v int64) {
	old := reclaimHeadroomMin
	reclaimHeadroomMin = v
	t.Cleanup(func() { reclaimHeadroomMin = old })
}

func exist(n *Node, name string) bool { return exists(filepath.Join(n.modelsDir(), name)) }

func TestMakeRoomTiersLRUAndProtection(t *testing.T) {
	n := testNode(t)
	smallHeadroom(t, 0)
	for _, m := range []string{"ctl-old", "ctl-new", "hf-old", "loaded", "unlisted"} {
		writeStoredModel(t, n, m, 100, true)
	}
	age(t, n, "ctl-old", 48*time.Hour)
	age(t, n, "ctl-new", 2*time.Hour)
	age(t, n, "hf-old", 100*time.Hour)
	n.llama.cmd, n.llama.model = &exec.Cmd{}, "loaded"
	s := dirBytes(filepath.Join(n.modelsDir(), "ctl-old"))
	fakeDisk(t, n, 5*s+s/2) // free = s/2
	rp := &Reclaim{Enabled: true, Names: []string{"ctl-old", "ctl-new", "loaded"}, Direct: []string{"hf-old"}}
	free := int64(modelsDiskFree(n.modelsDir()))
	// Needs two models' worth more: the two older controller copies go; the Hugging Face one stays.
	if err := n.makeRoom(free+2*s-80, "new", rp); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"ctl-old": false, "ctl-new": false, "hf-old": true, "loaded": true, "unlisted": true} {
		if exist(n, name) != want {
			t.Errorf("%s exists=%v want %v", name, !want, want)
		}
	}
	// Now the Hugging Face copy goes too, after the controller ones.
	free = int64(modelsDiskFree(n.modelsDir()))
	if err := n.makeRoom(free+s-80, "new", rp); err != nil {
		t.Fatal(err)
	}
	if exist(n, "hf-old") || !exist(n, "loaded") || !exist(n, "unlisted") {
		t.Error("tier 2 / protection wrong")
	}
	if !strings.Contains(n.freedNote, "hf-old (not on controller") {
		t.Errorf("note: %q", n.freedNote)
	}
}

func TestMakeRoomLeastRecentlyUsedFirstAndStopsEarly(t *testing.T) {
	n := testNode(t)
	smallHeadroom(t, 0)
	for _, m := range []string{"a", "b", "c"} {
		writeStoredModel(t, n, m, 100, true)
	}
	age(t, n, "b", 3*time.Hour)
	age(t, n, "c", 2*time.Hour)
	age(t, n, "a", time.Hour)
	fakeDisk(t, n, 400)
	rp := &Reclaim{Enabled: true, Names: []string{"a", "b", "c"}}
	free := int64(modelsDiskFree(n.modelsDir()))
	if err := n.makeRoom(free+50, "x", rp); err != nil {
		t.Fatal(err)
	}
	if exist(n, "b") || !exist(n, "c") || !exist(n, "a") {
		t.Errorf("expected only b removed: a=%v b=%v c=%v", exist(n, "a"), exist(n, "b"), exist(n, "c"))
	}
}

func TestMakeRoomNothingDeletedWhenImpossible(t *testing.T) {
	n := testNode(t)
	writeStoredModel(t, n, "a", 100, true)
	fakeDisk(t, n, 1000)
	err := n.makeRoom(1<<40, "x", &Reclaim{Enabled: true, Names: []string{"a"}})
	if err == nil || !strings.Contains(err.Error(), "nothing was deleted") {
		t.Fatalf("got %v", err)
	}
	if !exist(n, "a") {
		t.Fatal("deleted despite being unable to make room")
	}
}

func TestMakeRoomOffOrFits(t *testing.T) {
	n := testNode(t)
	writeStoredModel(t, n, "a", 100, true)
	fakeDisk(t, n, 1000)
	if err := n.makeRoom(10, "x", nil); err != nil {
		t.Fatal(err)
	}
	if err := n.makeRoom(5000, "x", &Reclaim{Enabled: false, Names: []string{"a"}}); err == nil || !exist(n, "a") {
		t.Fatal("disabled cleanup must not delete")
	}
}
