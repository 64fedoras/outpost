package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// stubGitHub serves one release holding this test binary plus the build-info
// marker (an Outpost binary for this platform) plus SHA256SUMS. sumOverride replaces the
// listed checksum.
func stubGitHub(t *testing.T, sumOverride string) (*Controller, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	// The test binary lacks the build-info marker a release binary carries.
	body = append(body, []byte("\x00"+buildInfo)...)
	h := sha256.Sum256(body)
	sum := hex.EncodeToString(h[:])
	if sumOverride != "" {
		sum = sumOverride
	}
	goos, goarch, _ := strings.Cut(thisPlatform(), "-")
	name := "outpost-" + goos + "-" + goarch + exeSuffix(goos)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sekret" {
			http.Error(w, "no", 404)
			return
		}
		switch r.URL.Path {
		case "/repos/o/r/releases":
			fmt.Fprintf(w, `[{"tag_name":"v%s","assets":[
				{"id":1,"name":%q,"size":%d,"url":%q},
				{"id":2,"name":"SHA256SUMS","size":100,"url":%q}]}]`,
				Version, name, len(body), srv.URL+"/asset/1", srv.URL+"/asset/2")
		case "/asset/1":
			w.Write(body)
		case "/asset/2":
			fmt.Fprintf(w, "%s  %s\n", sum, name)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OUTPOST_GITHUB_API", srv.URL)
	c := &Controller{dataDir: t.TempDir(), releases: map[string]*Release{}}
	c.settings = defaultSettings()
	c.settings.GitHubRepo = "o/r"
	c.settings.GitHubTokenEnc, _ = c.encrypt("sekret")
	return c, name
}

func TestStageGitHubRelease(t *testing.T) {
	c, _ := stubGitHub(t, "")
	staged, err := c.stageGitHubRelease(context.Background(), "v"+Version)
	if err != nil || len(staged) != 1 || staged[0] != thisPlatform() {
		t.Fatalf("staged %v err %v", staged, err)
	}
	rel := c.releases[thisPlatform()]
	if rel == nil || rel.Version != Version || !strings.HasPrefix(rel.Source, "github") {
		t.Fatalf("release %+v", rel)
	}
	if _, err := os.Stat(c.releasePath(thisPlatform())); err != nil {
		t.Fatal(err)
	}
	// A second run finds identical bytes and stores nothing.
	if staged, err = c.stageGitHubRelease(context.Background(), "v"+Version); err != nil || len(staged) != 0 {
		t.Fatalf("second run staged %v err %v", staged, err)
	}
}

func TestStageGitHubRejectsBadChecksum(t *testing.T) {
	c, _ := stubGitHub(t, strings.Repeat("0", 64))
	_, err := c.stageGitHubRelease(context.Background(), "v"+Version)
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("err %v", err)
	}
	if len(c.releases) != 0 {
		t.Fatal("stored a binary that failed its checksum")
	}
}

func TestGitHubNeedsTokenForPrivateRepo(t *testing.T) {
	c, _ := stubGitHub(t, "")
	c.settings.GitHubTokenEnc = ""
	if _, err := c.outpostReleases(context.Background(), true); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("err %v", err)
	}
}

func TestNormalizeRepo(t *testing.T) {
	for in, want := range map[string]string{"": defaultOutpostRepo, "a/b": "a/b", "https://github.com/a/b.git": "a/b", "github.com/a/b/": "a/b"} {
		if got, err := normalizeRepo(in); err != nil || got != want {
			t.Errorf("%q -> %q %v", in, got, err)
		}
	}
	if _, err := normalizeRepo("a/b/c"); err == nil {
		t.Error("accepted a/b/c")
	}
}
