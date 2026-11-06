package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuildArtifactPlatform(t *testing.T) {
	for in, want := range map[string]string{
		"dist/outpost-linux-amd64":  "linux-amd64",
		"outpost-windows-amd64.exe": "windows-amd64",
		"outpost-windows-amd64":     "",
		"outpost-linux-amd64.exe":   "",
		"outpost-plan9-amd64":       "",
		"outpost-linux-amd64.zip":   "",
		"notoutpost-linux-amd64":    "",
		"outpost-darwin-arm64":      "darwin-arm64",
	} {
		if got := buildArtifactPlatform(in); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
}

func TestPublishArtifacts(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses the test binary as an ELF")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base, _ := os.ReadFile(exe)
	// The test binary carries its own build marker, so it passes as an Outpost build.
	good := append(append([]byte{}, base...), []byte(buildInfo+"\x00")...)

	c := &Controller{dataDir: t.TempDir(), releases: map[string]*Release{}}
	task := &Task{dir: t.TempDir()}
	write := func(name string, b []byte) Artifact {
		p := filepath.Join(task.artifactsDir(), name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, b, 0o644)
		sum, n, _ := fileSHA256(p)
		return Artifact{Path: name, Bytes: n, SHA256: sum}
	}
	plat := thisPlatform()
	ok := write("outpost-"+plat, good)
	noInfo := write("outpost-linux-arm64", base)             // an ELF for the wrong CPU and no marker
	wrongPlat := write("outpost-darwin-arm64", good)         // ELF named for macOS
	notBin := write("outpost-linux-amd64.txt", []byte("hi")) // ignored: not a binary name
	tampered := write("outpost-linux-arm64.bak", good)       // ignored too
	changed := ok
	changed.SHA256 = strings.Repeat("0", 64)
	changed.Path = "outpost-" + plat
	_ = tampered
	_ = notBin

	// A stale hash is refused and nothing is installed.
	task.Artifacts = []Artifact{changed}
	pub, refused := c.publishArtifacts(task)
	if len(pub) != 0 || len(refused) != 1 || len(c.releases) != 0 {
		t.Fatalf("stale hash accepted: %v %v", pub, refused)
	}

	task.Artifacts = []Artifact{ok, noInfo, wrongPlat, notBin, tampered}
	pub, refused = c.publishArtifacts(task)
	if len(pub) != 1 || pub[0].Platform != plat || pub[0].Version != Version {
		t.Fatalf("published %+v refused %v", pub, refused)
	}
	if _, bad := refused["outpost-darwin-arm64"]; !bad {
		t.Errorf("platform mismatch not refused: %v", refused)
	}
	if _, bad := refused["outpost-linux-arm64"]; !bad {
		t.Errorf("binary for another CPU not refused: %v", refused)
	}
	if len(refused) != 2 {
		t.Errorf("unexpected refusals: %v", refused)
	}
	if c.releases[plat] == nil {
		t.Fatal("release not registered")
	}
	if _, err := os.Stat(c.releasePath(plat)); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(c.releasesDir()); len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}
