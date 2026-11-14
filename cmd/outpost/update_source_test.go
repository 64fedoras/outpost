package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"
)

// releaseStubBinary is the test binary relabelled as 99.0.0 with its SHA256SUMS line.
func releaseStubBinary(t *testing.T) (bin []byte, name, sums string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("uses the test binary as an ELF")
	}
	exe, _ := os.Executable()
	bin, _ = relabelTestBinary(exe)
	sum := sha256.Sum256(bin)
	name = releaseFileName(thisPlatform())
	return bin, name, hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

func stubController(t *testing.T, repo, kind, token string) *Controller {
	t.Helper()
	c := &Controller{dataDir: t.TempDir(), releases: map[string]*Release{}, nodes: map[string]*NodeRec{}}
	c.settings.GitHubRepo, c.settings.UpdateHostKind = repo, kind
	if token != "" {
		enc, err := c.encrypt(token)
		if err != nil {
			t.Fatal(err)
		}
		c.settings.GitHubTokenEnc = enc
	}
	return c
}

// A self-hosted Gitea/Forgejo on plain http: the type is found by asking the server, the token
// goes as "token X" to that server only, and the newest stable release wins over a newer prerelease.
func TestGiteaPrivateReleaseUpdate(t *testing.T) {
	bin, name, sums := releaseStubBinary(t)
	var srv *httptest.Server
	leaked := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked = true
		}
		w.Write([]byte(sums))
	}))
	defer other.Close()
	mux := http.NewServeMux()
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "token sekrit" {
				http.Error(w, "not found", 404)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/api/v1/version", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"version":"1.22.0"}`)) })
	mux.HandleFunc("/api/v1/repos/me/outpost", authed(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"private":true}`)) }))
	mux.HandleFunc("/api/v1/repos/me/outpost/releases", authed(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[
		 {"tag_name":"v99.1.0-dev.1","prerelease":true,"assets":[]},
		 {"tag_name":"v99.2.0","draft":true,"assets":[]},
		 {"tag_name":"v99.0.0","assets":[
		   {"name":"SHA256SUMS","browser_download_url":"%[1]s/me/outpost/releases/download/v99.0.0/SHA256SUMS"},
		   {"name":%[2]q,"browser_download_url":"%[1]s/me/outpost/releases/download/v99.0.0/%[2]s"}]},
		 {"tag_name":"v98.0.0","assets":[]}]`, srv.URL, name)
	}))
	mux.HandleFunc("/me/outpost/releases/download/v99.0.0/SHA256SUMS", authed(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/attachments/sums", http.StatusFound) // same origin: token kept
	}))
	mux.HandleFunc("/attachments/sums", authed(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(sums)) }))
	mux.HandleFunc("/me/outpost/releases/download/v99.0.0/"+name, authed(func(w http.ResponseWriter, r *http.Request) { w.Write(bin) }))
	mux.HandleFunc("/leak", authed(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL+"/x", http.StatusFound) }))
	srv = httptest.NewServer(mux)
	defer srv.Close()

	c := stubController(t, srv.URL+"/me/outpost/releases", "", "sekrit")
	if latest, err := c.checkGitHub(context.Background()); err != nil || latest != "99.0.0" {
		t.Fatalf("check: %q %v", latest, err)
	}
	if r := c.releases[thisPlatform()]; r == nil || r.Version != "99.0.0" {
		t.Fatalf("release not stored: %+v", r)
	}
	if v := c.ghView(); v["host"] != "Gitea" || v["repo"] != srv.URL+"/me/outpost" {
		t.Fatalf("view: %v %v", v["host"], v["repo"])
	}
	// Test connection with the saved token.
	rec := httptest.NewRecorder()
	c.handleTestGitHub(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Gitea, private") || !strings.Contains(rec.Body.String(), "http://") {
		t.Fatalf("test: %d %s", rec.Code, rec.Body)
	}
	// Another server in the test request never gets the saved token.
	rec = httptest.NewRecorder()
	c.handleTestGitHub(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{"repo":"`+other.URL+`/me/outpost","kind":"gitea"}`)))
	// A redirect off the server drops the token.
	src, _, _ := c.updResolved(context.Background())
	if resp, err := src.get(context.Background(), srv.URL+"/leak", "*/*", "sekrit"); err == nil {
		resp.Body.Close()
	}
	if leaked {
		t.Fatal("token reached another server")
	}
}

// GitLab: subgroups in the path, release files as links into the package registry, Bearer token.
func TestGitLabReleaseUpdate(t *testing.T) {
	bin, name, sums := releaseStubBinary(t)
	var srv *httptest.Server
	proj := "/api/v4/projects/" + url.PathEscape("team/tools/outpost")
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.EscapedPath()
		if p == "/api/v1/version" {
			http.NotFound(w, r)
			return
		}
		if p == "/api/v4/version" {
			http.Error(w, `{"message":"401 Unauthorized"}`, 401)
			return
		}
		if r.Header.Get("Authorization") != "Bearer glpat-x" {
			http.Error(w, `{"message":"404 Project Not Found"}`, 404)
			return
		}
		switch p {
		case proj:
			w.Write([]byte(`{"visibility":"internal"}`))
		case proj + "/releases":
			pkg := srv.URL + proj + "/packages/generic/outpost/99.0.0/"
			fmt.Fprintf(w, `[
			 {"tag_name":"v99.0.1-dev.4","assets":{"links":[]}},
			 {"tag_name":"v99.0.0","assets":{"links":[
			   {"name":"SHA256SUMS","url":"%[1]sSHA256SUMS","direct_asset_url":"%[2]s/team/tools/outpost/-/releases/v99.0.0/downloads/SHA256SUMS"},
			   {"name":%[3]q,"url":"%[1]s%[3]s","direct_asset_url":"%[2]s/team/tools/outpost/-/releases/v99.0.0/downloads/%[3]s"}]}}]`, pkg, srv.URL, name)
		case proj + "/packages/generic/outpost/99.0.0/SHA256SUMS":
			w.Write([]byte(sums))
		case proj + "/packages/generic/outpost/99.0.0/" + name:
			w.Write(bin)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := stubController(t, srv.URL+"/team/tools/outpost/-/releases", "", "glpat-x")
	if latest, err := c.checkGitHub(context.Background()); err != nil || latest != "99.0.0" {
		t.Fatalf("check: %q %v", latest, err)
	}
	rec := httptest.NewRecorder()
	c.handleTestGitHub(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "GitLab, private") {
		t.Fatalf("test: %d %s", rec.Code, rec.Body)
	}
	// Branch builds: GitLab has no prerelease flag, the tag suffix marks them.
	src, tok, err := c.updResolved(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.ghBranchBuilds(context.Background(), src, tok)
	if err != nil || b["dev"].Tag != "v99.0.1-dev.4" {
		t.Fatalf("branches: %v %v", b, err)
	}
	// Without the token: a message that says what to fix.
	c.settings.GitHubTokenEnc = ""
	if _, err := c.checkGitHub(context.Background()); err == nil || !strings.Contains(err.Error(), "GitLab") || !strings.Contains(err.Error(), "token") {
		t.Fatalf("no token: %v", err)
	}
}

// Moving the repository to another server removes the saved token unless a new one comes with it.
func TestUpdateRepoMoveDropsToken(t *testing.T) {
	c := stubController(t, "", "", "ghp-old")
	save := func(body string) map[string]any {
		rec := httptest.NewRecorder()
		c.handleSaveSettings(rec, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
		return c.settingsView()
	}
	if v := save(`{"github_repo":"https://github.com/me/fork"}`); v["github_token_set"] != true || c.settings.GitHubRepo != "me/fork" {
		t.Fatalf("same server: %v %q", v["github_token_set"], c.settings.GitHubRepo)
	}
	if v := save(`{"github_repo":"https://gitea.example.local/me/outpost","update_host_kind":"gitea"}`); v["github_token_set"] != false || v["github_repo"] != "https://gitea.example.local/me/outpost" || c.settings.UpdateHostKind != "gitea" {
		t.Fatalf("moved: %v %v %q", v["github_token_set"], v["github_repo"], c.settings.UpdateHostKind)
	}
	if v := save(`{"github_repo":"https://gitlab.com/g/r","update_host_kind":"gitea","github_token":"glpat-new"}`); v["github_token_set"] != true || c.settings.UpdateHostKind != "" {
		t.Fatalf("moved with token: %v %q", v["github_token_set"], c.settings.UpdateHostKind)
	}
	rec := httptest.NewRecorder()
	c.handleSaveSettings(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{"github_repo":"ftp://x.local/a/b"}`)))
	if rec.Code != 400 {
		t.Fatalf("bad address saved: %d", rec.Code)
	}
}

func TestUpdClientRedirects(t *testing.T) {
	check := updClient().CheckRedirect
	mk := func(u string) *http.Request {
		r, _ := http.NewRequest("GET", u, nil)
		r.Header.Set("Authorization", "Bearer x")
		return r
	}
	first := mk("https://git.example.local/a")
	if err := check(mk("http://git.example.local/b"), []*http.Request{first}); err == nil {
		t.Fatal("https to http allowed")
	}
	next := mk("https://git.example.local:8443/b")
	if err := check(next, []*http.Request{first}); err != nil || next.Header.Get("Authorization") != "" {
		t.Fatalf("other port kept the token: %v", err)
	}
	same := mk("https://git.example.local/c")
	if err := check(same, []*http.Request{first}); err != nil || same.Header.Get("Authorization") == "" {
		t.Fatalf("same origin lost the token: %v", err)
	}
}

func TestDetectHostKindUnknown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if _, err := resolveSource(context.Background(), updSource{Scheme: "http", Host: strings.TrimPrefix(srv.URL, "http://"), Path: "a/b"}); err == nil || !strings.Contains(err.Error(), "server type") {
		t.Fatalf("got %v", err)
	}
}
