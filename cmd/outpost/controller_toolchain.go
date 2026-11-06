package main

// Controller side of toolchains on nodes (roadmap row 15): reading the tool
// inventory nodes report, routing commands to nodes that have the tool, the
// planner's description of the exec nodes, and the Go download and push.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// minGoMinor is the oldest Go 1.x that builds Outpost (see go.mod).
const minGoMinor = 24

func (c *Controller) toolchainRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/nodes/{id}/toolchain", c.auth(c.handleInstallToolchain))
	mux.HandleFunc("GET /v1/toolchain/{tool}/{file}", c.handleServeToolchain)
}

// ---- inventory ----

// nodeTools returns what node id reported, and whether it reported at all.
// A node that predates the inventory has no Shell, so its tools are unknown.
func (c *Controller) nodeTools(id string) (tools []ToolInfo, known bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.nodes[id]
	if n == nil || n.Hello == nil || n.Hello.Shell == "" {
		return nil, false
	}
	return append([]ToolInfo(nil), n.Hello.Tools...), true
}

func hasTool(tools []ToolInfo, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// execNodesWith narrows exec nodes to those that have every tool named. Nodes
// with no inventory (old agent versions) are kept after the confirmed ones, so
// a cluster that is only partly updated keeps working.
func (c *Controller) execNodesWith(ids []string, need []string) []string {
	if len(need) == 0 {
		return ids
	}
	var sure, unknown []string
	for _, id := range ids {
		tools, known := c.nodeTools(id)
		if !known {
			unknown = append(unknown, id)
			continue
		}
		ok := true
		for _, t := range need {
			if !hasTool(tools, t) {
				ok = false
				break
			}
		}
		if ok {
			sure = append(sure, id)
		}
	}
	return append(sure, unknown...)
}

// execArgv builds the command line that runs setup then test on a node.
func (c *Controller) execArgv(id string, cmds ...string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var h *Hello
	if n := c.nodes[id]; n != nil {
		h = n.Hello
	}
	return shellChainArgv(h, cmds...)
}

func toolLabel(t ToolInfo) string {
	if t.Version == "" {
		return t.Name
	}
	return t.Name + " " + t.Version
}

// toolsPrompt describes the exec nodes for the planner, so test_command and
// setup_command use tools and shell syntax that exist. Empty when no node can run commands.
func (c *Controller) toolsPrompt() string {
	execs := c.execNodes()
	if len(execs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nCommands (setup_command, test_command) run on these nodes, and each one goes to a node that has the tool it starts with. Use only tools listed here; if the project needs one nobody has, choose another approach.\n")
	posh := false
	for _, id := range execs {
		tools, known := c.nodeTools(id)
		c.mu.Lock()
		shell := ""
		if n := c.nodes[id]; n != nil && n.Hello != nil {
			shell = n.Hello.Shell
		}
		c.mu.Unlock()
		if shell == "" {
			shell = shellSh
		}
		var names []string
		for _, t := range tools {
			names = append(names, toolLabel(t))
		}
		list := strings.Join(names, ", ")
		switch {
		case !known:
			list = "unknown (old agent)"
		case list == "":
			list = "none"
		}
		fmt.Fprintf(&b, "- %s: shell %s; tools: %s\n", c.nodeLabel(id), shell, list)
		if shell == shellPowerShell {
			posh = true
		}
	}
	if posh {
		b.WriteString("Nodes with shell powershell run Windows PowerShell 5.1: no && or ||, no sh syntax; write plain commands that also work there (separate steps with ;). Prefer test commands that behave the same in sh and PowerShell.\n")
	}
	return b.String()
}

// ---- Go download ----

// goFile is one entry of go.dev/dl/?mode=json.
type goRelease struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
	Files   []struct {
		Filename string `json:"filename"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
		SHA256   string `json:"sha256"`
		Size     int64  `json:"size"`
		Kind     string `json:"kind"`
	} `json:"files"`
}

// GoRec is the Go archive the controller holds for one platform.
type GoRec struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Archive string `json:"archive"`
	SHA256  string `json:"sha256"`
	Bytes   int64  `json:"bytes"`
}

var goDLMu sync.Mutex // one Go download at a time

func goDLBase() string {
	if u := os.Getenv("OUTPOST_GO_DL"); u != "" { // tests point this at a stub
		return strings.TrimRight(u, "/")
	}
	return "https://go.dev/dl"
}

func (c *Controller) toolchainDir(tool string) string {
	return filepath.Join(c.dataDir, "toolchains", tool)
}

var goVerRe = regexp.MustCompile(`^go(\d+)\.(\d+)`)

func goAtLeast(v string, minor int) bool {
	m := goVerRe.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	return maj > 1 || (maj == 1 && min >= minor)
}

// pickGo chooses the newest stable release that is new enough and has an archive for the platform.
func pickGo(rels []goRelease, goos, goarch string) (*GoRec, error) {
	for _, r := range rels {
		if !r.Stable || !goAtLeast(r.Version, minGoMinor) {
			continue
		}
		for _, f := range r.Files {
			if f.Kind == "archive" && f.OS == goos && f.Arch == goarch && f.SHA256 != "" {
				return &GoRec{Version: strings.TrimPrefix(r.Version, "go"), OS: goos, Arch: goarch, Archive: f.Filename, SHA256: f.SHA256, Bytes: f.Size}, nil
			}
		}
	}
	return nil, fmt.Errorf("no stable Go 1.%d or newer for %s/%s", minGoMinor, goos, goarch)
}

// ensureGo makes sure the controller holds the newest official Go archive for
// a platform, checked against the SHA-256 that go.dev publishes.
func (c *Controller) ensureGo(ctx context.Context, goos, goarch string) (*GoRec, error) {
	goDLMu.Lock()
	defer goDLMu.Unlock()
	req, _ := http.NewRequestWithContext(ctx, "GET", goDLBase()+"/?mode=json", nil)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("go.dev: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("go.dev: HTTP %d", resp.StatusCode)
	}
	var rels []goRelease
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return nil, fmt.Errorf("go.dev list: %w", err)
	}
	rec, err := pickGo(rels, goos, goarch)
	if err != nil {
		return nil, err
	}
	dir := c.toolchainDir("go")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	dst := filepath.Join(dir, rec.Archive)
	sum, err := downloadResumable(ctx, &http.Client{}, goDLBase()+"/"+rec.Archive, "", dst, rec.Bytes, func(int64) {})
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", rec.Archive, err)
	}
	if !strings.EqualFold(sum, rec.SHA256) {
		os.Remove(dst)
		return nil, fmt.Errorf("%s failed its SHA-256 check", rec.Archive)
	}
	// Keep only the newest archive for each platform.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		n := e.Name()
		if n != rec.Archive && strings.HasSuffix(n, "."+goos+"-"+goarch+filepath.Ext(rec.Archive)) {
			os.Remove(filepath.Join(dir, n))
		}
	}
	return rec, nil
}

var toolFileRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func (c *Controller) handleServeToolchain(w http.ResponseWriter, r *http.Request) {
	if c.nodeFromBearer(r) == nil {
		writeJSON(w, 401, ErrInfo{Code: "bad_key", Message: "unknown node key"})
		return
	}
	tool, file := r.PathValue("tool"), r.PathValue("file")
	if tool != "go" || !toolFileRe.MatchString(file) || strings.Contains(file, "..") {
		writeJSON(w, 404, ErrInfo{Code: "no_toolchain", Message: "no such toolchain file"})
		return
	}
	p := filepath.Join(c.toolchainDir(tool), file)
	if _, err := os.Stat(p); err != nil {
		writeJSON(w, 404, ErrInfo{Code: "no_toolchain", Message: "that file is not on the controller"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, p)
}

// installToolchainOn downloads Go for the node's platform (once) and tells the node to install it.
func (c *Controller) installToolchainOn(ctx context.Context, id, tool string) error {
	c.mu.Lock()
	var hello Hello
	if n := c.nodes[id]; n != nil && n.Hello != nil {
		hello = *n.Hello
	}
	nc := c.conns[id]
	c.mu.Unlock()
	if nc == nil {
		return fmt.Errorf("%s is offline", c.nodeLabel(id))
	}
	c.setNodeOp(id, OpStatus{Op: "toolchain", Target: tool, State: "preparing"})
	rec, err := c.ensureGo(ctx, hello.OS, hello.Arch)
	if err != nil {
		c.setNodeOp(id, OpStatus{Op: "toolchain", Target: tool, State: "failed", Error: err.Error()})
		return err
	}
	return nc.send("toolchain.install", ToolInstall{Tool: tool, Version: rec.Version, Archive: rec.Archive, SHA256: rec.SHA256, Bytes: rec.Bytes})
}

// handleInstallToolchain: POST /api/nodes/{id}/toolchain {"tool":"go"}. The node
// is the user's choice, so each exec node gets Go only when asked.
func (c *Controller) handleInstallToolchain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Tool string `json:"tool"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Tool == "" {
		writeJSON(w, 400, map[string]string{"error": "tool required"})
		return
	}
	if req.Tool != "go" {
		writeJSON(w, 400, map[string]string{"error": "Outpost can install only Go; other tools are detected, not installed"})
		return
	}
	c.mu.Lock()
	n, nc := c.nodes[id], c.conns[id]
	exec := n != nil && n.ExecEnabled
	c.mu.Unlock()
	switch {
	case n == nil:
		writeJSON(w, 404, map[string]string{"error": "no such node"})
		return
	case nc == nil:
		writeJSON(w, 409, map[string]string{"error": "node is offline"})
		return
	case !exec:
		writeJSON(w, 409, map[string]string{"error": "enable exec on this node first"})
		return
	}
	go func() {
		if err := c.installToolchainOn(context.Background(), id, req.Tool); err != nil {
			c.setNodeOp(id, OpStatus{Op: "toolchain", Target: req.Tool, State: "failed", Error: err.Error()})
		}
	}()
	writeJSON(w, 202, map[string]bool{"ok": true})
}

// missingToolsNote names what no exec node has, for the task log.
func (c *Controller) missingToolsNote(execs, need []string) string {
	var missing []string
	for _, t := range need {
		found := false
		for _, id := range execs {
			if tools, known := c.nodeTools(id); known && hasTool(tools, t) {
				found = true
			}
		}
		if !found {
			missing = append(missing, t)
		}
	}
	sort.Strings(missing)
	return strings.Join(missing, ", ")
}
