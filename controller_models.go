package main

// Controller side of models: downloads GGUF files from Hugging Face once,
// serves them to nodes, and runs the "load this model on that node" pipeline
// (install llama.cpp → push model → start llama-server).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type CatalogEntry struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Repo     string   `json:"repo"`
	Files    []string `json:"files"`
	Bytes    int64    `json:"bytes"`
	For      string   `json:"for"`
	Notes    string   `json:"notes"`
	Ctx      int      `json:"ctx"` // suggested context size
	MinRAMGB int      `json:"min_ram_gb"`
	Coding   bool     `json:"coding"`           // a good local coding model; ranked in Suggested
	MMProj   string   `json:"mmproj,omitempty"` // vision projector file in the same repo; its size is included in Bytes
}

// isMMProj reports whether a model file is a vision projector (loaded with
// llama-server's --mmproj) rather than the model itself.
func isMMProj(file string) bool { return strings.Contains(strings.ToLower(path.Base(file)), "mmproj") }

// File names and sizes checked against Hugging Face on 2026-10-24 (the Qwen3.6 and Gemma 4 sizes are rounded from the web listing, not exact).
var modelCatalog = []CatalogEntry{
	{ID: "qwen2.5-coder-3b-q4km", Coding: true, Label: "Qwen2.5-Coder 3B Instruct (Q4_K_M)", Repo: "Qwen/Qwen2.5-Coder-3B-Instruct-GGUF",
		Files: []string{"qwen2.5-coder-3b-instruct-q4_k_m.gguf"}, Bytes: 2104932800, For: "Test VMs, 4 GB RAM CPU nodes",
		Notes: "Small and quick on CPU. Good for testing the pipeline; weak at multi-file code.", Ctx: 8192, MinRAMGB: 4},
	{ID: "qwen2.5-coder-7b-q4km", Coding: true, Label: "Qwen2.5-Coder 7B Instruct (Q4_K_M)", Repo: "bartowski/Qwen2.5-Coder-7B-Instruct-GGUF",
		Files: []string{"Qwen2.5-Coder-7B-Instruct-Q4_K_M.gguf"}, Bytes: 4683074336, For: "CPU nodes with 8 GB+ RAM",
		Notes: "Solid for narrow tasks like one function and its tests.", Ctx: 8192, MinRAMGB: 8},
	{ID: "qwen2.5-coder-14b-q4km", Coding: true, Label: "Qwen2.5-Coder 14B Instruct (Q4_K_M)", Repo: "bartowski/Qwen2.5-Coder-14B-Instruct-GGUF",
		Files: []string{"Qwen2.5-Coder-14B-Instruct-Q4_K_M.gguf"}, Bytes: 8988111072, For: "a 16 GB GPU, smaller model",
		Notes: "Fits fully in 16 GB VRAM with room for context.", Ctx: 16384, MinRAMGB: 12},
	{ID: "qwen3-coder-30b-a3b-q4km", Coding: true, Label: "Qwen3-Coder 30B-A3B Instruct (Q4_K_M)", Repo: "unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF",
		Files: []string{"Qwen3-Coder-30B-A3B-Instruct-Q4_K_M.gguf"}, Bytes: 18556689568, For: "a 16 GB GPU — default worker",
		Notes: "Mixture of experts, ~3B active per token, so it is fast. llama.cpp keeps what does not fit in VRAM in system RAM.", Ctx: 16384, MinRAMGB: 24},
	{ID: "devstral-small-2-24b-q4km", Coding: true, Label: "Devstral Small 2 24B (Q4_K_M)", Repo: "unsloth/Devstral-Small-2-24B-Instruct-2512-GGUF",
		Files: []string{"Devstral-Small-2-24B-Instruct-2512-Q4_K_M.gguf"}, Bytes: 14334446752, For: "a 16 GB GPU — alternative",
		Notes: "Built for agentic coding; fits a 16 GB card. Compare it with Qwen3-Coder on your tasks.", Ctx: 16384, MinRAMGB: 16},
	{ID: "qwen3-coder-next-q4km", Coding: true, Label: "Qwen3-Coder-Next 80B-A3B (Q4_K_M)", Repo: "unsloth/Qwen3-Coder-Next-GGUF",
		Files: []string{"Qwen3-Coder-Next-Q4_K_M.gguf"}, Bytes: 48528320544, For: "a large-RAM CPU node (64 GB+)",
		Notes: "~3B active per token, so reasonable on many CPU cores. Planner-quality local model.", Ctx: 32768, MinRAMGB: 64},
	{ID: "qwen3.6-35b-a3b-q4km", Coding: true, Label: "Qwen3.6 35B-A3B (Q4_K_M)", Repo: "unsloth/Qwen3.6-35B-A3B-GGUF",
		Files: []string{"Qwen3.6-35B-A3B-UD-Q4_K_M.gguf"}, MMProj: "mmproj-F16.gguf", Bytes: 23000000000, For: "a large-RAM CPU node, or a 16 GB GPU with RAM offload",
		Notes: "Newer than Qwen3-Coder 30B-A3B; ~3B active per token. Reads images (Vision). Needs a current llama.cpp build. Compare it with Qwen3-Coder on your tasks.", Ctx: 16384, MinRAMGB: 32},
	{ID: "qwen3.6-27b-q4km", Coding: true, Label: "Qwen3.6 27B (Q4_K_M)", Repo: "unsloth/Qwen3.6-27B-GGUF",
		Files: []string{"Qwen3.6-27B-Q4_K_M.gguf"}, MMProj: "mmproj-F16.gguf", Bytes: 17700000000, For: "16 GB GPUs with RAM offload",
		Notes: "Dense, so slower per token than the A3B models and a tight fit in 16 GB VRAM. Reads images (Vision). Needs a current llama.cpp build.", Ctx: 8192, MinRAMGB: 24},
	{ID: "gemma-4-26b-a4b-q4km", Label: "Gemma 4 26B-A4B (Q4_K_M)", Repo: "unsloth/gemma-4-26B-A4B-it-GGUF",
		Files: []string{"gemma-4-26B-A4B-it-UD-Q4_K_M.gguf"}, MMProj: "mmproj-F16.gguf", Bytes: 18100000000, For: "a large-RAM CPU node, or 16 GB GPUs with RAM offload",
		Notes: "Google MoE, ~4B active per token, Apache 2.0. Strong general model; worth a second opinion in review. Reads images (Vision): the projector file is downloaded with it. Needs a current llama.cpp build.", Ctx: 16384, MinRAMGB: 24},
}

