package main

// llama.cpp builds: the controller picks the newest release on GitHub that has
// a binary for a node's platform/GPU, downloads it once, and serves it to nodes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// runtimeAssets maps an Outpost variant to the llama.cpp release asset suffix.
var runtimeAssets = map[string]string{
	"linux-x64-cpu":      "-bin-ubuntu-x64.tar.gz",
	"linux-x64-vulkan":   "-bin-ubuntu-vulkan-x64.tar.gz",
	"linux-arm64-cpu":    "-bin-ubuntu-arm64.tar.gz",
	"linux-arm64-vulkan": "-bin-ubuntu-vulkan-arm64.tar.gz",
	"win-x64-cpu":        "-bin-win-cpu-x64.zip",
	"win-x64-vulkan":     "-bin-win-vulkan-x64.zip",
	"win-arm64-cpu":      "-bin-win-cpu-arm64.zip",
	"macos-arm64":        "-bin-macos-arm64.tar.gz",
	"macos-x64":          "-bin-macos-x64.tar.gz",
	// Compatible builds made by Outpost's own CI (.github/workflows/llama-compat.yml) for
	// machines the upstream releases cannot run on: Ubuntu arm64 older than 24.04 (glibc below
	// 2.38, e.g. a Jetson on JetPack 6) and Intel Macs without AVX2 (Mac Pro 2013 and older).
	// (Only a CPU build for arm64: an older Ubuntu has no usable Vulkan toolchain in CI.)
	"linux-arm64-cpu-compat": "-bin-ubuntu22-arm64.tar.gz",
	"macos-x64-compat":       "-bin-macos-x64-noavx2.tar.gz",
	// The same AVX-only build with Metal on, for Intel Macs with a discrete GPU. Used only on a
	// node that opted in (a file named "metal" in its data folder); see metalOptIn.
	"macos-x64-compat-metal": "-bin-macos-x64-noavx2-metal.tar.gz",
}

// upstreamGlibcArm64 is the glibc the upstream Ubuntu arm64 releases need (built on Ubuntu 24.04).
const upstreamGlibcArm64 = "2.38"

// compatOf returns the compatible-build variant for variant, or "" when there is none.
func compatOf(variant string) string {
	switch variant {
	case "linux-arm64-cpu", "linux-arm64-vulkan":
		return "linux-arm64-cpu-compat"
	case "macos-x64":
		return "macos-x64-compat"
	}
	return ""
}

// glibcBelow reports whether the node's glibc is known and older than want.
func glibcBelow(have, want string) bool { return have != "" && compareVersions(have, want) < 0 }

// nodeVariant picks the llama.cpp build for a node. GPU nodes get Vulkan,
// which covers AMD, Intel and NVIDIA cards without vendor toolkits.
func nodeVariant(h *Hello) (variant, note string) {
	hasGPU := false
	for _, g := range h.GPUs {
		if g.Vendor != "apple" {
			hasGPU = true
		}
	}
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[h.Arch]
	if arch == "" {
		return "", "unsupported CPU architecture"
	}
	switch h.OS {
	case "linux":
		v, note := "linux-"+arch+"-cpu", ""
		if hasGPU && h.VulkanAvailable {
			v = "linux-" + arch + "-vulkan"
		} else if hasGPU {
			note = "GPU found but no Vulkan driver; install the vulkan loader and mesa drivers (sudo apt install libvulkan1 mesa-vulkan-drivers) to use it"
		}
		if arch == "arm64" && glibcBelow(h.Glibc, upstreamGlibcArm64) {
			v = "linux-arm64-cpu-compat"
			note = "this system's glibc " + h.Glibc + " is too old for the upstream llama.cpp builds, so Outpost uses its own CPU-only build; GPU use needs Ubuntu 24.04 or newer"
		}
		return v, note
	case "windows":
		if hasGPU && arch == "x64" {
			return "win-x64-vulkan", ""
		}
		return "win-" + arch + "-cpu", ""
	case "darwin":
		if arch == "x64" && h.NoAVX2 {
			if h.MetalOptIn && len(h.GPUs) > 0 {
				return "macos-x64-compat-metal", "experimental: the GPUs are used through Metal; delete the \"metal\" file in the node's data folder to go back to the CPU build"
			}
			return "macos-x64-compat", ""
		}
		return "macos-" + arch, ""
	}
	return "", "unsupported OS"
}

