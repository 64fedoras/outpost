package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var fixedTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// fakeHF serves a tree listing and one model file, like huggingface.co.
func fakeHF(t *testing.T, content []byte, corrupt bool) *httptest.Server {
	sum := sha256.Sum256(content)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/models/acme/coder-GGUF/tree/main"):
			json.NewEncoder(w).Encode([]map[string]any{
				{"type": "file", "path": "README.md", "size": 10},
				{"type": "file", "path": "coder-Q4_K_M.gguf", "size": len(content), "lfs": map[string]any{"oid": hex.EncodeToString(sum[:]), "size": len(content)}},
			})
		case r.URL.Path == "/acme/coder-GGUF/resolve/main/coder-Q4_K_M.gguf":
			body := content
			if corrupt {
				body = append([]byte{}, content...)
				body[0] ^= 0xff
			}
			http.ServeContent(w, r, "f", fixedTime, strings.NewReader(string(body)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func useHF(t *testing.T, srv *httptest.Server) {
	old := hfBase
	hfBase = srv.URL
	t.Cleanup(func() { hfBase = old })
}

func TestResolveDirectModel(t *testing.T) {
	content := []byte(strings.Repeat("gguf-bytes ", 5000))
	useHF(t, fakeHF(t, content, false))
	c := &Controller{dataDir: t.TempDir(), models: map[string]*ModelRec{}}
	m := &ModelRec{Name: "coder", Repo: "acme/coder-GGUF", HFFiles: []string{"coder-Q4_K_M.gguf"}, Storage: "direct"}
	c.models[m.Name] = m
	c.resolveModel(context.Background(), m)
	if m.Status != "ready" || len(m.Files) != 1 || m.Files[0].Bytes != int64(len(content)) || m.Files[0].SHA256 == "" {
		t.Fatalf("resolve: %+v err=%s", m.Files, m.Error)
	}
	if _, err := os.Stat(filepath.Join(c.modelsStoreDir(), "coder", "coder-Q4_K_M.gguf")); err == nil {
		t.Fatal("direct model stored a file on the controller")
	}
	m.HFFiles = []string{"missing.gguf"}
	c.resolveModel(context.Background(), m)
	if m.Status != "failed" {
		t.Fatal("missing file not reported")
	}
}

func testNode(t *testing.T) *Node {
	n := &Node{dataDir: t.TempDir(), jobs: map[string]context.CancelFunc{}, wsLastUsed: map[string]time.Time{}, stats: newStatSampler()}
	n.llama = newLlamaMgr(n)
	os.MkdirAll(n.modelsDir(), 0o755)
	return n
}

func TestNodeFetchesDirect(t *testing.T) {
	content := []byte(strings.Repeat("gguf-bytes ", 5000))
	srv := fakeHF(t, content, false)
	useHF(t, srv)
	c := &Controller{dataDir: t.TempDir(), models: map[string]*ModelRec{}}
	m := &ModelRec{Name: "coder", Repo: "acme/coder-GGUF", HFFiles: []string{"coder-Q4_K_M.gguf"}, Storage: "direct"}
	c.models[m.Name] = m
	c.resolveModel(context.Background(), m)

	n := testNode(t)
	// Simulate an interrupted earlier download: half the file in .part.
	dir := filepath.Join(n.modelsDir(), "coder")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "coder-Q4_K_M.gguf.part"), content[:len(content)/2], 0o644)
	f := m.Files[0]
	n.fetchModel(ModelFetch{Name: "coder", Files: []ModelFetchFile{{ModelFile: f, URL: hfURL(m.Repo, m.HFFiles[0])}}})
	got, err := os.ReadFile(filepath.Join(dir, "coder-Q4_K_M.gguf"))
	if err != nil || string(got) != string(content) {
		t.Fatalf("file wrong after resumed download: %v (%d bytes)", err, len(got))
	}
	if _, err := n.modelManifest("coder"); err != nil {
		t.Fatal("no manifest:", err)
	}
	if ms := n.listModels(); len(ms) != 1 || ms[0].Name != "coder" {
		t.Fatalf("listModels: %+v", ms)
	}
}

func TestNodeRejectsCorruptDownload(t *testing.T) {
	content := []byte(strings.Repeat("gguf-bytes ", 5000))
	good := fakeHF(t, content, false)
	useHF(t, good)
	c := &Controller{dataDir: t.TempDir(), models: map[string]*ModelRec{}}
	m := &ModelRec{Name: "coder", Repo: "acme/coder-GGUF", HFFiles: []string{"coder-Q4_K_M.gguf"}, Storage: "direct"}
	c.models[m.Name] = m
	c.resolveModel(context.Background(), m)

	bad := fakeHF(t, content, true)
	useHF(t, bad)
	n := testNode(t)
	n.fetchModel(ModelFetch{Name: "coder", Files: []ModelFetchFile{{ModelFile: m.Files[0], URL: hfURL(m.Repo, m.HFFiles[0])}}})
	if _, err := n.modelManifest("coder"); err == nil {
		t.Fatal("corrupt download accepted")
	}
	if _, err := os.Stat(filepath.Join(n.modelsDir(), "coder", "coder-Q4_K_M.gguf")); err == nil {
		t.Fatal("corrupt file left on disk")
	}
}

func TestNodeRefusesNonHFURL(t *testing.T) {
	n := testNode(t)
	n.fetchModel(ModelFetch{Name: "x", Files: []ModelFetchFile{{ModelFile: ModelFile{Path: "a.gguf", Bytes: 1}, URL: "https://evil.example/a.gguf"}}})
	if _, err := os.Stat(filepath.Join(n.modelsDir(), "x", "a.gguf")); err == nil {
		t.Fatal("downloaded from a non-Hugging-Face URL")
	}
	_ = fmt.Sprint()
}