type ModelRec struct {
	Name       string      `json:"name"`
	Label      string      `json:"label"`
	Repo       string      `json:"repo"`
	HFFiles    []string    `json:"hf_files"`
	Files      []ModelFile `json:"files"`
	Ctx        int         `json:"ctx"`
	Storage    string      `json:"storage"` // "controller" (stored here, copied to nodes) or "direct" (nodes download from Hugging Face)
	Status     string      `json:"status"`  // resolving | downloading | ready | failed
	BytesDone  int64       `json:"bytes_done"`
	BytesTotal int64       `json:"bytes_total"`
	Error      string      `json:"error,omitempty"`
	Vision     bool        `json:"vision"` // has a vision projector (mmproj) file, so it can read images
	Added      time.Time   `json:"added"`
	Tier       string      `json:"tier,omitempty"`       // small | medium | large; empty = guessed from the name
	TierGuess  string      `json:"tier_guess,omitempty"` // filled in for the UI
	cancel     context.CancelFunc
}

// hasVision reports whether the model comes with a vision projector file.
func (m *ModelRec) hasVision() bool {
	for _, f := range m.HFFiles {
		if isMMProj(f) {
			return true
		}
	}
	return false
}

func (c *Controller) modelsStoreDir() string { return filepath.Join(c.dataDir, "models") }

func (c *Controller) loadModels() {
	c.models = map[string]*ModelRec{}
	entries, _ := os.ReadDir(c.modelsStoreDir())
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(c.modelsStoreDir(), e.Name(), "model.json"))
		if err != nil {
			continue
		}
		var m ModelRec
		if json.Unmarshal(b, &m) != nil || m.Name != e.Name() {
			continue
		}
		if m.Storage == "" {
			m.Storage = "controller"
		}
		if m.Status == "downloading" || m.Status == "resolving" {
			m.Status, m.Error = "failed", "interrupted by a controller restart; click Retry to resume"
		}
		m.Vision = m.hasVision()
		c.models[m.Name] = &m
	}
}

func (c *Controller) saveModelLocked(m *ModelRec) {
	m.Vision = m.hasVision()
	dir := filepath.Join(c.modelsStoreDir(), m.Name)
	_ = os.MkdirAll(dir, 0o755)
	b, _ := json.MarshalIndent(m, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "model.json"), b, 0o644)
}

var splitPartRe = regexp.MustCompile(`-\d{5}-of-\d{5}$`)

func modelNameFromFile(file string) string {
	n := strings.TrimSuffix(path.Base(file), ".gguf")
	n = splitPartRe.ReplaceAllString(n, "")
	n = strings.ToLower(n)
	var b strings.Builder
	for _, r := range n {
		if r == '_' || r == '-' || r == '.' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-.")
}

// hfBase is Hugging Face's address (a variable so tests can use a local stand-in).
var hfBase = "https://huggingface.co"

var hfRepoRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func (c *Controller) handleAddModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogID string   `json:"catalog_id"`
		Repo      string   `json:"repo"`
		Files     []string `json:"files"`
		MMProj    string   `json:"mmproj"` // optional vision projector file of a custom repo
		Name      string   `json:"name"`
		Storage   string   `json:"storage"` // "direct" (default) or "controller"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad json"})
		return
	}
	m := &ModelRec{Added: time.Now(), Ctx: 8192, Storage: "direct"}
	if req.Storage == "controller" {
		m.Storage = "controller"
	}
	if req.CatalogID != "" {
		var ce *CatalogEntry
		for i := range modelCatalog {
			if modelCatalog[i].ID == req.CatalogID {
				ce = &modelCatalog[i]
			}
		}
		if ce == nil {
			writeJSON(w, 400, map[string]string{"error": "unknown catalog entry"})
			return
		}
		m.Name, m.Label, m.Repo, m.Ctx = ce.ID, ce.Label, ce.Repo, ce.Ctx
		m.HFFiles = append([]string{}, ce.Files...)
		if ce.MMProj != "" {
			m.HFFiles = append(m.HFFiles, ce.MMProj)
		}
	} else {
		if !hfRepoRe.MatchString(req.Repo) || len(req.Files) == 0 {
			writeJSON(w, 400, map[string]string{"error": "give a Hugging Face repo (owner/name) and at least one .gguf file"})
			return
		}
		for _, f := range req.Files {
			if !strings.HasSuffix(f, ".gguf") || strings.Contains(f, "..") || strings.HasPrefix(f, "/") {
				writeJSON(w, 400, map[string]string{"error": "files must be .gguf paths inside the repo"})
				return
			}
		}
		sort.Strings(req.Files)
		m.Repo, m.HFFiles = req.Repo, req.Files
		if req.MMProj != "" {
			if !isMMProj(req.MMProj) || !strings.HasSuffix(req.MMProj, ".gguf") || strings.Contains(req.MMProj, "..") || strings.HasPrefix(req.MMProj, "/") {
				writeJSON(w, 400, map[string]string{"error": "the vision projector must be an mmproj .gguf path inside the repo"})
				return
			}
			m.HFFiles = append(m.HFFiles, req.MMProj)
		}
		m.Name = req.Name
		if m.Name == "" {
			m.Name = modelNameFromFile(req.Files[0])
		}
		m.Label = req.Repo + " · " + path.Base(req.Files[0])
	}
	if !validName(m.Name) {
		writeJSON(w, 400, map[string]string{"error": "invalid model name"})
		return
	}
	c.mu.Lock()
	if old, ok := c.models[m.Name]; ok && old.Status != "failed" {
		c.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "that model is already in the library"})
		return
	}
	c.models[m.Name] = m
	c.startModelWorkLocked(m)
	c.mu.Unlock()
	writeJSON(w, 200, m)
}

