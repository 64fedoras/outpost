package main

// Autoload after reboot, runtime checks, model fetch and chat payload. Split out of node_llama.go.

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ---------- autoload: reload the last model after a reboot or update ----------

func (n *Node) autoloadPath() string { return filepath.Join(n.llamaDir(), "autoload.json") }

func (n *Node) saveAutoload(ml *ModelLoad) {
	if ml == nil {
		os.Remove(n.autoloadPath())
		return
	}
	_ = os.MkdirAll(n.llamaDir(), 0o755)
	b, _ := json.Marshal(ml)
	_ = writeFileAtomic(n.autoloadPath(), b, 0o644)
}

func (n *Node) autoload() {
	b, err := os.ReadFile(n.autoloadPath())
	if err != nil {
		return
	}
	var ml ModelLoad
	if json.Unmarshal(b, &ml) != nil || ml.Name == "" {
		return
	}
	if n.currentRuntime() == nil {
		return
	}
	if len(ml.RPC) > 0 {
		return // pooled models are started by the controller once the helpers are back
	}
	if _, err := n.modelManifest(ml.Name); err != nil {
		return
	}
	log.Printf("reloading %s (it was loaded before this restart)", ml.Name)
	n.llama.load(ml)
}

// missingLibRe matches the dynamic loader's message for a missing library.
var missingLibRe = regexp.MustCompile(`error while loading shared libraries: ([^:\s]+)`)

// aptPackage names the Debian/Ubuntu package for libraries llama.cpp needs.
var aptPackage = map[string]string{
	"libgomp.so.1": "libgomp1", "libvulkan.so.1": "libvulkan1", "libssl.so.3": "libssl3",
	"libcrypto.so.3": "libssl3", "libstdc++.so.6": "libstdc++6", "libgcc_s.so.1": "libgcc-s1",
}

// checkLlamaRuns runs llama-server --version, supplying bundled libraries the
// system is missing. It returns "" on success or a message saying what to do.
func checkLlamaRuns(server string) string {
	dir := filepath.Dir(server)
	for attempt := 0; attempt < 6; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := exec.CommandContext(ctx, server, "--version")
		cmd.Dir = dir
		cmd.Env = llamaEnv(dir)
		out, err := cmd.CombinedOutput()
		cancel()
		if err == nil || bytes.Contains(out, []byte("version:")) {
			return ""
		}
		m := missingLibRe.FindSubmatch(out)
		if m == nil {
			return fmt.Sprintf("llama-server does not run here: %v %s", err, strings.TrimSpace(lastLines(string(out), 3)))
		}
		lib := string(m[1])
		gz, ok := bundledLibs[lib]
		if ok {
			if _, statErr := os.Stat(filepath.Join(dir, lib)); statErr != nil {
				if werr := writeGunzip(filepath.Join(dir, lib), gz); werr == nil {
					log.Printf("supplied %s from the Outpost binary", lib)
					continue
				} else {
					return fmt.Sprintf("could not write %s: %v", lib, werr)
				}
			}
		}
		if pkg, ok := aptPackage[lib]; ok {
			return fmt.Sprintf("llama.cpp needs %s, which this machine does not have. Install it with: sudo apt install -y %s", lib, pkg)
		}
		return fmt.Sprintf("llama.cpp needs the system library %s, which this machine does not have.", lib)
	}
	return "llama-server still fails to start after supplying bundled libraries"
}

func writeGunzip(dst string, gz []byte) error {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return err
	}
	defer zr.Close()
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, zr); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// pruneLlamaBuilds keeps the current build and the previous one (for rollback).
func pruneLlamaBuilds(root, current, previous string) {
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		p := filepath.Join(root, e.Name())
		if e.IsDir() && p != current && p != previous {
			os.RemoveAll(p)
		}
	}
}

// fetchModel downloads a model straight from Hugging Face (resumable, verified
// by SHA-256 when Hugging Face provides one).
func (n *Node) fetchModel(mf ModelFetch) {
	op := "model.fetch"
	var total int64
	for _, f := range mf.Files {
		total += f.Bytes
	}
	fail := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		log.Printf("model %s: %s", mf.Name, msg)
		n.opStatus(op, mf.Name, "failed", 0, total, msg)
	}
	if !validName(mf.Name) || len(mf.Files) == 0 {
		fail("bad model name")
		return
	}
	defer n.markModelBusy(mf.Name)()
	if _, err := n.modelManifest(mf.Name); err == nil {
		n.opStatus(op, mf.Name, "done", total, total, "")
		return
	}
	dir := filepath.Join(n.modelsDir(), mf.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail("%v", err)
		return
	}
	var have int64
	for _, f := range mf.Files {
		if st, err := os.Stat(filepath.Join(dir, f.Path)); err == nil {
			have += st.Size()
		} else if st, err := os.Stat(filepath.Join(dir, f.Path+".part")); err == nil {
			have += st.Size()
		}
	}
	if err := n.makeRoom(total-have, mf.Name, mf.Reclaim); err != nil {
		fail("%v", err)
		return
	}
	n.opStatus(op, mf.Name, "downloading", 0, total, "")
	client := &http.Client{}
	var before int64
	files := make([]ModelFile, 0, len(mf.Files))
	for _, f := range mf.Files {
		if !validName(f.Path) || !strings.HasPrefix(f.URL, hfBase()+"/") {
			fail("bad file %q", f.Path)
			return
		}
		dst := filepath.Join(dir, f.Path)
		base := before
		sum, err := downloadResumable(context.Background(), client, f.URL, mf.Token, dst, f.Bytes, func(d int64) {
			n.opStatus(op, mf.Name, "downloading", base+d, total, "")
		})
		if err != nil {
			fail("download %s: %v", f.Path, err)
			return
		}
		if f.SHA256 != "" && !strings.EqualFold(sum, f.SHA256) {
			os.Remove(dst)
			fail("%s failed its SHA-256 check; it was deleted, try again", f.Path)
			return
		}
		before += f.Bytes
		files = append(files, ModelFile{Path: f.Path, SHA256: sum, Bytes: f.Bytes})
	}
	b, _ := json.MarshalIndent(modelManifest{Name: mf.Name, Files: files}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o644); err != nil {
		fail("%v", err)
		return
	}
	n.markVerified(mf.Name, true)
	n.touchModel(mf.Name)
	log.Printf("model %s downloaded from Hugging Face (%s)", mf.Name, humanBytes(total))
	n.resendHello() // before "done": the controller checks the model list next
	n.opStatus(op, mf.Name, "done", total, total, "")
}

// chatPayload turns messages into llama-server's OpenAI-style form. A message
// with images becomes a list of content parts (text, then one image_url data
// URI per image); plain messages are sent unchanged.
func chatPayload(msgs []ChatMessage) []any {
	out := make([]any, 0, len(msgs))
	for _, m := range msgs {
		if len(m.Images) == 0 {
			msg := map[string]any{"role": m.Role, "content": m.Content}
			if len(m.ToolCalls) > 0 { // agent mode
				msg["tool_calls"] = m.ToolCalls
			}
			if m.ToolCallID != "" {
				msg["tool_call_id"] = m.ToolCallID
			}
			out = append(out, msg)
			continue
		}
		parts := []map[string]any{{"type": "text", "text": m.Content}}
		for _, img := range m.Images {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": img}})
		}
		out = append(out, map[string]any{"role": m.Role, "content": parts})
	}
	return out
}
