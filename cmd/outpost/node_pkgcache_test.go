package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHash1KnownAnswer(t *testing.T) {
	// Checked against the go command (and an independent Python computation).
	if h := hash1Bytes("go.mod", []byte("module example.com/m\n")); h != "h1:flS2VctbRrTv+sBE+VKgxx6hlkMGPVz9MGOmzMYFg3k=" {
		t.Fatalf("hash1 = %s", h)
	}
}

func makeModZip(t *testing.T, p, prefix string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, _ := zw.Create(prefix + "/" + name)
		w.Write([]byte(body))
	}
	zw.Close()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, buf.Bytes(), 0o444); err != nil {
		t.Fatal(err)
	}
}

func TestPromoteGoModules(t *testing.T) {
	t.Setenv("GOSUMDB", "")
	t.Setenv("GONOSUMDB", "")
	t.Setenv("GOPRIVATE", "private.example/*")
	cache := t.TempDir()
	store := filepath.Join(t.TempDir(), "go")
	dl := filepath.Join(cache, "cache", "download")

	// example.com/Foo is stored escaped as example.com/!foo.
	good := filepath.Join(dl, "example.com", "!foo", "@v")
	mod := "module example.com/Foo\n"
	os.MkdirAll(good, 0o555)
	os.Chmod(good, 0o755)
	os.WriteFile(filepath.Join(good, "v1.0.0.mod"), []byte(mod), 0o444)
	makeModZip(t, filepath.Join(good, "v1.0.0.zip"), "example.com/Foo@v1.0.0", map[string]string{"go.mod": mod, "a.go": "package foo\n"})
	zipSum, err := hashZip(filepath.Join(good, "v1.0.0.zip"))
	if err != nil {
		t.Fatal(err)
	}
	// A tampered zip: the database has a different hash for it.
	bad := filepath.Join(dl, "example.com", "bad", "@v")
	os.MkdirAll(bad, 0o755)
	os.WriteFile(filepath.Join(bad, "v1.0.0.mod"), []byte("module example.com/bad\n"), 0o444)
	makeModZip(t, filepath.Join(bad, "v1.0.0.zip"), "example.com/bad@v1.0.0", map[string]string{"x.go": "evil"})
	// A private module is never looked up.
	priv := filepath.Join(dl, "private.example", "x", "@v")
	os.MkdirAll(priv, 0o755)
	os.WriteFile(filepath.Join(priv, "v1.0.0.mod"), []byte("module private.example/x\n"), 0o444)

	var looked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		looked = append(looked, r.URL.Path)
		switch r.URL.Path {
		case "/lookup/example.com/!foo@v1.0.0":
			fmt.Fprintf(w, "123\nexample.com/Foo v1.0.0 %s\nexample.com/Foo v1.0.0/go.mod %s\n\ngo.sum database tree\n", zipSum, hash1Bytes("go.mod", []byte(mod)))
		case "/lookup/example.com/bad@v1.0.0":
			fmt.Fprintf(w, "124\nexample.com/bad v1.0.0 h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\nexample.com/bad v1.0.0/go.mod %s\n", hash1Bytes("go.mod", []byte("module example.com/bad\n")))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := sumDBURL
	sumDBURL = srv.URL
	defer func() { sumDBURL = old }()

	if n := promoteGoModules(cache, store); n != 3 {
		t.Fatalf("added %d files, want 3 (Foo .mod and .zip, bad .mod)", n)
	}
	for _, f := range []string{"example.com/!foo/@v/v1.0.0.mod", "example.com/!foo/@v/v1.0.0.zip", "example.com/!foo/@v/v1.0.0.info", "example.com/bad/@v/v1.0.0.mod"} {
		if !fileExists(filepath.Join(store, filepath.FromSlash(f))) {
			t.Errorf("%s missing from the store", f)
		}
	}
	if fileExists(filepath.Join(store, "example.com", "bad", "@v", "v1.0.0.zip")) {
		t.Error("a zip that does not match the checksum database was stored")
	}
	if b, _ := os.ReadFile(filepath.Join(store, "example.com", "!foo", "@v", "list")); string(b) != "v1.0.0\n" {
		t.Errorf("list = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(store, "example.com", "!foo", "@v", "v1.0.0.info")); !strings.Contains(string(b), `"Version":"v1.0.0"`) {
		t.Errorf("info = %s", b)
	}
	for _, p := range looked {
		if strings.Contains(p, "private") {
			t.Error("a private module was sent to the checksum database")
		}
	}
	// A second run finds everything stored and asks nothing.
	looked = nil
	if n := promoteGoModules(cache, store); n != 0 || len(looked) != 1 { // only bad's zip is looked up again
		t.Errorf("second run added %d, lookups %v", n, looked)
	}

	t.Setenv("GOSUMDB", "off")
	looked = nil
	promoteGoModules(cache, filepath.Join(t.TempDir(), "go"))
	if len(looked) != 0 {
		t.Error("promoted with the checksum database turned off")
	}
}

func TestPromoteNPM(t *testing.T) {
	src := filepath.Join(t.TempDir(), "_cacache")
	dst := filepath.Join(t.TempDir(), "_cacache")
	put := func(content, digest string) string {
		p := filepath.Join(src, "content-v2", "sha512", digest[:2], digest[2:4], digest[4:])
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
		return p
	}
	sum := sha512.Sum512([]byte("tarball"))
	good := hex.EncodeToString(sum[:])
	put("tarball", good)
	other := sha512.Sum512([]byte("other"))
	bad := hex.EncodeToString(other[:])
	put("tampered", bad)
	if n := promoteNPM(src, dst); n != 1 {
		t.Fatalf("added %d, want 1", n)
	}
	if !fileExists(filepath.Join(dst, "content-v2", "sha512", good[:2], good[2:4], good[4:])) {
		t.Error("verified tarball not stored")
	}
	if fileExists(filepath.Join(dst, "content-v2", "sha512", bad[:2], bad[2:4], bad[4:])) {
		t.Error("tampered tarball stored")
	}
}

func waitGone(t *testing.T, p string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s was not deleted", p)
}

func TestCacheScopes(t *testing.T) {
	t.Setenv("GOSUMDB", "off")
	n := newTestPeerNode(t)

	// A task cache lives across jobs and goes when the task ends.
	d1, rel1 := n.acquireCache("task_a", "job1")
	d2, rel2 := n.acquireCache("task_a", "job2")
	if d1 != d2 {
		t.Fatal("two jobs of one task got different caches")
	}
	ro := filepath.Join(d1, "gomod", "x")
	os.MkdirAll(ro, 0o755)
	os.WriteFile(filepath.Join(ro, "f"), []byte("x"), 0o444)
	os.Chmod(ro, 0o555) // the go command leaves the module cache read-only
	rel1()
	n.closeCacheScope("task_a") // job2 still runs
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(d1); err != nil {
		t.Fatal("cache deleted while a job still used it")
	}
	rel2()
	waitGone(t, d1)

	// A job without a task gets a cache of its own, gone when it ends.
	d3, rel3 := n.acquireCache("", "job3")
	if !strings.HasSuffix(d3, "job_job3") {
		t.Fatalf("one-off cache %s", d3)
	}
	rel3()
	waitGone(t, d3)

	// A cache no one closes expires.
	d4, rel4 := n.acquireCache("task_b", "job4")
	rel4()
	n.mu.Lock()
	n.scopes["task_b"].last = time.Now().Add(-2 * cacheScopeTTL)
	n.mu.Unlock()
	n.expireCacheScopes()
	waitGone(t, d4)
}

func TestCacheSeededFromStore(t *testing.T) {
	n := newTestPeerNode(t)
	f := filepath.Join(n.pkgStoreDir(), "npm", "_cacache", "content-v2", "sha512", "ab", "cd", "ef")
	os.MkdirAll(filepath.Dir(f), 0o755)
	os.WriteFile(f, []byte("pkg"), 0o644)
	d, rel := n.acquireCache("task_c", "job1")
	defer rel()
	if b, err := os.ReadFile(filepath.Join(d, "npm", "_cacache", "content-v2", "sha512", "ab", "cd", "ef")); err != nil || string(b) != "pkg" {
		t.Fatalf("npm cache not seeded: %v", err)
	}
	env := strings.Join(n.cacheEnv(d, true), "\n")
	if !strings.Contains(env, "GOPROXY=file://") || !strings.Contains(env, "GOMODCACHE="+filepath.Join(d, "gomod")) {
		t.Errorf("env: %s", env)
	}
}

func TestCapStore(t *testing.T) {
	store := t.TempDir()
	v := filepath.Join(store, "go", "m", "@v")
	os.MkdirAll(v, 0o755)
	old := time.Now().Add(-time.Hour)
	for i, name := range []string{"v1.0.0.mod", "v1.0.0.zip", "v1.1.0.mod", "v1.1.0.zip"} {
		p := filepath.Join(v, name)
		os.WriteFile(p, bytes.Repeat([]byte("x"), 100), 0o644)
		if i < 2 {
			os.Chtimes(p, old, old)
		}
	}
	capStore(store, 250)
	if fileExists(filepath.Join(v, "v1.0.0.mod")) || !fileExists(filepath.Join(v, "v1.1.0.zip")) {
		t.Error("the cap should drop the oldest files first")
	}
	if b, _ := os.ReadFile(filepath.Join(v, "list")); string(b) != "v1.1.0\n" {
		t.Errorf("list = %q", b)
	}
}

func TestMatchGlobs(t *testing.T) {
	for _, c := range []struct {
		g, p string
		want bool
	}{
		{"private.example", "private.example/x/y", true},
		{"*.corp.example", "git.corp.example/a", true},
		{"github.com/me/*", "github.com/me/repo/sub", true},
		{"github.com/me/*", "github.com/other/repo", false},
		{"", "github.com/a/b", false},
	} {
		if got := matchGlobs(c.g, c.p); got != c.want {
			t.Errorf("matchGlobs(%q, %q) = %v", c.g, c.p, got)
		}
	}
}
