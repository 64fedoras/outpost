package main

// The web UI's files are embedded in the binary. http.FileServer serves them
// with no Last-Modified (embedded files have no time) and no ETag, so with
// "no-cache" every page load downloaded the whole UI again (about 550 KB).
// staticHandler gives each file an ETag from its contents, so a reload costs
// a 304 per file, and sends a gzip copy (about 160 KB in all) to browsers
// that accept it. Both are computed once, on the first request.

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

type staticFile struct {
	body, gz []byte
	etag     string // of body; the gzip copy is etag with a -gz suffix
	ctype    string
}

func staticHandler(fsys fs.FS) http.Handler {
	files := http.FileServer(http.FS(fsys))
	var once sync.Once
	var assets map[string]*staticFile
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { assets = loadStatic(fsys) })
		// The UI ships inside the binary; make browsers revalidate so an update shows at once.
		w.Header().Set("Cache-Control", "no-cache")
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		f := assets[name]
		if f == nil || name == "index.html" && r.URL.Path != "/" {
			files.ServeHTTP(w, r) // folders, missing files and /index.html behave as before
			return
		}
		h := w.Header()
		h.Set("Content-Type", f.ctype)
		h.Set("Vary", "Accept-Encoding")
		body, etag := f.body, f.etag
		if f.gz != nil && acceptsGzip(r.Header.Get("Accept-Encoding")) {
			body, etag = f.gz, strings.TrimSuffix(f.etag, `"`)+`-gz"`
			h.Set("Content-Encoding", "gzip")
		}
		h.Set("ETag", etag)
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
	})
}

func loadStatic(fsys fs.FS) map[string]*staticFile {
	out := map[string]*staticFile{}
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(b)
		f := &staticFile{body: b, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
		f.ctype = mime.TypeByExtension(path.Ext(p))
		if f.ctype == "" {
			f.ctype = http.DetectContentType(b)
		}
		if compressible(f.ctype) && len(b) > 1024 {
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			if _, err := zw.Write(b); err == nil && zw.Close() == nil && buf.Len() < len(b)*9/10 {
				f.gz = buf.Bytes()
			}
		}
		out[p] = f
		return nil
	})
	return out
}

func compressible(ctype string) bool {
	return strings.HasPrefix(ctype, "text/") || strings.Contains(ctype, "javascript") ||
		strings.Contains(ctype, "json") || strings.Contains(ctype, "svg")
}

// acceptsGzip reads an Accept-Encoding header; "gzip;q=0" refuses it.
func acceptsGzip(ae string) bool {
	for _, part := range strings.Split(ae, ",") {
		enc, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(enc), "gzip") && strings.TrimSpace(enc) != "*" {
			continue
		}
		q := strings.ReplaceAll(strings.TrimSpace(params), " ", "")
		return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
	}
	return false
}
