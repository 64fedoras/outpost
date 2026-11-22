package main

// One-click install of Chromium and OpenSCAD on a node (extends row 15).
// Go keeps its own resolver in controller_toolchain.go. The controller
// downloads each archive once, records its SHA-256 and serves it to nodes,
// which check that hash again. Neither Google (Chrome for Testing) nor
// openscad.org publishes checksums, so the hash is recorded at the first
// download and cannot vouch for the publisher; HTTPS does that.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// installableTools are the tools the one-click button can install.
var installableTools = map[string]string{"go": "Go", "chromium": "Chromium", "openscad": "OpenSCAD"}

// openscadVersion is the stable release Outpost installs; newer builds are
// development snapshots only.
const openscadVersion = "2021.01"

func toolsDLBase() string {
	if u := os.Getenv("OUTPOST_TOOLS_DL"); u != "" { // tests point this at a stub
		return strings.TrimRight(u, "/")
	}
	return ""
}

func chromeListURL() string {
	if b := toolsDLBase(); b != "" {
		return b + "/chrome-for-testing.json"
	}
	return "https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json"
}

// toolPlatformOK says whether Outpost can install tool on goos/goarch, and why not.
func toolPlatformOK(tool, goos, goarch string) error {
	if goarch != "amd64" || (goos != "linux" && goos != "windows") {
		name := installableTools[tool]
		hint := "install it by hand"
		if goos == "darwin" {
			hint = "install it by hand (Homebrew or the vendor's download)"
		}
		return fmt.Errorf("Outpost installs %s on Linux and Windows (64-bit Intel/AMD) only, not %s/%s; %s", name, goos, goarch, hint)
	}
	return nil
}