func (c *Controller) setModelProgress(m *ModelRec, done, total int64) {
	c.mu.Lock()
	m.BytesDone, m.BytesTotal = done, total
	c.mu.Unlock()
}

// downloadModel fetches each file from Hugging Face, resuming .part files.
func (c *Controller) downloadModel(ctx context.Context, m *ModelRec) {
	dir := filepath.Join(c.modelsStoreDir(), m.Name)
	_ = os.MkdirAll(dir, 0o755)
	fail := func(err error) {
		c.mu.Lock()
		m.Status, m.Error = "failed", err.Error()
		if ctx.Err() != nil {
			m.Error = "cancelled"
		}
		c.saveModelLocked(m)
		c.mu.Unlock()
		log.Printf("model %s download failed: %v", m.Name, err)
	}
	token := c.hfToken()
	client := &http.Client{}
	// Sizes first, so progress covers all parts.
	sizes := make([]int64, len(m.HFFiles))
	var total int64
	for i, f := range m.HFFiles {
		req, _ := http.NewRequestWithContext(ctx, "HEAD", hfURL(m.Repo, f), nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			fail(fmt.Errorf("Hugging Face: %w", err))
			return
		}
		resp.Body.Close()
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			fail(fmt.Errorf("Hugging Face refused %s (%s); the model may be gated: accept its license on huggingface.co and add a token in Settings", f, resp.Status))
			return
		}
		if resp.StatusCode != 200 {
			fail(fmt.Errorf("Hugging Face: %s for %s", resp.Status, f))
			return
		}
		sizes[i] = resp.ContentLength
		total += resp.ContentLength
	}
	if free := diskFree(c.modelsStoreDir()); free > 0 && uint64(total) > free {
		fail(fmt.Errorf("not enough disk space on the controller: need %s, have %s", humanBytes(total), humanBytes(int64(free))))
		return
	}
	c.setModelProgress(m, 0, total)
	var files []ModelFile
	var before int64
	for i, f := range m.HFFiles {
		local := path.Base(f)
		dst := filepath.Join(dir, local)
		sum, err := downloadResumable(ctx, client, hfURL(m.Repo, f), token, dst, sizes[i], func(d int64) {
			c.setModelProgress(m, before+d, total)
		})
		if err != nil {
			fail(err)
			return
		}
		before += sizes[i]
		files = append(files, ModelFile{Path: local, SHA256: sum, Bytes: sizes[i]})
	}
	c.mu.Lock()
	m.Files, m.Status, m.Error, m.BytesDone = files, "ready", "", total
	c.saveModelLocked(m)
	c.mu.Unlock()
	log.Printf("model %s downloaded (%s)", m.Name, humanBytes(total))
}

func hfURL(repo, file string) string {
	parts := strings.Split(file, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return hfBase + "/" + repo + "/resolve/main/" + strings.Join(parts, "/") + "?download=true"
}

// downloadResumable downloads url to dst (via dst.part) and returns its SHA-256.
func downloadResumable(ctx context.Context, client *http.Client, u, token, dst string, size int64, progress func(int64)) (string, error) {
	if st, err := os.Stat(dst); err == nil && st.Size() == size {
		sum, _, err := fileSHA256(dst)
		return sum, err
	}
	part := dst + ".part"
	h := sha256.New()
	var have int64
	if f, err := os.Open(part); err == nil {
		have, _ = io.Copy(h, f)
		f.Close()
	}
	for attempt := 0; ; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if have > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
		}
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK && have > 0 {
			h.Reset()
			have = 0
		}
		if err == nil && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			err = fmt.Errorf("Hugging Face: %s", resp.Status)
		}
		if err == nil {
			flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
			if have > 0 {
				flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
			}
			f, ferr := os.OpenFile(part, flags, 0o644)
			if ferr != nil {
				resp.Body.Close()
				return "", ferr
			}
			buf := make([]byte, 1<<20)
			last := time.Now()
			for {
				k, rerr := resp.Body.Read(buf)
				if k > 0 {
					if _, werr := f.Write(buf[:k]); werr != nil {
						f.Close()
						resp.Body.Close()
						return "", werr
					}
					h.Write(buf[:k])
					have += int64(k)
					if time.Since(last) > time.Second {
						progress(have)
						last = time.Now()
					}
				}
				if rerr == io.EOF {
					break
				}
				if rerr != nil {
					err = rerr
					break
				}
			}
			f.Close()
			resp.Body.Close()
			if err == nil {
				if size > 0 && have != size {
					return "", fmt.Errorf("size mismatch (%d of %d bytes)", have, size)
				}
				progress(have)
				if err := os.Rename(part, dst); err != nil {
					return "", err
				}
				return hex.EncodeToString(h.Sum(nil)), nil
			}
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if attempt >= 5 {
			return "", err
		}
		log.Printf("download %s: %v; retrying", path.Base(dst), err)
		time.Sleep(time.Duration(2<<attempt) * time.Second)
	}
}

