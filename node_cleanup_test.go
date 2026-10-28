package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func writeStoredModel(t *testing.T, n *Node, name string, size int, manifest bool) {
	t.Helper()
	dir := filepath.Join(n.modelsDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "a.gguf"), make([]byte, size), 0o644)
	if manifest {
		b, _ := json.Marshal(modelManifest{Name: name, Files: []ModelFile{{Path: "a.gguf", Bytes: int64(size)}}})
		os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o644)
	}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestCleanupKeepsProtectedAndStaysInsideModelsDir(t *testing.T) {
	n := testNode(t)
	writeStoredModel(t, n, "old", 1000, true)
	writeStoredModel(t, n, "loaded", 1000, true)
	writeStoredModel(t, n, "reload", 1000, true)
	writeStoredModel(t, n, "copying", 1000, true)
	writeStoredModel(t, n, "partial", 500, false)

	n.llama.cmd, n.llama.model = &exec.Cmd{}, "loaded" // as while llama-server runs
	n.saveAutoload(&ModelLoad{Name: "reload"})
	defer n.markModelBusy("copying")()

	// Outside targets: a sibling folder and a symlink inside the models folder.
	outside := filepath.Join(n.dataDir, "precious")
	os.MkdirAll(outside, 0o755)
	os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("x"), 0o644)
	if err := os.Symlink(outside, filepath.Join(n.modelsDir(), "link")); err != nil {
		t.Skip("no symlinks:", err)
	}

	n.cleanupModels(ModelCleanup{
		Names:      []string{"old", "loaded", "reload", "copying", "../precious", "link", "a/b", ""},
		Incomplete: true,
	})

	for name, want := range map[string]bool{"old": false, "loaded": true, "reload": true, "copying": true, "partial": false} {
		if got := exists(filepath.Join(n.modelsDir(), name)); got != want {
			t.Errorf("%s exists = %v, want %v", name, got, want)
		}
	}
	if !exists(filepath.Join(outside, "keep.txt")) {
		t.Fatal("cleanup reached outside the models folder")
	}
	if !exists(filepath.Join(n.modelsDir(), "link")) {
		t.Error("symlinked folder was removed or followed")
	}
}

func TestCleanupWithoutIncompleteKeepsPartialDownloads(t *testing.T) {
	n := testNode(t)
	writeStoredModel(t, n, "partial", 500, false)
	n.cleanupModels(ModelCleanup{Names: []string{"nothing"}})
	if !exists(filepath.Join(n.modelsDir(), "partial")) {
		t.Fatal("incomplete download deleted without being asked")
	}
}

func TestRemoveModelKeepsProtected(t *testing.T) {
	n := testNode(t)
	writeStoredModel(t, n, "old", 1000, true)
	writeStoredModel(t, n, "loaded", 1000, true)
	writeStoredModel(t, n, "reload", 1000, true)
	writeStoredModel(t, n, "copying", 1000, true)
	n.llama.cmd, n.llama.model = &exec.Cmd{}, "loaded"
	n.saveAutoload(&ModelLoad{Name: "reload"})
	defer n.markModelBusy("copying")()
	for _, name := range []string{"old", "loaded", "reload", "copying"} {
		n.removeModel(name)
	}
	for name, want := range map[string]bool{"old": false, "loaded": true, "reload": true, "copying": true} {
		if got := exists(filepath.Join(n.modelsDir(), name)); got != want {
			t.Errorf("%s exists = %v, want %v", name, got, want)
		}
	}
}
