package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyModel(t *testing.T) {
	n := newTestPeerNode(t)
	dir := filepath.Join(n.modelsDir(), "m1")
	os.MkdirAll(dir, 0o755)
	data := []byte("model bytes")
	os.WriteFile(filepath.Join(dir, "a.gguf"), data, 0o644)
	sum := sha256.Sum256(data)
	b, _ := json.Marshal(modelManifest{Name: "m1", Files: []ModelFile{{Path: "a.gguf", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data))}}})
	os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o644)

	if err := n.verifyModel("m1"); err != nil {
		t.Fatal(err)
	}
	if n.readVerified()["m1"] != Version {
		t.Fatal("not marked verified")
	}
	// corrupt it; a verified model is not hashed again this version
	os.WriteFile(filepath.Join(dir, "a.gguf"), []byte("model byteX"), 0o644)
	if err := n.verifyModel("m1"); err != nil {
		t.Fatal("re-hashed an already verified model")
	}
	// a new version re-checks, finds the damage and deletes the model
	n.markVerified("m1", false)
	if err := n.verifyModel("m1"); err == nil {
		t.Fatal("corruption not detected")
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("corrupt model not deleted")
	}
}