func (c *Controller) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	c.mu.Lock()
	m, ok := c.models[name]
	if ok {
		delete(c.models, name)
		if m.cancel != nil {
			m.cancel()
		}
	}
	c.mu.Unlock()
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "no such model"})
		return
	}
	go func() {
		time.Sleep(time.Second) // let a cancelled download close its file
		os.RemoveAll(filepath.Join(c.modelsStoreDir(), name))
	}()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleRetryModel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	c.mu.Lock()
	m, ok := c.models[name]
	if !ok || m.Status != "failed" {
		c.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "only failed downloads can be retried"})
		return
	}
	c.startModelWorkLocked(m)
	c.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleServeModelFile streams a model file to an authenticated node (supports Range).
func (c *Controller) handleServeModelFile(w http.ResponseWriter, r *http.Request) {
	if c.nodeFromBearer(r) == nil {
		writeJSON(w, 401, ErrInfo{Code: "bad_key", Message: "unknown node key"})
		return
	}
	name := r.PathValue("name")
	idx, err := strconv.Atoi(r.PathValue("index"))
	c.mu.Lock()
	m, ok := c.models[name]
	var f ModelFile
	if ok && m.Status == "ready" && err == nil && idx >= 0 && idx < len(m.Files) {
		f = m.Files[idx]
	} else {
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		writeJSON(w, 404, ErrInfo{Code: "no_model", Message: "model not available"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, filepath.Join(c.modelsStoreDir(), name, f.Path))
}

// handleHFFiles lists .gguf files in a Hugging Face repo, grouping split parts.
func (c *Controller) handleHFFiles(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	repo = strings.TrimPrefix(strings.TrimPrefix(repo, "https://huggingface.co/"), "huggingface.co/")
	repo = strings.Trim(repo, "/")
	if !hfRepoRe.MatchString(repo) {
		writeJSON(w, 400, map[string]string{"error": "enter a repo like owner/name"})
		return
	}
	req, _ := http.NewRequestWithContext(r.Context(), "GET", hfBase+"/api/models/"+repo+"/tree/main?recursive=true", nil)
	if t := c.hfToken(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": "Hugging Face: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		writeJSON(w, 502, map[string]string{"error": "Hugging Face: " + resp.Status})
		return
	}
	var tree []struct {
		Type string `json:"type"`
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		writeJSON(w, 502, map[string]string{"error": "Hugging Face: unexpected answer"})
		return
	}
	type group struct {
		Name  string    `json:"name"`
		Files []string  `json:"files"`
		Bytes int64     `json:"bytes"`
		Fits  []NodeFit `json:"fits"`
	}
	groups := map[string]*group{}
	mmprojs := []string{} // vision projectors, offered separately
	for _, t := range tree {
		if t.Type == "file" && strings.HasSuffix(t.Path, ".gguf") && isMMProj(t.Path) {
			mmprojs = append(mmprojs, t.Path)
			continue
		}
		if t.Type != "file" || !strings.HasSuffix(t.Path, ".gguf") {
			continue
		}
		key := splitPartRe.ReplaceAllString(strings.TrimSuffix(t.Path, ".gguf"), "")
		g := groups[key]
		if g == nil {
			g = &group{Name: path.Base(key)}
			groups[key] = g
		}
		g.Files = append(g.Files, t.Path)
		g.Bytes += t.Size
	}
	out := []*group{}
	ctx := 8192
	if v, err := strconv.Atoi(r.URL.Query().Get("ctx")); err == nil && v > 0 {
		ctx = v
	}
	for _, g := range groups {
		sort.Strings(g.Files)
		c.mu.Lock()
		g.Fits = c.nodeFitsLocked(g.Bytes, ctx)
		c.mu.Unlock()
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes < out[j].Bytes })
	sort.Strings(mmprojs)
	writeJSON(w, 200, map[string]any{"repo": repo, "groups": out, "mmproj": mmprojs})
}

// ---------- node model operations ----------

type nodeOpView struct {
	OpStatus
	Updated time.Time `json:"updated"`
}

func (c *Controller) setNodeOp(nodeID string, st OpStatus) {
	c.mu.Lock()
	c.nodeOps[nodeID] = &nodeOpView{OpStatus: st, Updated: time.Now()}
	c.mu.Unlock()
}

// waitNodeOp waits until the node reports a final state for op/target.
func (c *Controller) waitNodeOp(ctx context.Context, nodeID, op, target string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		st := c.nodeOps[nodeID]
		online := c.conns[nodeID] != nil
		c.mu.Unlock()
		if st != nil && st.Op == op && st.Target == target {
			switch st.State {
			case "done", "ready":
				return nil
			case "failed":
				return errors.New(st.Error)
			}
		}
		if !online {
			return errors.New("node went offline")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("timed out waiting for %s", op)
}

func (c *Controller) nodeHasModel(nodeID, name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.nodes[nodeID]
	if n == nil || n.Hello == nil {
		return false
	}
	for _, m := range n.Hello.Models {
		if m.Name == name {
			return true
		}
	}
	return false
}

// handleLoadModel runs the whole pipeline for a node in the background:
// make sure llama.cpp is installed, push the model if missing, then load it.
func (c *Controller) handleLoadModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name      string   `json:"name"`
		Ctx       int      `json:"ctx"`
		GPULayers string   `json:"gpu_layers"`
		Threads   int      `json:"threads"`
		ExtraArgs string   `json:"extra_args"`
		Pool      []string `json:"pool"` // helper node IDs that lend their memory
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeJSON(w, 400, map[string]string{"error": "model name required"})
		return
	}
	c.mu.Lock()
	m := c.models[req.Name]
	n := c.nodes[id]
	online := c.conns[id] != nil
	busy := c.pipelines[id] != nil
	c.mu.Unlock()
	switch {
	case m == nil || m.Status != "ready":
		writeJSON(w, 409, map[string]string{"error": "that model is not ready in the library yet"})
		return
	case n == nil || !online:
		writeJSON(w, 409, map[string]string{"error": "node is offline"})
		return
	case busy:
		writeJSON(w, 409, map[string]string{"error": "this node is already setting up a model"})
		return
	}
	if len(req.Pool) > 0 && !c.poolingOn() {
		writeJSON(w, 409, map[string]string{"error": "memory pooling is off; enable it under Settings first (trusted networks only)"})
		return
	}
	if err := c.checkPool(id, req.Pool); err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	if req.Ctx <= 0 {
		req.Ctx = m.Ctx
	}
	ml := ModelLoad{Name: req.Name, Ctx: req.Ctx, GPULayers: req.GPULayers, Threads: req.Threads, ExtraArgs: strings.Fields(req.ExtraArgs)}
	ctx, cancel := context.WithCancel(context.Background())
	p := &pipeline{cancel}
	c.mu.Lock()
	c.pipelines[id] = p
	oldPool := n.Pool
	n.LoadPrefs = &ml
	n.Pool = append([]string(nil), req.Pool...)
	for _, h := range req.Pool {
		if hn := c.nodes[h]; hn != nil {
			hn.LoadPrefs = nil // a helper lends memory instead of running its own model
		}
	}
	c.saveNodesLocked()
	c.mu.Unlock()
	c.releaseHelpers(oldPool, req.Pool)
	go func() {
		defer c.endPipeline(id, p)
		if err := c.loadPipeline(ctx, id, ml); err != nil {
			if ctx.Err() != nil {
				return // cancelled by an unload
			}
			log.Printf("node %s: load %s failed: %v", id, ml.Name, err)
			c.setNodeOp(id, OpStatus{Op: "model.load", Target: ml.Name, State: "failed", Error: err.Error()})
		}
	}()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) loadPipeline(ctx context.Context, id string, ml ModelLoad) error {
	c.mu.Lock()
	n := c.nodes[id]
	m := c.models[ml.Name]
	nc := c.conns[id]
	c.mu.Unlock()
	if n == nil || nc == nil || m == nil {
		return errors.New("node or model unavailable")
	}
	// 1. llama.cpp (one build across a pool, so the RPC protocol matches)
	c.mu.Lock()
	pool := append([]string(nil), n.Pool...)
	c.mu.Unlock()
	build := ""
	if len(pool) > 0 {
		var err error
		if build, err = c.poolBuild(ctx, append([]string{id}, pool...)); err != nil {
			return err
		}
	}
	if err := c.installRuntimeOn(ctx, id, build); err != nil {
		return err
	}
	// 1b. helpers lend their memory
	if len(pool) > 0 {
		if !c.poolingOn() {
			return errors.New("memory pooling is off; enable it under Settings first")
		}
		endpoints, err := c.startHelpers(ctx, id, pool, build)
		if err != nil {
			return err
		}
		ml.RPC = endpoints
	}
	// 2. model files: from another node first, checked against the controller's hash
	if !c.nodeHasModel(id, ml.Name) {
		if err := c.copyFromPeers(ctx, id, m); err != nil && ctx.Err() == nil {
			log.Printf("node %s: copy of %s from other nodes failed (%v); using the usual route", id, ml.Name, err)
		}
	}
	if !c.nodeHasModel(id, ml.Name) && m.Storage == "direct" {
		files := make([]ModelFetchFile, len(m.Files))
		for i, f := range m.Files {
			files[i] = ModelFetchFile{ModelFile: f, URL: hfURL(m.Repo, m.HFFiles[i])}
		}
		c.setNodeOp(id, OpStatus{Op: "model.fetch", Target: ml.Name, State: "downloading", BytesTotal: totalBytes(m.Files)})
		if err := nc.send("model.fetch", ModelFetch{Name: ml.Name, Files: files, Token: c.hfToken()}); err != nil {
			return err
		}
		if err := c.waitNodeOp(ctx, id, "model.fetch", ml.Name, 24*time.Hour); err != nil {
			return fmt.Errorf("downloading the model on the node: %w", err)
		}
	}
	if !c.nodeHasModel(id, ml.Name) && m.Storage != "direct" {
		c.setNodeOp(id, OpStatus{Op: "model.push", Target: ml.Name, State: "downloading", BytesTotal: totalBytes(m.Files)})
		if err := nc.send("model.push", ModelPush{Name: ml.Name, Files: m.Files}); err != nil {
			return err
		}
		if err := c.waitNodeOp(ctx, id, "model.push", ml.Name, 24*time.Hour); err != nil {
			return fmt.Errorf("copying the model: %w", err)
		}
	}
	// 3. load
	c.setNodeOp(id, OpStatus{Op: "model.load", Target: ml.Name, State: "loading"})
	if err := nc.send("model.load", ml); err != nil {
		return err
	}
	return c.waitNodeOp(ctx, id, "model.load", ml.Name, 20*time.Minute)
}