// ensureToolArchive makes the controller hold the archive for tool/platform.
func (c *Controller) ensureToolArchive(ctx context.Context, tool, goos, goarch string) (*GoRec, error) {
	if tool == "go" {
		return c.ensureGo(ctx, goos, goarch)
	}
	if err := toolPlatformOK(tool, goos, goarch); err != nil {
		return nil, err
	}
	goDLMu.Lock()
	defer goDLMu.Unlock()
	var ver, archive, url string
	switch tool {
	case "chromium":
		plat := map[string]string{"linux": "linux64", "windows": "win64"}[goos]
		req, _ := http.NewRequestWithContext(ctx, "GET", chromeListURL(), nil)
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			return nil, fmt.Errorf("Chrome for Testing list: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("Chrome for Testing list: HTTP %d", resp.StatusCode)
		}
		var doc struct {
			Channels struct {
				Stable struct {
					Version   string `json:"version"`
					Downloads struct {
						Chrome []struct{ Platform, URL string } `json:"chrome"`
					} `json:"downloads"`
				}
			} `json:"channels"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
			return nil, fmt.Errorf("Chrome for Testing list: %w", err)
		}
		for _, d := range doc.Channels.Stable.Downloads.Chrome {
			if d.Platform == plat {
				url = d.URL
			}
		}
		ver = doc.Channels.Stable.Version
		if url == "" || !validName(ver) {
			return nil, fmt.Errorf("Chrome for Testing has no stable build for %s", plat)
		}
		archive = "chrome-" + plat + "-" + ver + ".zip"
	case "openscad":
		file := map[string]string{"linux": "OpenSCAD-" + openscadVersion + "-x86_64.AppImage", "windows": "OpenSCAD-" + openscadVersion + "-x86-64.zip"}[goos]
		base := toolsDLBase()
		if base == "" {
			base = "https://files.openscad.org"
		}
		ver, archive, url = openscadVersion, file, base+"/"+file
	default:
		return nil, fmt.Errorf("Outpost cannot install %q", tool)
	}
	dir := c.toolchainDir(tool)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	dst := filepath.Join(dir, archive)
	rec := &GoRec{Version: ver, OS: goos, Arch: goarch, Archive: archive}
	// A finished download leaves <archive>.sha256 ("<hash> <bytes>") beside it.
	if b, err := os.ReadFile(dst + ".sha256"); err == nil {
		if _, err := fmt.Sscanf(string(b), "%s %d", &rec.SHA256, &rec.Bytes); err == nil {
			if st, err := os.Stat(dst); err == nil && st.Size() == rec.Bytes {
				return rec, nil
			}
		}
	}
	sum, err := downloadResumable(ctx, &http.Client{}, url, "", dst, 0, func(int64) {})
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", archive, err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		return nil, err
	}
	rec.SHA256, rec.Bytes = sum, st.Size()
	_ = os.WriteFile(dst+".sha256", []byte(fmt.Sprintf("%s %d", sum, rec.Bytes)), 0o644)
	// Keep only the newest archive for each platform.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		n := strings.TrimSuffix(e.Name(), ".sha256")
		if n != archive && sameToolPlatform(tool, n, goos) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return rec, nil
}

func sameToolPlatform(tool, name, goos string) bool {
	if tool == "chromium" {
		return strings.HasPrefix(name, "chrome-"+map[string]string{"linux": "linux64", "windows": "win64"}[goos]+"-")
	}
	return (goos == "linux") == strings.HasSuffix(name, ".AppImage")
}

// toolRec is one "install this tool" notice for the Nodes page.
type toolRec struct {
	Tool   string     `json:"tool"`
	Label  string     `json:"label"`
	Why    string     `json:"why"`
	Size   string     `json:"size"`
	Nodes  []toolCand `json:"nodes"`            // best first; the first is the recommendation
	Reason string     `json:"reason,omitempty"` // set instead of Nodes when no node can take it
}

type toolCand struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var toolRecInfo = []struct{ tool, why, size string }{
	{"chromium", "Screenshots and visual review of web tasks need Chromium.", "about 170 MB"},
	{"openscad", "The New 3D model page needs OpenSCAD to render files.", "about 60 MB"},
}

// toolRecsLocked lists the tools no exec node has, with the nodes that could
// install them. Caller holds c.mu. Nothing is listed until an exec node exists.
func (c *Controller) toolRecsLocked() []toolRec {
	type cand struct {
		id, name string
		linux    bool
		always   bool
		free     uint64
	}
	var cands []cand
	anyExec := false
	have := map[string]bool{}
	for _, n := range c.nodes {
		if !n.ExecEnabled || n.Hello == nil || n.Hello.Shell == "" {
			continue
		}
		anyExec = true
		for _, t := range n.Hello.Tools {
			have[t.Name] = true
		}
	}
	if !anyExec {
		return nil
	}
	for id, n := range c.nodes {
		if !n.ExecEnabled || n.Hello == nil || n.Hello.Shell == "" || c.conns[id] == nil {
			continue
		}
		cd := cand{id: id, name: displayName(n), linux: n.Hello.OS == "linux", always: n.Hours == nil}
		if hb := c.lastHB[id]; hb != nil && hb.Storage != nil {
			cd.free = hb.Storage.DiskFree
		}
		if toolPlatformOK("chromium", n.Hello.OS, n.Hello.Arch) != nil {
			continue
		}
		cands = append(cands, cd)
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.linux != b.linux {
			return a.linux
		}
		if a.always != b.always {
			return a.always
		}
		if a.free != b.free {
			return a.free > b.free
		}
		return a.name < b.name
	})
	var out []toolRec
	for _, ti := range toolRecInfo {
		if have[ti.tool] {
			continue
		}
		r := toolRec{Tool: ti.tool, Label: installableTools[ti.tool], Why: ti.why, Size: ti.size}
		for _, cd := range cands {
			r.Nodes = append(r.Nodes, toolCand{cd.id, cd.name})
		}
		if len(r.Nodes) == 0 {
			r.Reason = "No online exec node can take it (Linux or Windows, 64-bit Intel/AMD)."
		}
		out = append(out, r)
	}
	return out
}

// removeToolchainOn asks a node to delete a tool Outpost installed there.
func (c *Controller) removeToolchainOn(id, tool string) error {
	c.mu.Lock()
	nc := c.conns[id]
	c.mu.Unlock()
	if nc == nil {
		return fmt.Errorf("%s is offline", c.nodeLabel(id))
	}
	return nc.send("toolchain.remove", ToolInstall{Tool: tool})
}

// handleRemoveToolchain: DELETE /api/nodes/{id}/toolchain/{tool}.
func (c *Controller) handleRemoveToolchain(w http.ResponseWriter, r *http.Request) {
	id, tool := r.PathValue("id"), r.PathValue("tool")
	if installableTools[tool] == "" {
		writeJSON(w, 400, map[string]string{"error": "unknown tool"})
		return
	}
	if err := c.removeToolchainOn(id, tool); err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 202, map[string]bool{"ok": true})
}
