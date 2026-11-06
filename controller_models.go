package main

// Controller side of models: downloads GGUF files from Hugging Face once,
// serves them to nodes, and runs the "load this model on that node" pipeline
// (install llama.cpp → push model → start llama-server).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
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
	Shape      *ModelShape `json:"shape,omitempty"`      // from the GGUF header, for the context-size estimate
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
		if m.Status == "ready" && m.Shape.needsHeaderRefresh() {
			go c.fillModelShape(&m) // models added before the header was read
		}
	}
}

func (c *Controller) saveModelLocked(m *ModelRec) {
	m.Vision = m.hasVision()
	dir := filepath.Join(c.modelsStoreDir(), m.Name)
	_ = os.MkdirAll(dir, 0o755)
	b, _ := json.MarshalIndent(m, "", "  ")
	_ = writeFileAtomic(filepath.Join(dir, "model.json"), b, 0o644)
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
var hfBaseV atomic.Value // string; read through hfBase()

func hfBase() string {
	if v, ok := hfBaseV.Load().(string); ok && v != "" {
		return v
	}
	return "https://huggingface.co"
}

func setHFBase(u string) { hfBaseV.Store(u) }

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
		cat := catalogNow()
		for i := range cat {
			if cat[i].ID == req.CatalogID {
				ce = &cat[i]
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
		if ctx.Err() == nil {
			go c.deliverNotification("model_failed", "Model download failed", fmt.Sprintf("%s could not be downloaded to the controller: %v. Retry on the Models page.", m.Name, err), "")
		}
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
	c.fillModelShape(m)
}

func hfURL(repo, file string) string {
	parts := strings.Split(file, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return hfBase() + "/" + repo + "/resolve/main/" + strings.Join(parts, "/") + "?download=true"
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
	req, _ := http.NewRequestWithContext(r.Context(), "GET", hfBase()+"/api/models/"+repo+"/tree/main?recursive=true", nil)
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