type RuntimeRec struct {
	Variant    string    `json:"variant"`
	Build      string    `json:"build"`
	Archive    string    `json:"archive"`
	SHA256     string    `json:"sha256"`
	Bytes      int64     `json:"bytes"`
	Downloaded time.Time `json:"downloaded"`
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

type blockedBuild struct {
	msg string
	at  time.Time
}

type runtimeCache struct {
	mu       sync.Mutex
	checked  time.Time
	releases []ghRelease
	locks    map[string]*sync.Mutex
	ceiling  map[string]string       // node|variant -> newest build known to run on that node (older glibc)
	blocked  map[string]blockedBuild // node|variant|build -> this build needs a newer glibc and no older release can help
}

func (c *Controller) runtimeDir() string { return filepath.Join(c.dataDir, "runtime") }

func (c *Controller) loadRuntimes() {
	c.runtimes = map[string]*RuntimeRec{}
	c.rtCache = &runtimeCache{locks: map[string]*sync.Mutex{}, ceiling: map[string]string{}, blocked: map[string]blockedBuild{}}
	entries, _ := os.ReadDir(c.runtimeDir())
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(c.runtimeDir(), e.Name(), "runtime.json"))
		if err != nil {
			continue
		}
		var r RuntimeRec
		if json.Unmarshal(b, &r) == nil && r.Variant == e.Name() {
			c.runtimes[r.Variant] = &r
		}
	}
}

// latestReleases lists recent llama.cpp releases (cached for an hour).
func (c *Controller) latestReleases(ctx context.Context, force bool) ([]ghRelease, error) {
	rc := c.rtCache
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if !force && rc.releases != nil && time.Since(rc.checked) < time.Hour {
		return rc.releases, nil
	}
	base := "https://api.github.com"
	if u := os.Getenv("OUTPOST_GITHUB_API"); u != "" { // for testing against a stub
		base = strings.TrimRight(u, "/")
	}
	// The compatible-builds list does not depend on the upstream one, so fetch both at once.
	var compat []ghRelease
	done := make(chan struct{})
	go func() {
		defer close(done)
		compat = c.fetchCompatReleases(ctx, base)
	}()
	rels, err := fetchGHReleases(ctx, base+"/repos/ggml-org/llama.cpp/releases?per_page=20")
	<-done
	if err != nil {
		return nil, err
	}
	rels = mergeCompat(rels, compat)
	rc.releases, rc.checked = rels, time.Now()
	return rels, nil
}

func fetchGHReleases(ctx context.Context, url string) ([]ghRelease, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "outpost/"+Version)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub: %s", resp.Status)
	}
	var rels []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return nil, err
	}
	return rels, nil
}

// compatRepo is where Outpost's CI publishes the compatible llama.cpp builds.
func compatRepo() string {
	if r := os.Getenv("OUTPOST_COMPAT_REPO"); r != "" {
		return r
	}
	return ghDefaultRepo
}

// mergeCompatAssets fetches the compatible builds and merges them into rels.
func (c *Controller) mergeCompatAssets(ctx context.Context, base string, rels []ghRelease) []ghRelease {
	return mergeCompat(rels, c.fetchCompatReleases(ctx, base))
}

// fetchCompatReleases lists the compatible builds. A failure here only means nodes that need a
// compatible build cannot get one yet; upstream builds are unaffected, so it returns nothing.
func (c *Controller) fetchCompatReleases(ctx context.Context, base string) []ghRelease {
	compat, err := fetchGHReleases(ctx, base+"/repos/"+compatRepo()+"/releases?per_page=50")
	if err != nil {
		log.Printf("compatible llama.cpp builds: %v", err)
		return nil
	}
	return compat
}

