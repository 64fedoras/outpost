package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyStore(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), models: map[string]*ModelRec{}}
	dir := filepath.Join(c.modelsStoreDir(), "m1")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "a.gguf"), []byte("hello"), 0o644)
	sum, n, _ := fileSHA256(filepath.Join(dir, "a.gguf"))
	c.models["m1"] = &ModelRec{Name: "m1", Status: "ready", Storage: "controller", Files: []ModelFile{{Path: "a.gguf", SHA256: sum, Bytes: n}}}
	if p := c.verifyStore(); len(p) != 0 {
		t.Fatalf("clean store reported %v", p)
	}
	os.WriteFile(filepath.Join(dir, "a.gguf"), []byte("hellp"), 0o644) // same size, flipped byte
	if p := c.verifyStore(); len(p) != 1 {
		t.Fatalf("flipped byte not found: %v", p)
	}
	os.Remove(filepath.Join(dir, "a.gguf"))
	if p := c.verifyStore(); len(p) != 1 {
		t.Fatalf("missing file not found: %v", p)
	}
}
