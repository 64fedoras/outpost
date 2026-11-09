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

func TestOpFailureSticksThroughRetries(t *testing.T) {
	c := &Controller{nodeOps: map[string]*nodeOpView{}, nodes: map[string]*NodeRec{}}
	c.setNodeOp("n", OpStatus{Op: "model.load", Target: "m", State: "failed", Error: "boom"})
	c.setNodeOp("n", OpStatus{Op: "runtime", Target: "v", State: "preparing"})
	c.setNodeOp("n", OpStatus{Op: "model.load", Target: "m", State: "failed", Error: "boom"})
	c.setNodeOp("n", OpStatus{Op: "runtime", Target: "v", State: "preparing"})
	lf := c.nodeOps["n"].LastFailure
	if lf == nil || lf.Count != 2 {
		t.Fatalf("failure should persist and count tries, got %+v", lf)
	}
	c.setNodeOp("n", OpStatus{Op: "model.load", Target: "m", State: "ready"})
	if c.nodeOps["n"].LastFailure != nil {
		t.Fatal("a successful load should clear the failure")
	}
}

func TestOldBuildsCannotHelpArm64(t *testing.T) {
	if !oldBuildsCannotHelp("linux-arm64-cpu") || oldBuildsCannotHelp("linux-x64-cpu") {
		t.Fatal("only Ubuntu arm64 builds are glibc 2.39 only")
	}
}