func totalBytes(fs []ModelFile) int64 {
	var t int64
	for _, f := range fs {
		t += f.Bytes
	}
	return t
}

func (c *Controller) sendToNode(w http.ResponseWriter, id, typ string, body any) {
	c.mu.Lock()
	nc := c.conns[id]
	c.mu.Unlock()
	if nc == nil {
		writeJSON(w, 409, map[string]string{"error": "node is offline"})
		return
	}
	if err := nc.send(typ, body); err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleUnloadModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var pool []string
	c.mu.Lock()
	if p := c.pipelines[id]; p != nil {
		p.cancel() // stop a setup still in progress
	}
	if n := c.nodes[id]; n != nil {
		n.LoadPrefs = nil
		pool, n.Pool = n.Pool, nil
		c.saveNodesLocked()
	}
	c.mu.Unlock()
	c.releaseHelpers(pool, nil)
	c.sendToNode(w, id, "model.unload", struct{}{})
}

func (c *Controller) handleRemoveNodeModel(w http.ResponseWriter, r *http.Request) {
	c.sendToNode(w, r.PathValue("id"), "model.remove", ModelRemove{Name: r.PathValue("name")})
}

// handleListModels returns the catalog, the controller's model store and the
// llama.cpp builds it holds.
func (c *Controller) handleListModels(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	models := make([]*ModelRec, 0, len(c.models))
	for _, m := range c.models {
		m.TierGuess = tierName(guessTier(m.Name, m.BytesTotal))
		models = append(models, m)
	}
	rts := make([]*RuntimeRec, 0, len(c.runtimes))
	for _, rt := range c.runtimes {
		rts = append(rts, rt)
	}
	fits := map[string][]NodeFit{}
	catFits := map[string][]NodeFit{}
	for _, m := range models {
		if m.Status == "ready" {
			fits[m.Name] = c.nodeFitsLocked(m.BytesTotal, m.Ctx)
		}
	}
	for _, e := range modelCatalog {
		catFits[e.ID] = c.nodeFitsLocked(e.Bytes, e.Ctx)
	}
	recommended := map[string]LoadOption{}
	for id := range c.nodes {
		if c.conns[id] == nil {
			continue
		}
		if opts, _ := c.loadOptionsFor(id, 1); len(opts) > 0 && opts[0].Rank == 1 {
			recommended[id] = opts[0]
		}
	}
	c.mu.Unlock()
	sort.Slice(models, func(i, j int) bool { return models[i].Added.Before(models[j].Added) })
	sort.Slice(rts, func(i, j int) bool { return rts[i].Variant < rts[j].Variant })
	writeJSON(w, 200, map[string]any{"catalog": modelCatalog, "models": models, "runtimes": rts,
		"fits": fits, "catalog_fits": catFits, "suggested": suggestCoding(modelCatalog, catFits, 4), "recommended": recommended,
		"disk_free": diskFree(c.modelsStoreDir())})
}

