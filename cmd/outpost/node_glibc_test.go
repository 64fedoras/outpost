package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestCompatVariants(t *testing.T) {
	cases := []struct {
		h    Hello
		want string
	}{
		{Hello{OS: "linux", Arch: "arm64", Glibc: "2.35"}, "linux-arm64-cpu-compat"},
		{Hello{OS: "linux", Arch: "arm64", Glibc: "2.35", VulkanAvailable: true, GPUs: []GPUInfo{{Vendor: "nvidia"}}}, "linux-arm64-cpu-compat"},
		{Hello{OS: "linux", Arch: "arm64", Glibc: "2.39"}, "linux-arm64-cpu"},
		{Hello{OS: "linux", Arch: "arm64"}, "linux-arm64-cpu"}, // older node: glibc unknown, the install error decides
		{Hello{OS: "linux", Arch: "amd64", Glibc: "2.31"}, "linux-x64-cpu"},
		{Hello{OS: "darwin", Arch: "amd64", NoAVX2: true}, "macos-x64-compat"},
		{Hello{OS: "darwin", Arch: "amd64"}, "macos-x64"},
		{Hello{OS: "darwin", Arch: "arm64", NoAVX2: true}, "macos-arm64"},
	}
	for _, c := range cases {
		if got, _ := nodeVariant(&c.h); got != c.want {
			t.Errorf("%+v: got %s want %s", c.h, got, c.want)
		}
	}
	if compatOf("linux-arm64-cpu") != "linux-arm64-cpu-compat" || compatOf("macos-x64") != "macos-x64-compat" ||
		compatOf("linux-arm64-vulkan") != "linux-arm64-cpu-compat" || compatOf("linux-x64-cpu") != "" || compatOf("macos-x64-compat") != "" {
		t.Fatal("compatOf")
	}
}

func TestFlagsLackAVX2(t *testing.T) {
	has := "processor : 0\nflags\t\t: fpu avx avx2 fma\n"
	no := "processor : 0\nflags\t\t: fpu avx f16c\n"
	if flagsLackAVX2(has, "flags") || !flagsLackAVX2(no, "flags") || flagsLackAVX2("nothing here", "flags") {
		t.Fatal("flagsLackAVX2")
	}
}

func TestCompatReleaseWithoutUpstreamEntry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"tag_name":"llama-compat-v1.0.0","assets":[{"name":"llama-v1.0.0-bin-ubuntu22-arm64.tar.gz","size":5,"browser_download_url":"https://example.invalid/a"}]},
		 {"tag_name":"llama-compat-b2","assets":[{"name":"llama-b2-bin-ubuntu22-arm64.tar.gz","size":5,"browser_download_url":"https://example.invalid/b"}]}]`)
	}))
	defer srv.Close()
	c := &Controller{}
	up := ghRelease{TagName: "b2"}
	rels := c.mergeCompatAssets(context.Background(), srv.URL, []ghRelease{{TagName: "b3"}, up})
	// b2 is in the upstream list: its asset is merged. v1.0.0 is not: it becomes a release of its own.
	build, _, _, _ := newestAsset(rels, "linux-arm64-cpu-compat")
	if build != "b2" {
		t.Fatalf("newest compat build %q, want b2 (the upstream-ordered list comes first)", build)
	}
	if name, _, _ := assetFor(rels, "linux-arm64-cpu-compat", "v1.0.0"); name == "" {
		t.Fatal("a compat release whose upstream tag is not in the recent list must still be found")
	}
	if len(rels) != 3 {
		t.Fatalf("got %d releases", len(rels))
	}
}
