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
	mux.HandleFunc("/repos/"+ghRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
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
