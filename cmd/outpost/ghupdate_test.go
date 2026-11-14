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
	"time"
)

func TestParseSums(t *testing.T) {
	h := hex.EncodeToString(make([]byte, 32))
	m := parseSums([]byte(h + "  outpost-linux-amd64\n" + h + " *outpost-windows-amd64.exe\nnot a line\n"))
	if m["outpost-linux-amd64"] != h || m["outpost-windows-amd64.exe"] != h || len(m) != 2 {
		t.Fatalf("got %v", m)
	}
}

// relabelTestBinary returns the test binary with its build marker changed to 99.0.0. The file
// keeps its length (NUL padding after the marker) so the ELF section headers at the end stay
// valid whatever version the build was stamped with, such as 0.13.16-dev.1.
func relabelTestBinary(exe string) ([]byte, string) {
	base, _ := os.ReadFile(exe)
	// Built at run time: a literal marker here would be found first by the other tests' binary scans.
	marker := fmt.Sprintf("%s|%s|", strings.Split(buildInfo, "|")[0], "99.0.0")
	repl := marker
	if n := len(buildInfo) - len(marker); n > 0 {
		repl += strings.Repeat("\x00", n)
	}
	bin := bytes.ReplaceAll(base, []byte(buildInfo), []byte(repl))
	return append(bin, []byte(marker+"\x00")...), marker
}

// ghStub serves a release whose binary is the test binary relabelled as version 99.0.0.
func ghStub(t *testing.T, badSum bool) (*httptest.Server, []byte) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin, _ := relabelTestBinary(exe)
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

func TestParseUpdateSource(t *testing.T) {
	ok := []struct{ in, kind, want, wantKind string }{
		{"", "", "https://github.com/" + ghDefaultRepo, kindGitHub},
		{"https://github.com/64fedoras/outpost", "", "https://github.com/64fedoras/outpost", kindGitHub},
		{"https://github.com/me/my.repo.git", "", "https://github.com/me/my.repo", kindGitHub},
		{"https://www.github.com/me/r/releases/tag/v1", "", "https://github.com/me/r", kindGitHub},
		{"github.com/me/r/", "", "https://github.com/me/r", kindGitHub},
		{"  me/r  ", "", "https://github.com/me/r", kindGitHub},
		{"git@github.com:me/r.git", "", "https://github.com/me/r", kindGitHub},
		{"https://gitlab.com/group/sub/repo/-/releases", "", "https://gitlab.com/group/sub/repo", kindGitLab},
		{"https://gitlab.com/group/repo.git", "", "https://gitlab.com/group/repo", kindGitLab},
		{"https://codeberg.org/me/r/releases", "", "https://codeberg.org/me/r", kindGitea},
		{"https://Gitea.Example.local:3000/me/r", "", "https://gitea.example.local:3000/me/r", ""},
		{"http://192.0.2.5:3000/me/r/releases", kindGitea, "http://192.0.2.5:3000/me/r", kindGitea},
		{"ssh://git@gitlab.example.local/a/b/c.git", kindGitLab, "https://gitlab.example.local/a/b/c", kindGitLab},
		{"https://ghe.example.local/org/r/tree/main", kindGitHub, "https://ghe.example.local/org/r", kindGitHub},
		{"https://gitlab.com/g/r", kindGitea, "https://gitlab.com/g/r", kindGitLab}, // a known host wins
	}
	for _, c := range ok {
		got, err := parseUpdateSource(c.in, c.kind)
		if err != nil || got.URL() != c.want || got.Kind != c.wantKind {
			t.Errorf("%q/%q -> %+v, %v (want %s %q)", c.in, c.kind, got, err, c.want, c.wantKind)
		}
	}
	for _, in := range []string{
		"http://github.com/me/r", "ftp://example.local/me/r", "https://github.com/me", "me",
		"https://github.com/me/r?x=1", "https://user:pw@github.com/me/r", "a/b/c", "../x", "me/..",
		"https://example.local/me", "https://exa mple.local/me/r", "https://example.local/me/%2e%2e",
		"https://-bad-.local/me/r", "file:///etc/passwd", "ssh://git@example.local:2222/me/r",
	} {
		if got, err := parseUpdateSource(in, ""); err == nil {
			t.Errorf("%q accepted as %+v", in, got)
		}
	}
	if _, err := parseUpdateSource("me/r", "svn"); err == nil {
		t.Error("unknown server type accepted")
	}
	// What older versions read stays the same for github.com.
	if s, _ := parseUpdateSource("https://github.com/me/r", ""); s.stored() != "me/r" {
		t.Errorf("stored %q", s.stored())
	}
	if s, _ := parseUpdateSource("", ""); s.stored() != "" {
		t.Errorf("default stored as %q", s.stored())
	}
}

