package main

import (
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticHandlerETagAndGzip(t *testing.T) {
	static, _ := fs.Sub(webFS, "web")
	h := staticHandler(static)
	get := func(path string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	want, _ := fs.ReadFile(static, "app.js")

	plain := get("/app.js", nil)
	if plain.Code != 200 || plain.Body.String() != string(want) || plain.Header().Get("Content-Encoding") != "" {
		t.Fatalf("plain: %d %q", plain.Code, plain.Header().Get("Content-Encoding"))
	}
	if !strings.Contains(plain.Header().Get("Content-Type"), "javascript") || plain.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("headers: %v", plain.Header())
	}
	etag := plain.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	if w := get("/app.js", map[string]string{"If-None-Match": etag}); w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Fatalf("revalidate: %d, %d bytes", w.Code, w.Body.Len())
	}

	gz := get("/app.js", map[string]string{"Accept-Encoding": "gzip, deflate, br"})
	if gz.Header().Get("Content-Encoding") != "gzip" || gz.Header().Get("ETag") == etag || gz.Body.Len() >= len(want) {
		t.Fatalf("gzip: %q etag %q, %d bytes", gz.Header().Get("Content-Encoding"), gz.Header().Get("ETag"), gz.Body.Len())
	}
	zr, err := gzip.NewReader(gz.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(zr); string(got) != string(want) {
		t.Fatal("gzip body differs")
	}
	if w := get("/app.js", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": gz.Header().Get("ETag")}); w.Code != http.StatusNotModified {
		t.Fatalf("gzip revalidate: %d", w.Code)
	}
	if w := get("/app.js", map[string]string{"Accept-Encoding": "gzip;q=0"}); w.Header().Get("Content-Encoding") != "" {
		t.Fatal("gzip;q=0 still got gzip")
	}

	// The page itself, and the old behaviour for missing files.
	if w := get("/", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "<html") || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("index: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w := get("/no-such-file.js", nil); w.Code != 404 {
		t.Fatalf("missing file: %d", w.Code)
	}
	if w := get("/index.html", nil); w.Code != http.StatusMovedPermanently {
		t.Fatalf("/index.html: %d", w.Code)
	}
}

func TestAcceptsGzip(t *testing.T) {
	for ae, want := range map[string]bool{"": false, "gzip": true, "deflate, gzip;q=1.0": true, "GZIP": true, "br": false, "gzip;q=0": false, "gzip; q=0": false, "*": true} {
		if got := acceptsGzip(ae); got != want {
			t.Errorf("%q: %v", ae, got)
		}
	}
}
