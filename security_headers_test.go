package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestSecureHeadersOnRoutes(t *testing.T) {
	c := authTestController(t)
	static, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(static)))
	mux.HandleFunc("GET /licenses", handleLicenses)
	mux.HandleFunc("POST /api/login", c.handleLogin)
	mux.HandleFunc("GET /api/state", c.auth(func(w http.ResponseWriter, r *http.Request) {}))
	h := secureHeaders(mux)
	for _, rc := range []struct{ method, path string }{
		{"GET", "/"}, {"GET", "/app.js"}, {"GET", "/style.css"}, {"GET", "/licenses"},
		{"POST", "/api/login"}, {"GET", "/api/state"}, {"GET", "/nope"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(rc.method, rc.path, strings.NewReader("{}")))
		for k, want := range map[string]string{
			"X-Frame-Options":        "DENY",
			"X-Content-Type-Options": "nosniff",
			"Referrer-Policy":        "no-referrer",
		} {
			if got := w.Header().Get(k); got != want {
				t.Errorf("%s %s: %s = %q, want %q", rc.method, rc.path, k, got, want)
			}
		}
		csp := w.Header().Get("Content-Security-Policy")
		for _, d := range []string{"default-src 'self'", "script-src 'self'", "style-src 'self'", "frame-ancestors 'none'", "base-uri 'none'"} {
			if !strings.Contains(csp, d) {
				t.Errorf("%s %s: CSP lacks %q: %s", rc.method, rc.path, d, csp)
			}
		}
		if strings.Contains(csp, "unsafe-") {
			t.Errorf("CSP must not allow unsafe-*: %s", csp)
		}
		if w.Header().Get("Strict-Transport-Security") != "" {
			t.Errorf("HSTS must stay off for a self-signed controller")
		}
	}
}

// The CSP forbids inline styles, scripts and event handlers, so the UI sources must not contain them.
func TestWebHasNoInlineCode(t *testing.T) {
	bad := regexp.MustCompile(`(?i)\sstyle\s*=|\son[a-z]+\s*=\s*["'\\]|javascript:|setAttribute\(\s*['"]style|cssText|<style|<script(\s[^>]*)?>\s*[^<\s]`)
	for _, name := range []string{"web/index.html", "web/app.js"} {
		b, err := webFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if m := bad.FindString(line); m != "" {
				t.Errorf("%s:%d has inline code %q (the CSP blocks it)", name, i+1, m)
			}
		}
	}
}