// A private repository: the API wants the token, the asset must come from the API URL, and the
// token must not reach a different host.
func TestCheckGitHubPrivateRepoUsesToken(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses the test binary as an ELF")
	}
	exe, _ := os.Executable()
	bin, marker := relabelTestBinary(exe)
	_ = marker
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
	if resp, err := defaultSource().get(context.Background(), other.URL+"/x", "*/*", "sekrit"); err == nil {
		resp.Body.Close()
	}
}

func TestWaitNodeUpdates(t *testing.T) {
	oldPoll := nodeUpdatePoll
	nodeUpdatePoll = 5 * time.Millisecond
	defer func() { nodeUpdatePoll = oldPoll }()
	c := &Controller{updates: map[string]*UpdateStatus{"a": {State: "downloading"}}}
	go func() {
		time.Sleep(30 * time.Millisecond)
		c.mu.Lock()
		c.updates["a"].State = "restarting"
		c.mu.Unlock()
	}()
	if !c.waitNodeUpdates([]string{"a"}, 2*time.Second) {
		t.Fatal("expected the wait to settle once the node reached restarting")
	}
	c.updates["b"] = &UpdateStatus{State: "installing"}
	if c.waitNodeUpdates([]string{"b"}, 30*time.Millisecond) {
		t.Fatal("expected the wait to give up at the cap")
	}
	c.updates["c"] = &UpdateStatus{State: "failed"}
	if !c.waitNodeUpdates([]string{"c"}, time.Second) {
		t.Fatal("a failed update must not hold the controller")
	}
}

func TestNegotiateProtocol(t *testing.T) {
	if v, ok := negotiateProtocol(supportedProtocols()); !ok || v != ProtocolVersion {
		t.Fatalf("own list: %d %v", v, ok)
	}
	if _, ok := negotiateProtocol([]int{ProtocolVersion + 1}); ok {
		t.Fatal("a protocol newer than this build must be refused")
	}
	if v, ok := negotiateProtocol([]int{0, MinProtocolVersion, ProtocolVersion + 5}); !ok || v != MinProtocolVersion {
		t.Fatalf("overlap: %d %v", v, ok)
	}
}

func TestCompareVersionsPrerelease(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.12.18", "0.12.18-dev.57", 1},
		{"0.12.18-dev.57", "0.12.17", 1},
		{"0.12.18-dev.9", "0.12.18-dev.10", -1},
		{"0.12.18-dev.10", "0.12.18-dev.10", 0},
		{"0.12.10", "0.12.9", 1},
		{"0.12.19-dev.1", "0.12.18", 1},
	} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestGhWantFetch(t *testing.T) {
	cases := []struct {
		branch, have string
		ok           bool
		latest       string
		want         bool
	}{
		{"", "", false, "0.12.18", true},
		{"", "0.12.18", true, "0.12.18", false},
		{"", "0.12.17", true, "0.12.18", true},
		{"", "0.12.19-dev.3", true, "0.12.18", true}, // back to Stable replaces a branch build
		{"", "0.12.19", true, "0.12.18", false},      // a newer manual upload is left alone
		{"dev", "0.12.18", true, "0.12.19-dev.3", true},
		{"dev", "0.12.19-dev.3", true, "0.12.19-dev.3", false},
		{"dev", "0.12.20", true, "0.12.19-dev.3", true},
	}
	for i, c := range cases {
		if got := ghWantFetch(c.branch, c.have, c.ok, c.latest); got != c.want {
			t.Errorf("case %d: got %v want %v", i, got, c.want)
		}
	}
}

func TestBranchBuildsPicksNewestPrereleasePerBranch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+ghDefaultRepo+"/releases", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[
		 {"tag_name":"v0.12.18","prerelease":false},
		 {"tag_name":"v0.12.19-dev.9","prerelease":true},
		 {"tag_name":"v0.12.19-dev.10","prerelease":true},
		 {"tag_name":"v0.12.19-wip.1","prerelease":true},
		 {"tag_name":"v0.12.19-fake.2","prerelease":false},
		 {"tag_name":"v0.12.19-draft.3","prerelease":true,"draft":true},
		 {"tag_name":"junk","prerelease":true}]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("OUTPOST_GITHUB_API", srv.URL)
	c := &Controller{}
	b, err := c.ghBranchBuilds(context.Background(), defaultSource(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 2 || b["dev"].Tag != "v0.12.19-dev.10" || b["wip"].Tag != "v0.12.19-wip.1" {
		t.Fatalf("got %+v", b)
	}
}
