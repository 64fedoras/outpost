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
}

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
		if hasGPU && h.VulkanAvailable {
			return "linux-" + arch + "-vulkan", ""
		}
		if hasGPU {
			return "linux-" + arch + "-cpu", "GPU found but no Vulkan driver; install the vulkan loader and mesa drivers (sudo apt install libvulkan1 mesa-vulkan-drivers) to use it"
		}
		return "linux-" + arch + "-cpu", ""
	case "windows":
		if hasGPU && arch == "x64" {
			return "win-x64-vulkan", ""
		}
		return "win-" + arch + "-cpu", ""
	case "darwin":
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

type runtimeCache struct {
	mu       sync.Mutex
	checked  time.Time
	releases []ghRelease
	locks    map[string]*sync.Mutex
	ceiling  map[string]string // node|variant -> newest build known to run on that node (older glibc)
}

func (c *Controller) runtimeDir() string { return filepath.Join(c.dataDir, "runtime") }

func (c *Controller) loadRuntimes() {
	c.runtimes = map[string]*RuntimeRec{}
	c.rtCache = &runtimeCache{locks: map[string]*sync.Mutex{}, ceiling: map[string]string{}}
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
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/repos/ggml-org/llama.cpp/releases?per_page=20", nil)
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
	rc.releases, rc.checked = rels, time.Now()
	return rels, nil
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

// ensureRuntime makes sure the newest build for variant is on the controller.
func (c *Controller) ensureRuntime(ctx context.Context, variant string) (*RuntimeRec, error) {
	return c.ensureRuntimeBuild(ctx, variant, "")
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