// startModelWorkLocked resolves a direct model's files on Hugging Face, or
// downloads a controller-stored model. c.mu must be held.
func (c *Controller) startModelWorkLocked(m *ModelRec) {
	if m.Storage == "" {
		m.Storage = "controller"
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.Error = ""
	if m.Storage == "direct" {
		m.Status = "resolving"
		c.saveModelLocked(m)
		go c.resolveModel(ctx, m)
		return
	}
	m.Status = "downloading"
	c.saveModelLocked(m)
	go c.downloadModel(ctx, m)
}

// hfTreeEntry is one file from the Hugging Face tree API; large (LFS) files
// carry their SHA-256 in lfs.oid.
type hfTreeEntry struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	LFS  *struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	} `json:"lfs"`
}

func hfTree(ctx context.Context, repo, token string) ([]hfTreeEntry, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", hfBase+"/api/models/"+repo+"/tree/main?recursive=true", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Hugging Face: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("Hugging Face refused %s (%s); the model may be gated: accept its license on huggingface.co and add a token in Settings", repo, resp.Status)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Hugging Face: %s for %s", resp.Status, repo)
	}
	var tree []hfTreeEntry
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		return nil, errors.New("Hugging Face: unexpected answer")
	}
	return tree, nil
}

// resolveModel looks up sizes and checksums so nodes can download and verify
// the files themselves. Nothing is stored on the controller.
func (c *Controller) resolveModel(ctx context.Context, m *ModelRec) {
	tree, err := hfTree(ctx, m.Repo, c.hfToken())
	var files []ModelFile
	var total int64
	if err == nil {
		byPath := map[string]hfTreeEntry{}
		for _, e := range tree {
			byPath[e.Path] = e
		}
		for _, f := range m.HFFiles {
			e, ok := byPath[f]
			if !ok {
				err = fmt.Errorf("%s is not in %s", f, m.Repo)
				break
			}
			mf := ModelFile{Path: path.Base(f), Bytes: e.Size}
			if e.LFS != nil {
				mf.SHA256, mf.Bytes = e.LFS.OID, e.LFS.Size
			}
			files = append(files, mf)
			total += mf.Bytes
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.models[m.Name] != m || m.Storage != "direct" {
		return // deleted or switched while resolving
	}
	if err != nil {
		m.Status, m.Error = "failed", err.Error()
	} else {
		m.Files, m.BytesTotal, m.BytesDone, m.Status = files, total, 0, "ready"
	}
	c.saveModelLocked(m)
}

// handleStoreModel downloads a direct model to the controller too.
func (c *Controller) handleStoreModel(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.models[r.PathValue("name")]
	if !ok || m.Storage != "direct" {
		writeJSON(w, 409, map[string]string{"error": "that model is already stored on the controller"})
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.Storage = "controller"
	c.startModelWorkLocked(m)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleUnstoreModel deletes a model's files from the controller but keeps it
// in the library; nodes that need it then download it from Hugging Face.
func (c *Controller) handleUnstoreModel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	c.mu.Lock()
	m, ok := c.models[name]
	if !ok || m.Storage == "direct" {
		c.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "that model is not stored on the controller"})
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.Storage = "direct"
	c.startModelWorkLocked(m) // re-resolve: refreshes sizes and checksums
	c.mu.Unlock()
	go func() {
		time.Sleep(time.Second) // let a cancelled download close its file
		dir := filepath.Join(c.modelsStoreDir(), name)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.Name() != "model.json" {
				os.RemoveAll(filepath.Join(dir, e.Name()))
			}
		}
	}()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- pooling (llama.cpp RPC) ----------

// installRuntimeOn makes sure node id runs llama.cpp (a given build, or the newest).
func (c *Controller) installRuntimeOn(ctx context.Context, id, build string) error {
	c.mu.Lock()
	var hello Hello
	if n := c.nodes[id]; n != nil && n.Hello != nil {
		hello = *n.Hello
	}
	nc := c.conns[id]
	c.mu.Unlock()
	if nc == nil {
		return fmt.Errorf("%s is offline", c.nodeLabel(id))
	}
	variant, _ := nodeVariant(&hello)
	if variant == "" {
		return fmt.Errorf("no llama.cpp build for %s/%s", hello.OS, hello.Arch)
	}
	c.setNodeOp(id, OpStatus{Op: "runtime", Target: variant, State: "preparing"})
	rt, err := c.ensureRuntimeBuild(ctx, variant, build)
	if err != nil {
		return fmt.Errorf("llama.cpp: %w", err)
	}
	if hello.Runtime == nil || hello.Runtime.Build != rt.Build || hello.Runtime.Variant != variant {
		if err := nc.send("runtime.install", RuntimeInstall{Build: rt.Build, Variant: variant, Archive: rt.Archive, SHA256: rt.SHA256, Bytes: rt.Bytes}); err != nil {
			return err
		}
		if err := c.waitNodeOp(ctx, id, "runtime", variant, 30*time.Minute); err != nil {
			return fmt.Errorf("installing llama.cpp on %s: %w", c.nodeLabel(id), err)
		}
	}
	return nil
}

// poolBuild picks one llama.cpp build that all pool members can run.
func (c *Controller) poolBuild(ctx context.Context, members []string) (string, error) {
	var variants []string
	seen := map[string]bool{}
	c.mu.Lock()
	for _, m := range members {
		n := c.nodes[m]
		if n == nil || n.Hello == nil {
			c.mu.Unlock()
			return "", fmt.Errorf("pool member %s is unknown or never connected", m)
		}
		v, _ := nodeVariant(n.Hello)
		if v != "" && !seen[v] {
			seen[v] = true
			variants = append(variants, v)
		}
	}
	c.mu.Unlock()
	rels, err := c.latestReleases(ctx, false)
	if err != nil {
		// Offline: use the build the controller already has, if all variants share it.
		c.mu.Lock()
		defer c.mu.Unlock()
		b := ""
		for _, v := range variants {
			rt := c.runtimes[v]
			if rt == nil || (b != "" && rt.Build != b) {
				return "", fmt.Errorf("cannot check llama.cpp releases (%v) and the pool has no common build on the controller", err)
			}
			b = rt.Build
		}
		return b, nil
	}
	b := commonBuild(rels, variants)
	if b == "" {
		return "", fmt.Errorf("no recent llama.cpp release has builds for all of: %s", strings.Join(variants, ", "))
	}
	return b, nil
}

func hostOf(addr string) string {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return h
}

// startHelpers asks each helper to lend its memory to main and returns their
// RPC endpoints. Each helper only accepts connections from main's address.
func (c *Controller) startHelpers(ctx context.Context, main string, helpers []string, build string) ([]string, error) {
	c.mu.Lock()
	mainConn := c.conns[main]
	c.mu.Unlock()
	if mainConn == nil {
		return nil, errors.New("main node is offline")
	}
	mainIP := hostOf(mainConn.addr)
	var endpoints []string
	for _, h := range helpers {
		if err := c.installRuntimeOn(ctx, h, build); err != nil {
			return nil, err
		}
		c.mu.Lock()
		hc := c.conns[h]
		threads := 0
		if n := c.nodes[h]; n != nil && n.Hello != nil {
			threads = n.Hello.CPU.Threads
		}
		c.mu.Unlock()
		if hc == nil {
			return nil, fmt.Errorf("%s is offline", c.nodeLabel(h))
		}
		c.setNodeOp(h, OpStatus{Op: "rpc", Target: "serve", State: "starting"})
		if err := hc.send("rpc.start", RPCStart{Host: hostOf(hc.addr), Allow: []string{mainIP}, Threads: threads}); err != nil {
			return nil, err
		}
		if err := c.waitNodeOp(ctx, h, "rpc", "serve", 2*time.Minute); err != nil {
			return nil, fmt.Errorf("%s could not lend its memory: %w", c.nodeLabel(h), err)
		}
		c.mu.Lock()
		ep := ""
		if hb := c.lastHB[h]; hb != nil {
			ep = hb.RPCEndpoint
		}
		c.mu.Unlock()
		if ep == "" {
			return nil, fmt.Errorf("%s started lending but reported no address", c.nodeLabel(h))
		}
		endpoints = append(endpoints, ep)
	}
	return endpoints, nil
}

// checkPool validates a requested pool for main.
func (c *Controller) checkPool(main string, pool []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if lender := c.lendingToLocked(main); lender != "" {
		return fmt.Errorf("this node lends its memory to %s; unload that model first", displayName(c.nodes[lender]))
	}
	seen := map[string]bool{}
	for _, h := range pool {
		n := c.nodes[h]
		switch {
		case h == main || seen[h]:
			return errors.New("each helper can be listed once and cannot be the main node")
		case n == nil:
			return fmt.Errorf("unknown node %s", h)
		case c.conns[h] == nil:
			return fmt.Errorf("%s is offline", displayName(n))
		case len(n.Pool) > 0:
			return fmt.Errorf("%s runs its own pool; unload it first", displayName(n))
		}
		if other := c.lendingToLocked(h); other != "" && other != main {
			return fmt.Errorf("%s already lends its memory to %s", displayName(n), displayName(c.nodes[other]))
		}
		seen[h] = true
	}
	return nil
}

// lendingToLocked returns the main node that helper lends memory to, if any.
func (c *Controller) lendingToLocked(helper string) string {
	for id, n := range c.nodes {
		for _, h := range n.Pool {
			if h == helper {
				return id
			}
		}
	}
	return ""
}

// releaseHelpers tells helpers that left a pool to stop lending.
func (c *Controller) releaseHelpers(old, keep []string) {
	keepSet := map[string]bool{}
	for _, k := range keep {
		keepSet[k] = true
	}
	for _, h := range old {
		if keepSet[h] {
			continue
		}
		c.mu.Lock()
		hc := c.conns[h]
		c.mu.Unlock()
		if hc != nil {
			_ = hc.send("rpc.stop", struct{}{})
		}
	}
}

// poolKeeper re-forms pools after a main node or a helper restarts: when all
// members are online and the main node's model is not running, it loads it again.
func (c *Controller) poolKeeper() {
	last := map[string]time.Time{}
	for range time.Tick(15 * time.Second) {
		type job struct {
			id  string
			ml  ModelLoad
			ctx context.Context
			p   *pipeline
		}
		var todo []job
		c.mu.Lock()
		if !c.poolingOnLocked() {
			c.mu.Unlock()
			continue
		}
		for id, n := range c.nodes {
			if len(n.Pool) == 0 || n.LoadPrefs == nil || c.pipelines[id] != nil || c.conns[id] == nil {
				continue
			}
			hb := c.lastHB[id]
			stale := !c.poolStale[id].IsZero() && c.poolStale[id].After(last[id])
			if !stale && hb != nil && hb.LoadedModel == n.LoadPrefs.Name && (hb.InferenceRdy || hb.LlamaError == "") {
				continue // running or starting
			}
			all := true
			for _, h := range n.Pool {
				if c.conns[h] == nil {
					all = false
				}
			}
			if !all || (!stale && time.Since(last[id]) < 90*time.Second) {
				continue
			}
			delete(c.poolStale, id)
			last[id] = time.Now()
			ctx, cancel := context.WithCancel(context.Background())
			p := &pipeline{cancel}
			c.pipelines[id] = p
			todo = append(todo, job{id, *n.LoadPrefs, ctx, p})
		}
		c.mu.Unlock()
		for _, j := range todo {
			go func(j job) {
				defer c.endPipeline(j.id, j.p)
				log.Printf("pool keeper: starting the pooled model on %s again", c.nodeLabel(j.id))
				if err := c.loadPipeline(j.ctx, j.id, j.ml); err != nil {
					if j.ctx.Err() != nil {
						return
					}
					log.Printf("pool keeper: %s: %v", c.nodeLabel(j.id), err)
					c.setNodeOp(j.id, OpStatus{Op: "model.load", Target: j.ml.Name, State: "failed", Error: err.Error()})
				}
			}(j)
		}
	}
}

// endPipeline clears a finished model setup (if it is still the current one).
type pipeline struct{ cancel context.CancelFunc }

func (c *Controller) endPipeline(id string, p *pipeline) {
	p.cancel()
	c.mu.Lock()
	if c.pipelines[id] == p {
		delete(c.pipelines, id)
	}
	c.mu.Unlock()
}
