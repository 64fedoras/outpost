package main

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func storageNode(t *testing.T) *Node {
	t.Helper()
	n := testNode(t)
	n.workRoot = filepath.Join(n.dataDir, "work")
	n.rpc = newRPCMgr(n)
	return n
}

func writeBytes(t *testing.T, p string, size int) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMeasureStorageGroupsFoldersAndSkipsJobs(t *testing.T) {
	n := storageNode(t)
	writeStoredModel(t, n, "m", 1000, false)
	writeBytes(t, filepath.Join(n.dataDir, "package-store", "go", "x"), 200)
	writeBytes(t, filepath.Join(n.dataDir, "cache", "task1", "y"), 50)
	writeBytes(t, filepath.Join(n.dataDir, "llama", "b1", "llama-server"), 300)
	writeBytes(t, filepath.Join(n.dataDir, "toolchains", "go", "bin"), 70)
	writeBytes(t, filepath.Join(n.dataDir, "agent", "outpost-0.9.0"), 400)
	writeBytes(t, filepath.Join(n.dataDir, "node.json"), 5)
	writeBytes(t, filepath.Join(n.dataDir, "work", "job1", "big"), 9999) // deleted after each job: not counted
	u := n.measureStorage()
	if u.Models != 1000 || u.Packages != 250 || u.Runtimes != 370 || u.Other != 405 {
		t.Errorf("by kind: %+v", u)
	}
	if u.Total != 1000+250+370+405 || u.At == 0 {
		t.Errorf("total: %+v", u)
	}
	if hb := n.buildHeartbeat(); hb.Storage != nil {
		t.Error("nothing is reported before the first measurement")
	}
	n.refreshStorage()
	if hb := n.buildHeartbeat(); hb.Storage == nil || hb.Storage.Total != u.Total {
		t.Errorf("heartbeat: %+v", hb.Storage)
	}
}

func TestStorageLimitRemovesOldModelsFirst(t *testing.T) {
	n := storageNode(t)
	for _, m := range []string{"old", "newer", "hf", "loaded"} {
		writeStoredModel(t, n, m, 1000, true)
	}
	age(t, n, "old", 48*time.Hour)
	age(t, n, "newer", time.Hour)
	age(t, n, "hf", 100*time.Hour)
	n.llama.model = "loaded"
	n.llama.cmd = nil
	used := n.measureStorage().Total
	n.applySettings(NodeSettings{StorageLimit: used + 500})
	rp := &Reclaim{Enabled: false, Names: []string{"old", "newer", "loaded"}, Direct: []string{"hf"}}
	// 1000 more needs 500 freed: only the oldest controller-held copy goes,
	// even though the disk-full cleanup in Settings is off.
	if err := n.makeRoom(1000, "incoming", rp); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"old": false, "newer": true, "hf": true, "loaded": true} {
		if exist(n, name) != want {
			t.Errorf("%s exists=%v want %v", name, !want, want)
		}
	}
	if n.freedNote == "" || !strings.Contains(n.freedNote, "storage limit") {
		t.Errorf("note: %q", n.freedNote)
	}
}

func TestStorageLimitRefuses(t *testing.T) {
	n := storageNode(t)
	writeStoredModel(t, n, "old", 1000, true)
	used := n.measureStorage().Total
	n.applySettings(NodeSettings{StorageLimit: used + 100, StorageFull: "refuse"})
	rp := &Reclaim{Enabled: true, Names: []string{"old"}}
	err := n.makeRoom(1000, "incoming", rp)
	if err == nil || !strings.Contains(err.Error(), "storage limit reached") || !strings.Contains(err.Error(), "nothing was deleted") {
		t.Fatalf("err: %v", err)
	}
	if !exist(n, "old") {
		t.Error("refuse must not delete")
	}
	if !strings.Contains(n.limitNote, "Refused incoming") || n.limitAt == 0 || n.hello().LimitNote == "" {
		t.Errorf("note: %q", n.limitNote)
	}
	// It fits under the limit: no error, nothing touched.
	if err := n.makeRoom(50, "small", rp); err != nil {
		t.Error(err)
	}
}

func TestStorageLimitDeletesNothingWhenCleanupIsNotEnough(t *testing.T) {
	n := storageNode(t)
	writeStoredModel(t, n, "old", 1000, true)
	writeStoredModel(t, n, "mine", 1000, true) // not offered by the controller
	used := n.measureStorage().Total
	n.applySettings(NodeSettings{StorageLimit: used})
	err := n.makeRoom(3000, "incoming", &Reclaim{Names: []string{"old"}})
	if err == nil || !strings.Contains(err.Error(), "would not be enough") {
		t.Fatalf("err: %v", err)
	}
	if !exist(n, "old") || !exist(n, "mine") {
		t.Error("nothing may be deleted when it would not make room")
	}
}

func TestNoStorageLimitChangesNothing(t *testing.T) {
	n := storageNode(t)
	writeStoredModel(t, n, "old", 1000, true)
	fakeDisk(t, n, 1<<40)
	if err := n.makeRoom(1<<30, "incoming", &Reclaim{Enabled: true, Names: []string{"old"}}); err != nil {
		t.Fatal(err)
	}
	if !exist(n, "old") {
		t.Error("without a limit and with disk to spare, nothing goes")
	}
}

func TestStorageLimitSettingsAPI(t *testing.T) {
	c := poolTestController(t)
	c.settings = defaultSettings()
	c.settings.AutoFreeOff = true
	c.nodes["a"] = &NodeRec{ID: "a", Hello: &Hello{Models: []ModelInfo{{Name: "ready-one"}}}}
	c.models["ready-one"] = &ModelRec{Name: "ready-one", Status: "ready", Files: []ModelFile{{Path: "a.gguf"}}}
	post := func(body string) int {
		req := httptest.NewRequest("POST", "/api/nodes/a/settings", bytes.NewBufferString(body))
		req.SetPathValue("id", "a")
		w := httptest.NewRecorder()
		c.handleNodeSettings(w, req)
		return w.Code
	}
	for _, bad := range []string{`{"storage_limit_gb":-1}`, `{"storage_limit_gb":0.5}`, `{"storage_full":"delete"}`} {
		if code := post(bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if rp := c.reclaimFor("a", "x"); len(rp.Names) != 0 {
		t.Error("with no limit and the Settings cleanup off, nothing is offered")
	}
	if code := post(`{"storage_limit_gb":60,"storage_full":"refuse"}`); code != 200 {
		t.Fatalf("save: %d", code)
	}
	c.mu.Lock()
	s := c.nodeSettingsLocked(c.nodes["a"])
	c.mu.Unlock()
	if s.StorageLimit != 60<<30 || s.StorageFull != "refuse" {
		t.Errorf("sent to node: %+v", s)
	}
	if rp := c.reclaimFor("a", "x"); rp.Enabled || len(rp.Names) != 1 {
		t.Errorf("a limit needs the list even with the Settings cleanup off: %+v", rp)
	}
	if code := post(`{"storage_limit_gb":0,"storage_full":""}`); code != 200 {
		t.Fatal(code)
	}
	if c.nodes["a"].StorageLimitGB != 0 || c.nodes["a"].StorageFull != "" {
		t.Errorf("cleared: %+v", c.nodes["a"])
	}
}
