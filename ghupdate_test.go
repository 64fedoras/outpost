package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestParseSums(t *testing.T) {
	h := hex.EncodeToString(make([]byte, 32))
	m := parseSums([]byte(h + "  outpost-linux-amd64\n" + h + " *outpost-windows-amd64.exe\nnot a line\n"))
	if m["outpost-linux-amd64"] != h || m["outpost-windows-amd64.exe"] != h || len(m) != 2 {
		t.Fatalf("got %v", m)
	}
}

// ghStub serves a release whose binary is the test binary relabelled as version 99.0.0.
func ghStub(t *testing.T, badSum bool) (*httptest.Server, []byte) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base, _ := os.ReadFile(exe)
	// Built at run time: a literal marker here would be found first by the other tests' binary scans.
	marker := fmt.Sprintf("%s|%s|", strings.Split(buildInfo, "|")[0], "99.0.0")
	bin := bytes.ReplaceAll(base, []byte(buildInfo), []byte(marker))
	bin = append(bin, []byte(marker+"\x00")...)
	sum := sha256.Sum256(bin)
	name := releaseFileName(thisPlatform())
	sums := hex.EncodeToString(sum[:]) + "  " + name + "\n"
	if badSum {
		sums = hex.EncodeToString(make([]byte, 32)) + "  " + name + "\n"
	}
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/"+ghDefaultRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v99.0.0","assets":[{"name":"SHA256SUMS","browser_download_url":"%s/dl/SHA256SUMS"},{"name":%q,"browser_download_url":"%s/dl/bin"}]}`, srv.URL, name, srv.URL)
	})
	mux.HandleFunc("/dl/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(sums)) })
	mux.HandleFunc("/dl/bin", func(w http.ResponseWriter, r *http.Request) { w.Write(bin) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, bin
}

func TestCheckGitHubStoresVerifiedRelease(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses the test binary as an ELF")
	}
	srv, _ := ghStub(t, false)
	t.Setenv("OUTPOST_GITHUB_API", srv.URL)
	c := &Controller{dataDir: t.TempDir(), releases: map[string]*Release{}, nodes: map[string]*NodeRec{}}
	latest, err := c.checkGitHub(context.Background())
	if err != nil || latest != "99.0.0" {
		t.Fatalf("got %q %v", latest, err)
	}
	if r := c.releases[thisPlatform()]; r == nil || r.Version != "99.0.0" {
		t.Fatalf("release not stored: %v", c.releases)
	}
	// Already current: a second check downloads nothing.
	if _, err := c.checkGitHub(context.Background()); err != nil || len(c.ghState.Fetched) != 0 {
		t.Fatalf("second check: %v %v", err, c.ghState.Fetched)
	}
}

func TestCheckGitHubRejectsBadChecksum(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses the test binary as an ELF")
	}
	srv, _ := ghStub(t, true)
	t.Setenv("OUTPOST_GITHUB_API", srv.URL)
	c := &Controller{dataDir: t.TempDir(), releases: map[string]*Release{}, nodes: map[string]*NodeRec{}}
	if _, err := c.checkGitHub(context.Background()); err == nil {
		t.Fatal("accepted a binary that does not match SHA256SUMS")
	}
	if len(c.releases) != 0 {
		t.Fatal("stored an unverified release")
	}
}

func TestRollbackHoldBlocksOlderAutoInstall(t *testing.T) {
	c := &Controller{dataDir: t.TempDir()}
	c.holdAfterRollbackLocked("0.8.5")
	c.holdAfterRollbackLocked("0.8.3") // never lowers the hold
	if c.settings.GitHubHoldVersion != "0.8.5" {
		t.Fatalf("hold = %q", c.settings.GitHubHoldVersion)
	}
}

func TestParseGitHubRepo(t *testing.T) {
	ok := map[string]string{
		"":                                     ghDefaultRepo,
		"https://github.com/64fedoras/outpost": "64fedoras/outpost",
		"https://github.com/me/my.repo.git":    "me/my.repo",
		"https://www.github.com/me/r/releases/tag/v1": "me/r",
		"github.com/me/r/":                            "me/r",
		"  me/r  ":                                    "me/r",
	}
	for in, want := range ok {
		if got, err := parseGitHubRepo(in); err != nil || got != want {
			t.Errorf("%q -> %q, %v (want %q)", in, got, err, want)
		}
	}
	for _, in := range []string{
		"http://github.com/me/r", "https://gitlab.com/me/r", "https://evil.example/github.com/me/r",
		"git@github.com:me/r.git", "https://github.com/me", "me", "https://github.com/me/r?x=1",
		"https://user:pw@github.com/me/r", "a/b/c", "../x", "me/..",
	} {
		if got, err := parseGitHubRepo(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

// A private repository: the API wants the token, the asset must come from the API URL, and the
// token must not reach a different host.
func TestCheckGitHubPrivateRepoUsesToken(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses the test binary as an ELF")
	}
	exe, _ := os.Executable()
	base, _ := os.ReadFile(exe)
	marker := fmt.Sprintf("%s|%s|", strings.Split(buildInfo, "|")[0], "99.0.0")
	bin := bytes.ReplaceAll(base, []byte(buildInfo), []byte(marker))
	bin = append(bin, []byte(marker+"\x00")...)
	sum := sha256.Sum256(bin)
	name := releaseFileName(thisPlatform())
	sums := hex.EncodeToString(sum[:]) + "  " + name + "\n"
	var srv *httptest.Server
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer sekrit" {
				http.Error(w, "nope", 404)
				return
			}
			h(w, r)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/me/priv", auth(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"private":true}`)) }))
	mux.HandleFunc("/repos/me/priv/releases/latest", auth(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v99.0.0","assets":[{"name":"SHA256SUMS","url":"%s/repos/me/priv/releases/assets/1","browser_download_url":"%s/web/1"},{"name":%q,"url":"%s/repos/me/priv/releases/assets/2","browser_download_url":"%s/web/2"}]}`, srv.URL, srv.URL, name, srv.URL, srv.URL)
	}))
	mux.HandleFunc("/repos/me/priv/releases/assets/1", auth(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(sums)) }))
	mux.HandleFunc("/repos/me/priv/releases/assets/2", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/octet-stream" {
			http.Error(w, "accept", 415)
			return
		}
		w.Write(bin)
	}))
	srv = httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("OUTPOST_GITHUB_API", srv.URL)
	c := &Controller{dataDir: t.TempDir(), releases: map[string]*Release{}, nodes: map[string]*NodeRec{}}
	enc, err := c.encrypt("sekrit")
	if err != nil {
		t.Fatal(err)
	}
	c.settings.GitHubRepo = "me/priv"
	if _, err := c.checkGitHub(context.Background()); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("without a token expected a helpful error, got %v", err)
	}
	c.settings.GitHubTokenEnc = enc
	if latest, err := c.checkGitHub(context.Background()); err != nil || latest != "99.0.0" {
		t.Fatalf("with token: %q %v", latest, err)
	}
	// Test connection: saved token is used when the request has none; a bad one is reported.
	rec := httptest.NewRecorder()
	c.handleTestGitHub(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{"repo":"https://github.com/me/priv"}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "private") {
		t.Fatalf("test: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	c.handleTestGitHub(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{"token":"wrong"}`)))
	if rec.Code != 502 {
		t.Fatalf("bad token: %d %s", rec.Code, rec.Body)
	}
	// The token is never sent to a non-API address.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("token leaked to another host")
		}
	}))
	defer other.Close()
	if resp, err := c.ghGet(context.Background(), other.URL+"/x", "*/*", "sekrit"); err == nil {
		resp.Body.Close()
	}
}
