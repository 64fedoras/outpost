package main

import (
	"strings"
	"testing"
)

func TestGlibcTooOld(t *testing.T) {
	out := "./llama-server: /lib/aarch64-linux-gnu/libc.so.6: version `GLIBC_2.38' not found (required by ./llama-server)"
	msg := glibcTooOld(out, "2.35")
	if !strings.HasPrefix(msg, glibcTooOldMarker) || !strings.Contains(msg, "2.38") || !strings.Contains(msg, "2.35") {
		t.Fatalf("bad message %q", msg)
	}
	if glibcTooOld("other failure", "2.35") != "" {
		t.Fatal("matched unrelated output")
	}
}

func TestOlderBuild(t *testing.T) {
	mk := func(tag string, has bool) ghRelease {
		r := ghRelease{TagName: tag}
		if has {
			r.Assets = append(r.Assets, struct {
				Name string `json:"name"`
				Size int64  `json:"size"`
				URL  string `json:"browser_download_url"`
			}{Name: "llama-" + tag + "-bin-ubuntu-arm64.tar.gz"})
		}
		return r
	}
	rels := []ghRelease{mk("b3", true), mk("b2", false), mk("b1", true)}
	if got := olderBuild(rels, "linux-arm64-cpu", "b3"); got != "b1" {
		t.Fatalf("got %q", got)
	}
	if got := olderBuild(rels, "linux-arm64-cpu", "b1"); got != "" {
		t.Fatalf("got %q", got)
	}
}
