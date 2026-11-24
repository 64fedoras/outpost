package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func mkBuild(t *testing.T, root, name string, age time.Duration) string {
	t.Helper()
	d := filepath.Join(root, name)
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "llama-server"), []byte("x"), 0o755)
	old := time.Now().Add(-age)
	os.Chtimes(d, old, old)
	return d
}

func TestPruneLlamaBuildsKeepsCurrentPreviousRunningAndCache(t *testing.T) {
	root := t.TempDir()
	cur := mkBuild(t, root, "b3-vulkan", 0)
	prev := mkBuild(t, root, "b2-vulkan", 0)
	running := mkBuild(t, root, "b1-cpu", 0)
	old := mkBuild(t, root, "b0-cpu", 0)
	cache := filepath.Join(root, "rpc-cache")
	os.MkdirAll(cache, 0o755)
	os.WriteFile(filepath.Join(root, "current.json"), []byte("{}"), 0o644)
	stale := filepath.Join(root, "llama-b0.tar.gz")
	os.WriteFile(stale, []byte("x"), 0o644)
	os.Chtimes(stale, time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour))
	fresh := filepath.Join(root, "llama-b3.tar.gz.part")
	os.WriteFile(fresh, []byte("x"), 0o644)

	pruneLlamaBuilds(root, cur, prev, running)

	for _, p := range []string{cur, prev, running, cache, filepath.Join(root, "current.json"), fresh} {
		if !exists(p) {
			t.Errorf("%s should be kept", p)
		}
	}
	for _, p := range []string{old, stale} {
		if exists(p) {
			t.Errorf("%s should be removed", p)
		}
	}
}

func TestPruneOldBuildsAtStart(t *testing.T) {
	n := testNode(t)
	root := n.llamaDir()
	cur := mkBuild(t, root, "b5-compat-metal", 0)
	mkBuild(t, root, "b4-vulkan", time.Hour)
	mkBuild(t, root, "b3-vulkan", 2*time.Hour)
	mkBuild(t, root, "b2-cpu", 3*time.Hour)
	b, _ := json.Marshal(runtimeRecord{RuntimeInfo: RuntimeInfo{Build: "b5", Variant: "compat-metal"}, ServerPath: filepath.Join(cur, "llama-server")})
	os.WriteFile(filepath.Join(root, "current.json"), b, 0o644)
	os.MkdirAll(filepath.Join(n.modelsDir(), "m1"), 0o755)

	n.pruneOldBuildsAtStart()

	for _, d := range []string{"b5-compat-metal", "b4-vulkan"} {
		if !exists(filepath.Join(root, d)) {
			t.Errorf("%s should be kept", d)
		}
	}
	for _, d := range []string{"b3-vulkan", "b2-cpu"} {
		if exists(filepath.Join(root, d)) {
			t.Errorf("%s should be removed", d)
		}
	}
	if !exists(filepath.Join(n.modelsDir(), "m1")) {
		t.Error("models must never be touched")
	}
}

func TestPruneOldBuildsAtStartKeepsRunningServer(t *testing.T) {
	n := testNode(t)
	root := n.llamaDir()
	cur := mkBuild(t, root, "b5-vulkan", 0)
	mkBuild(t, root, "b4-vulkan", time.Hour)
	run := mkBuild(t, root, "b3-cpu", 2*time.Hour)
	b, _ := json.Marshal(runtimeRecord{RuntimeInfo: RuntimeInfo{Build: "b5", Variant: "vulkan"}, ServerPath: filepath.Join(cur, "llama-server")})
	os.WriteFile(filepath.Join(root, "current.json"), b, 0o644)
	n.llama.cmd = &exec.Cmd{Path: filepath.Join(run, "llama-server")}

	n.pruneOldBuildsAtStart()

	if !exists(run) {
		t.Error("the build a running llama-server uses must be kept")
	}
}

func TestPruneOldBuildsAtStartNoCurrentDoesNothing(t *testing.T) {
	n := testNode(t)
	d := mkBuild(t, n.llamaDir(), "b1-cpu", time.Hour)
	n.pruneOldBuildsAtStart()
	if !exists(d) {
		t.Error("with no current build nothing should be removed")
	}
}
