package main

// Background re-check of stored model files. After the node runs a new
// Outpost version, each model's files are hashed once and compared with the
// SHA-256 recorded when they arrived, which catches disk corruption. A model
// is also checked right before it is loaded if the background pass has not got
// to it yet. A model that fails is deleted so the controller can send it again.

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// verifiedPath records, per model, the Outpost version that last verified it.
func (n *Node) verifiedPath() string { return filepath.Join(n.modelsDir(), ".verified.json") }

func (n *Node) readVerified() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(n.verifiedPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (n *Node) markVerified(name string, set bool) {
	m := n.readVerified()
	if set {
		m[name] = Version
	} else {
		delete(m, name)
	}
	b, _ := json.Marshal(m)
	_ = writeFileAtomic(n.verifiedPath(), b, 0o644)
}

// verifyModel hashes the files of one model unless this version already did.
// A mismatch removes the model and returns an error naming the file.
func (n *Node) verifyModel(name string) error {
	n.verifyMu.Lock()
	defer n.verifyMu.Unlock()
	if n.readVerified()[name] == Version {
		return nil
	}
	m, err := n.modelManifest(name)
	if err != nil {
		return err
	}
	done := n.markModelBusy(name)
	defer done()
	dir := filepath.Join(n.modelsDir(), name)
	var total, doneBytes int64
	for _, f := range m.Files {
		total += f.Bytes
	}
	n.opStatus("model.verify", name, "verifying", 0, total, "")
	for _, f := range m.Files {
		if !validName(f.Path) {
			return fmt.Errorf("bad file name %q", f.Path)
		}
		if f.SHA256 == "" {
			doneBytes += f.Bytes
			continue // nothing trusted to compare with
		}
		sum, size, err := fileSHA256(filepath.Join(dir, f.Path))
		if err == nil && (!strings.EqualFold(sum, f.SHA256) || (f.Bytes > 0 && size != f.Bytes)) {
			err = fmt.Errorf("%s does not match its recorded SHA-256 (corrupt on disk)", f.Path)
		}
		if err != nil {
			n.markVerified(name, false)
			os.RemoveAll(dir)
			log.Printf("model %s failed its re-check and was deleted: %v", name, err)
			n.opStatus("model.verify", name, "failed", 0, 0, err.Error()+"; the model was deleted, load it again to fetch a fresh copy")
			n.resendHello()
			return err
		}
		doneBytes += f.Bytes
		n.opStatus("model.verify", name, "verifying", doneBytes, total, "")
	}
	n.markVerified(name, true)
	n.opStatus("model.verify", name, "done", total, total, "")
	return nil
}

// verifyModelsBackground checks every stored model once per version.
func (n *Node) verifyModelsBackground() {
	for _, mi := range n.listModels() {
		if n.readVerified()[mi.Name] == Version || n.protectedModels()[mi.Name] {
			continue // protected: loaded or copying; load() checks before start
		}
		if err := n.verifyModel(mi.Name); err != nil {
			log.Printf("background verify %s: %v", mi.Name, err)
		}
	}
}
