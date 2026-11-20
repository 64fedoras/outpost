package main

// Live preview: HTML pages, SVG images, Mermaid diagrams and Markdown documents
// from chat answers and task files are shown in a sandboxed iframe
// (web/preview.js on the page side, preview/ for the frame). The frame has its
// own Content-Security-Policy: model-written scripts may run, but the document
// has an opaque origin (no cookies, no access to the UI or the API) and every
// network request is blocked, so a page cannot send anything anywhere. The
// frame's files hold no data, so they are served without signing in.

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"outpost"
)

const previewCSP = "sandbox allow-scripts; default-src 'none'; script-src 'self' 'unsafe-inline'; " +
	"style-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src data: blob:; " +
	"connect-src 'none'; frame-src 'none'; worker-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'"

var (
	previewMermaidOnce sync.Once
	previewMermaidGz   []byte
	previewMermaidRaw  []byte // only made for a browser that does not take gzip
	previewMermaidMu   sync.Mutex
)

func previewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /preview/frame", func(w http.ResponseWriter, r *http.Request) {
		previewServe(w, r, "preview/frame.html", "text/html; charset=utf-8")
	})
	mux.HandleFunc("GET /preview/boot.js", func(w http.ResponseWriter, r *http.Request) {
		previewServe(w, r, "preview/boot.js", "text/javascript; charset=utf-8")
	})
	mux.HandleFunc("GET /preview/mermaid.js", handlePreviewMermaid)
}

// previewHeaders replaces the UI's headers: the frame may be framed by the UI,
// and runs under the preview policy.
func previewHeaders(w http.ResponseWriter, ctype string) {
	h := w.Header()
	h.Del("X-Frame-Options")
	h.Set("Content-Security-Policy", previewCSP)
	h.Set("Content-Type", ctype)
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin") // the opaque-origin frame loads boot.js and mermaid.js
}

func previewServe(w http.ResponseWriter, r *http.Request, name, ctype string) {
	b, err := fs.ReadFile(outpost.PreviewFS, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	previewHeaders(w, ctype)
	w.Write(b)
}

func handlePreviewMermaid(w http.ResponseWriter, r *http.Request) {
	previewMermaidOnce.Do(func() { previewMermaidGz, _ = fs.ReadFile(outpost.PreviewFS, "preview/mermaid.min.js.gz") })
	if len(previewMermaidGz) == 0 {
		http.NotFound(w, r)
		return
	}
	previewHeaders(w, "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Vary", "Accept-Encoding")
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(previewMermaidGz)
		return
	}
	previewMermaidMu.Lock()
	if previewMermaidRaw == nil {
		if zr, err := gzip.NewReader(bytes.NewReader(previewMermaidGz)); err == nil {
			previewMermaidRaw, _ = io.ReadAll(zr)
		}
	}
	raw := previewMermaidRaw
	previewMermaidMu.Unlock()
	w.Write(raw)
}

// handleProjectSaveFile writes one file into a project (Save to project in the
// preview panel). It refuses while a task works in the project, so a task never
// sees a file change under it.
func (c *Controller) handleProjectSaveFile(w http.ResponseWriter, r *http.Request) {
	p := c.getProject(r.PathValue("id"))
	if p == nil {
		writeJSON(w, 404, map[string]string{"error": "no such project"})
		return
	}
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRepoFileSize+4096)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	rel := readRel(req.Path)
	if rel == "" || strings.HasPrefix(rel, ".git/") || rel == ".git" {
		writeJSON(w, 400, map[string]string{"error": "give a file name inside the project"})
		return
	}
	if len(req.Content) > maxRepoFileSize {
		writeJSON(w, 400, map[string]string{"error": "the file is too large"})
		return
	}
	select {
	case p.slot <- struct{}{}:
	default:
		writeJSON(w, 409, map[string]string{"error": "a task is working in this project; save again when it finishes"})
		return
	}
	defer func() { <-p.slot }()
	p.mu.Lock()
	repo := p.repoDir()
	p.mu.Unlock()
	fp, err := safeJoin(repo, rel)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad path"})
		return
	}
	_, statErr := os.Stat(fp)
	err = os.MkdirAll(filepath.Dir(fp), 0o755)
	if err == nil {
		err = writeFileAtomic(fp, []byte(req.Content), 0o644)
	}
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"path": rel, "replaced": statErr == nil})
}
