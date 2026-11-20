package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewFrameHeaders(t *testing.T) {
	mux := http.NewServeMux()
	previewRoutes(mux)
	h := secureHeaders(mux)
	for _, p := range []string{"/preview/frame", "/preview/boot.js", "/preview/mermaid.js"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", p, nil)
		r.Header.Set("Accept-Encoding", "gzip")
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d", p, w.Code)
		}
		csp := w.Header().Get("Content-Security-Policy")
		for _, want := range []string{"sandbox allow-scripts", "connect-src 'none'", "default-src 'none'", "frame-ancestors 'self'", "form-action 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP lacks %q: %s", p, want, csp)
			}
		}
		if strings.Contains(csp, "allow-same-origin") || w.Header().Get("X-Frame-Options") != "" {
			t.Errorf("%s: frame must be sandboxed without same-origin and frameable by the UI", p)
		}
	}
	// Mermaid ships gzip-compressed and is unpacked for a browser that does not take gzip.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/preview/mermaid.js", nil))
	if w.Header().Get("Content-Encoding") != "" || !strings.Contains(w.Body.String(), "mermaid") || w.Body.Len() < 1<<20 {
		t.Fatalf("plain mermaid: %d bytes, encoding %q", w.Body.Len(), w.Header().Get("Content-Encoding"))
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/preview/mermaid.js", nil)
	r.Header.Set("Accept-Encoding", "gzip, br")
	h.ServeHTTP(w, r)
	zr, err := gzip.NewReader(w.Body)
	if err != nil || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip mermaid: %v", err)
	}
	if b, _ := io.ReadAll(zr); len(b) < 1<<20 {
		t.Fatal("gzip body too small")
	}
}

func TestProjectSaveFile(t *testing.T) {
	c := testController(t)
	p := &Project{ID: "proj_v", Name: "v", slot: make(chan struct{}, 1)}
	p.dir = filepath.Join(c.projectsDir(), p.ID)
	os.MkdirAll(p.repoDir(), 0o755)
	c.projects[p.ID] = p
	save := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/projects/proj_v/file", strings.NewReader(body))
		r.SetPathValue("id", p.ID)
		c.handleProjectSaveFile(w, r)
		return w
	}
	if w := save(`{"path":"site/index.html","content":"<h1>hi</h1>"}`); w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(p.repoDir(), "site", "index.html")); string(b) != "<h1>hi</h1>" {
		t.Fatalf("content %q", b)
	}
	w := save(`{"path":"site/index.html","content":"v2"}`)
	var res struct{ Replaced bool }
	json.Unmarshal(w.Body.Bytes(), &res)
	if !res.Replaced {
		t.Fatal("second save must say replaced")
	}
	for _, bad := range []string{`{"path":"","content":"x"}`, `{"path":".git/config","content":"x"}`} {
		if w := save(bad); w.Code != 400 {
			t.Fatalf("%s: %d", bad, w.Code)
		}
	}
	if w := save(`{"path":"../../outside.txt","content":"x"}`); w.Code != 200 || fileExists(filepath.Join(c.projectsDir(), "outside.txt")) {
		t.Fatalf("escape: %d", w.Code)
	}
	p.slot <- struct{}{} // a task is working
	if w := save(`{"path":"a.txt","content":"x"}`); w.Code != 409 {
		t.Fatalf("busy project: %d", w.Code)
	}
}
