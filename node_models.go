package main

// Model files on the node: manifest, list, push, remove. Split out of node_llama.go.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// ---------- models ----------

type modelManifest struct {
	Name  string      `json:"name"`
	Files []ModelFile `json:"files"`
}

func (n *Node) modelManifest(name string) (*modelManifest, error) {
	b, err := os.ReadFile(filepath.Join(n.modelsDir(), name, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m modelManifest
	if err := json.Unmarshal(b, &m); err != nil || len(m.Files) == 0 {
		return nil, errors.New("bad manifest")
	}
	return &m, nil
}

func (n *Node) listModels() []ModelInfo {
	out := []ModelInfo{}
	entries, _ := os.ReadDir(n.modelsDir())
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := n.modelManifest(e.Name())
		if err != nil {
			continue
		}
		var total int64
		for _, f := range m.Files {
			total += f.Bytes
		}
		out = append(out, ModelInfo{Name: m.Name, Bytes: total})
	}
	return out
}

func (n *Node) pushModel(mp ModelPush) {
	op := "model.push"
	var total int64
	for _, f := range mp.Files {
		total += f.Bytes
	}
	var badPeers []string
	fail := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		log.Printf("model %s: %s", mp.Name, msg)
		st := OpStatus{Op: op, Target: mp.Name, State: "failed", BytesTotal: total, Error: msg, BadPeers: badPeers}
		_ = n.send("node.op_status", st)
	}
	if !validName(mp.Name) || len(mp.Files) == 0 {
		fail("bad model name")
		return
	}
	defer n.markModelBusy(mp.Name)()
	if _, err := n.modelManifest(mp.Name); err == nil {
		n.opStatus(op, mp.Name, "done", total, total, "")
		return
	}
	dir := filepath.Join(n.modelsDir(), mp.Name)
	var have int64
	for _, f := range mp.Files {
		if st, err := os.Stat(filepath.Join(dir, f.Path)); err == nil {
			have += st.Size()
		} else if st, err := os.Stat(filepath.Join(dir, f.Path+".part")); err == nil {
			have += st.Size()
		}
	}
	if err := n.makeRoom(total-have, mp.Name, mp.Reclaim); err != nil {
		fail("%v", err)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail("%v", err)
		return
	}
	var before int64
	n.opStatus(op, mp.Name, "downloading", 0, total, "")
	for i, f := range mp.Files {
		if !validName(f.Path) {
			fail("bad file name %q", f.Path)
			return
		}
		dst := filepath.Join(dir, f.Path)
		if st, err := os.Stat(dst); err == nil && st.Size() == f.Bytes {
			before += f.Bytes
			continue
		}
		base := before
		report := func(d int64) { n.opStatus(op, mp.Name, "downloading", base+d, total, "") }
		var err error
		if len(mp.Peers) > 0 {
			var bad []string
			bad, err = n.fetchFromPeers(mp, f, dst, report)
			badPeers = append(badPeers, bad...)
		} else {
			err = n.fetchFile(fmt.Sprintf("/v1/models/%s/files/%d", mp.Name, i), dst, f.SHA256, f.Bytes, report)
		}
		if err != nil {
			fail("download %s: %v", f.Path, err)
			return
		}
		before += f.Bytes
	}
	b, _ := json.MarshalIndent(modelManifest{Name: mp.Name, Files: mp.Files}, "", "  ")
	if err := writeFileAtomic(filepath.Join(dir, "manifest.json"), b, 0o644); err != nil {
		fail("%v", err)
		return
	}
	n.markVerified(mp.Name, true) // just checked on arrival
	n.touchModel(mp.Name)
	log.Printf("model %s stored (%s)", mp.Name, humanBytes(total))
	n.resendHello() // before "done": the controller checks the model list next
	n.opStatus(op, mp.Name, "done", total, total, "")
}

func (n *Node) removeModel(name string) {
	if !validName(name) {
		return
	}
	if n.protectedModels()[name] {
		n.opStatus("model.remove", name, "failed", 0, 0, "in use: loaded, set to reload, or being copied; unload it first")
		return
	}
	dir := filepath.Join(n.modelsDir(), name)
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() {
		n.opStatus("model.remove", name, "done", 0, 0, "")
		return // gone already, or a symlink or file: never followed
	}
	if err := os.RemoveAll(dir); err != nil {
		n.opStatus("model.remove", name, "failed", 0, 0, err.Error())
		return
	}
	n.opStatus("model.remove", name, "done", 0, 0, "")
	n.resendHello()
}

func humanBytes(b int64) string {
	const u = 1024
	if b < u {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(u), 0
	for k := b / u; k >= u; k /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// resendHello refreshes the controller's view of models and runtime.
func (n *Node) resendHello() { _ = n.send("node.hello_update", n.hello()) }