// mergeCompat adds the compatible builds to the matching upstream releases, so every
// later lookup (newest, older, common build) treats them as one more asset of that release.
// Releases there are tagged llama-compat-<upstream tag>. When the upstream release is not
// in the recent list (llama.cpp publishes many builds a day, so a tag can scroll off, or
// the newest tag may not be the newest release) the compatible build is added as a release
// of its own, after the upstream ones.
func mergeCompat(rels, compat []ghRelease) []ghRelease {
	merged := map[string]bool{}
	for _, r := range compat { // newest first, as GitHub lists them
		t, ok := strings.CutPrefix(r.TagName, "llama-compat-")
		if !ok || len(r.Assets) == 0 {
			continue
		}
		found := false
		for i := range rels {
			if rels[i].TagName == t {
				rels[i].Assets = append(rels[i].Assets, r.Assets...)
				found = true
			}
		}
		if !found && !merged[t] {
			rels = append(rels, ghRelease{TagName: t, Assets: r.Assets})
		}
		merged[t] = true
	}
	return rels
}

// newestAsset finds the newest release with a binary for variant.
func newestAsset(rels []ghRelease, variant string) (build, name, url string, size int64) {
	suffix := runtimeAssets[variant]
	for _, r := range rels {
		for _, a := range r.Assets {
			if strings.HasSuffix(a.Name, suffix) && !strings.HasPrefix(a.Name, "cudart") {
				return r.TagName, a.Name, a.URL, a.Size
			}
		}
	}
	return "", "", "", 0
}

// olderBuild returns the next release after current (listed newest first)
// that has a binary for variant.
func olderBuild(rels []ghRelease, variant, current string) string {
	suffix := runtimeAssets[variant]
	seen := false
	for _, r := range rels {
		if !seen {
			seen = r.TagName == current
			continue
		}
		for _, a := range r.Assets {
			if strings.HasSuffix(a.Name, suffix) && !strings.HasPrefix(a.Name, "cudart") {
				return r.TagName
			}
		}
	}
	return ""
}

func (c *Controller) runtimeCeiling(node, variant string) string {
	c.rtCache.mu.Lock()
	defer c.rtCache.mu.Unlock()
	return c.rtCache.ceiling[node+"|"+variant]
}

func (c *Controller) setRuntimeCeiling(node, variant, build string) {
	c.rtCache.mu.Lock()
	c.rtCache.ceiling[node+"|"+variant] = build
	c.rtCache.mu.Unlock()
}

func (c *Controller) runtimeBlocked(node, variant, build string) string {
	c.rtCache.mu.Lock()
	defer c.rtCache.mu.Unlock()
	b := c.rtCache.blocked[node+"|"+variant+"|"+build]
	if time.Since(b.at) > 10*time.Minute { // the OS may have been upgraded since
		return ""
	}
	return b.msg
}

func (c *Controller) setRuntimeBlocked(node, variant, build, msg string) {
	c.rtCache.mu.Lock()
	if c.rtCache.blocked == nil {
		c.rtCache.blocked = map[string]blockedBuild{}
	}
	c.rtCache.blocked[node+"|"+variant+"|"+build] = blockedBuild{msg, time.Now()}
	c.rtCache.mu.Unlock()
}

// assetFor finds variant's binary in a specific release.
func assetFor(rels []ghRelease, variant, build string) (name, url string, size int64) {
	suffix := runtimeAssets[variant]
	for _, r := range rels {
		if r.TagName != build {
			continue
		}
		for _, a := range r.Assets {
			if strings.HasSuffix(a.Name, suffix) && !strings.HasPrefix(a.Name, "cudart") {
				return a.Name, a.URL, a.Size
			}
		}
	}
	return "", "", 0
}

// commonBuild is the newest release that has binaries for every variant, so
// pooled nodes speak the same RPC protocol.
func commonBuild(rels []ghRelease, variants []string) string {
	for _, r := range rels {
		ok := true
		for _, v := range variants {
			if name, _, _ := assetFor([]ghRelease{r}, v, r.TagName); name == "" {
				ok = false
				break
			}
		}
		if ok {
			return r.TagName
		}
	}
	return ""
}

// ensureRuntimeBuild makes sure a build for variant is on the controller: the
// given build, or the newest one when build is "".
func (c *Controller) ensureRuntimeBuild(ctx context.Context, variant, wantBuild string) (*RuntimeRec, error) {
	if _, ok := runtimeAssets[variant]; !ok {
		return nil, fmt.Errorf("unknown variant %s", variant)
	}
	c.rtCache.mu.Lock()
	lk := c.rtCache.locks[variant]
	if lk == nil {
		lk = &sync.Mutex{}
		c.rtCache.locks[variant] = lk
	}
	c.rtCache.mu.Unlock()
	lk.Lock()
	defer lk.Unlock()

	c.mu.Lock()
	have := c.runtimes[variant]
	c.mu.Unlock()
	if have != nil && wantBuild != "" && have.Build == wantBuild {
		return have, nil
	}
	rels, err := c.latestReleases(ctx, false)
	if err != nil {
		if have != nil {
			log.Printf("llama.cpp release check failed (%v); using %s", err, have.Build)
			return have, nil
		}
		return nil, err
	}
	build, name, url, size := newestAsset(rels, variant)
	if wantBuild != "" {
		build = wantBuild
		name, url, size = assetFor(rels, variant, wantBuild)
		if name == "" {
			return nil, fmt.Errorf("llama.cpp %s has no %s build", wantBuild, variant)
		}
	}
	if build == "" {
		if have != nil {
			return have, nil
		}
		return nil, fmt.Errorf("no recent llama.cpp release has a %s build", variant)
	}
	if have != nil && have.Build == build {
		return have, nil
	}
	dir := filepath.Join(c.runtimeDir(), variant)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	log.Printf("downloading llama.cpp %s for %s (%s)", build, variant, humanBytes(size))
	sum, err := downloadResumable(ctx, &http.Client{}, url, "", filepath.Join(dir, name), size, func(int64) {})
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	rec := &RuntimeRec{Variant: variant, Build: build, Archive: name, SHA256: sum, Bytes: size, Downloaded: time.Now()}
	b, _ := json.MarshalIndent(rec, "", "  ")
	if err := writeFileAtomic(filepath.Join(dir, "runtime.json"), b, 0o644); err != nil {
		return nil, err
	}
	// Drop older archives for this variant.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != name && e.Name() != "runtime.json" {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	c.mu.Lock()
	c.runtimes[variant] = rec
	c.mu.Unlock()
	return rec, nil
}

func (c *Controller) handleServeRuntime(w http.ResponseWriter, r *http.Request) {
	if c.nodeFromBearer(r) == nil {
		writeJSON(w, 401, ErrInfo{Code: "bad_key", Message: "unknown node key"})
		return
	}
	c.mu.Lock()
	rt := c.runtimes[r.PathValue("variant")]
	c.mu.Unlock()
	if rt == nil {
		writeJSON(w, 404, ErrInfo{Code: "no_runtime", Message: "that llama.cpp build is not on the controller"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, filepath.Join(c.runtimeDir(), rt.Variant, rt.Archive))
}

// handleCheckRuntime refreshes the release list and reports the newest build.
func (c *Controller) handleCheckRuntime(w http.ResponseWriter, r *http.Request) {
	rels, err := c.latestReleases(r.Context(), true)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	build, _, _, _ := newestAsset(rels, "linux-x64-cpu")
	if build == "" {
		writeJSON(w, 502, map[string]string{"error": errors.New("no llama.cpp release with binaries found").Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"latest": build})
}
