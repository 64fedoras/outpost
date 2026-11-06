package main

// Toolchains on nodes (roadmap row 15): which developer tools a node has,
// which shell its exec jobs use, and installing Go that Outpost carries itself.
// Shared types and helpers live here; the node side is at the bottom.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ToolInfo is one developer tool found on a node.
type ToolInfo struct {
	Name    string `json:"name"`              // canonical: go, python, node, gcc, git, zip, chromium
	Cmd     string `json:"cmd"`               // command that starts it here (python3 on Linux, python on Windows)
	Version string `json:"version,omitempty"` // empty when it could not be read
	Path    string `json:"path,omitempty"`
	Managed bool   `json:"managed,omitempty"` // installed by Outpost into its own data folder
}

// ToolInstall asks a node to install a toolchain from GET /v1/toolchain/<tool>/<archive>.
type ToolInstall struct {
	Tool    string `json:"tool"` // only "go" so far
	Version string `json:"version"`
	Archive string `json:"archive"`
	SHA256  string `json:"sha256"`
	Bytes   int64  `json:"bytes"`
}

// Shell names a node reports in Hello.Shell.
const (
	shellSh         = "sh"
	shellPowerShell = "powershell"
	shellCmd        = "cmd"
)

// toolSpec says how to find one tool and read its version.
type toolSpec struct {
	name       string
	cmds       []string // candidates, first hit wins
	winPaths   []string // extra absolute locations on Windows (env vars expanded)
	macPaths   []string
	versionArg []string
	trust      bool // list it when found even if the version command fails (Chrome prints nothing on Windows)
}

var toolSpecs = []toolSpec{
	{name: "go", cmds: []string{"go"}, versionArg: []string{"version"}},
	{name: "python", cmds: []string{"python3", "python"}, versionArg: []string{"--version"}},
	{name: "node", cmds: []string{"node"}, versionArg: []string{"--version"}},
	{name: "gcc", cmds: []string{"gcc", "cc"}, versionArg: []string{"--version"}},
	{name: "git", cmds: []string{"git"}, versionArg: []string{"--version"}},
	{name: "zip", cmds: []string{"zip"}, versionArg: []string{"-v"}},
	{name: "chromium", cmds: []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome"},
		winPaths: []string{`%ProgramFiles%\Google\Chrome\Application\chrome.exe`, `%ProgramFiles(x86)%\Google\Chrome\Application\chrome.exe`,
			`%LocalAppData%\Google\Chrome\Application\chrome.exe`, `%ProgramFiles(x86)%\Microsoft\Edge\Application\msedge.exe`},
		macPaths:   []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium"},
		versionArg: []string{"--version"}, trust: true},
}

// toolAliases maps the first word of a command to the tool that provides it.
var toolAliases = map[string]string{
	"go": "go", "gofmt": "go",
	"python": "python", "python3": "python", "py": "python", "pip": "python", "pip3": "python", "pytest": "python",
	"node": "node", "npm": "node", "npx": "node", "yarn": "node",
	"gcc": "gcc", "g++": "gcc", "cc": "gcc",
	"git": "git", "zip": "zip",
	"chromium": "chromium", "chromium-browser": "chromium", "google-chrome": "chromium", "chrome": "chromium",
}

var (
	cmdSplitRe = regexp.MustCompile(`[;&|(){}\n]+`)
	envAssign  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	versionRe  = regexp.MustCompile(`\d+(\.\d+)+`)
)

// toolsInCommand lists the tools a shell command line starts, for routing it
// to a node that has them. Only the first word of each statement counts.
func toolsInCommand(cmd string) []string {
	seen := map[string]bool{}
	var out []string
	for _, seg := range cmdSplitRe.Split(cmd, -1) {
		words := strings.Fields(seg)
		for len(words) > 0 && (envAssign.MatchString(words[0]) || words[0] == "sudo" || words[0] == "exec" || words[0] == "time" || words[0] == "&") {
			words = words[1:]
		}
		if len(words) == 0 {
			continue
		}
		w := strings.ToLower(filepath.Base(strings.ReplaceAll(words[0], `\`, "/")))
		w = strings.TrimSuffix(w, ".exe")
		if t := toolAliases[w]; t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// shellArgv wraps a command line for the shell a node reports. A node that
// predates the inventory (Shell empty) keeps its old behaviour.
func shellArgv(h *Hello, command string) []string {
	return shellChainArgv(h, command)
}

// shellChainArgv runs the non-empty commands one after another and stops at the
// first failure, returning the exit code of the one that failed.
func shellChainArgv(h *Hello, cmds ...string) []string {
	var parts []string
	for _, c := range cmds {
		if c = strings.TrimSpace(c); c != "" {
			parts = append(parts, c)
		}
	}
	shell := shellSh
	if h != nil {
		switch {
		case h.Shell != "":
			shell = h.Shell
		case h.OS == "windows":
			shell = shellCmd
		}
	}
	switch shell {
	case shellPowerShell:
		// PowerShell 5.1 has no &&, and -Command reports 0 or 1 whatever a native
		// program returned, so each step checks $? and $LASTEXITCODE itself.
		const check = `; $__ok = $?; $__c = $LASTEXITCODE; if ($__c) { exit $__c }; if (-not $__ok) { exit 1 }`
		script := strings.Join(parts, check+"; ") + check
		return []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}
	case shellCmd:
		return []string{"cmd", "/C", strings.Join(parts, " && ")}
	default:
		return []string{"sh", "-c", strings.Join(parts, " && ")}
	}
}

// nodeShell is the shell a node's exec jobs use on this OS.
func nodeShell() string {
	if runtime.GOOS != "windows" {
		return shellSh
	}
	if _, err := exec.LookPath("powershell.exe"); err == nil {
		return shellPowerShell
	}
	return shellCmd
}

// ---- node side ----

// toolchainDir holds what Outpost installed for exec jobs; Go goes to <dir>/go.
func (n *Node) toolchainDir() string { return filepath.Join(n.dataDir, "toolchains") }

func (n *Node) goBinDir() string { return filepath.Join(n.toolchainDir(), "go", "bin") }

type toolCache struct {
	mu   sync.Mutex
	list []ToolInfo
	at   time.Time
}

// toolInventory returns the tools found on this node, scanning at most once a
// minute unless force is set.
func (n *Node) toolInventory(force bool) []ToolInfo {
	n.tools.mu.Lock()
	defer n.tools.mu.Unlock()
	if !force && n.tools.list != nil && time.Since(n.tools.at) < time.Minute {
		return n.tools.list
	}
	n.tools.list = detectTools([]string{n.goBinDir()}, n.goBinDir())
	n.tools.at = time.Now()
	return n.tools.list
}

// detectTools looks for every known tool. extraDirs are searched before PATH;
// a tool found under managedDir is flagged as installed by Outpost.
func detectTools(extraDirs []string, managedDir string) []ToolInfo {
	out := []ToolInfo{}
	for _, spec := range toolSpecs {
		path, cmd := findTool(spec, extraDirs)
		if path == "" {
			continue
		}
		ver, ok := toolVersion(path, spec)
		if !ok && !spec.trust {
			continue
		}
		out = append(out, ToolInfo{Name: spec.name, Cmd: cmd, Version: ver, Path: path,
			Managed: managedDir != "" && filepath.Dir(path) == managedDir})
	}
	return out
}

func findTool(spec toolSpec, extraDirs []string) (path, cmd string) {
	for _, c := range spec.cmds {
		for _, d := range extraDirs {
			p := filepath.Join(d, c+exeSuffix(runtime.GOOS))
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p, c
			}
		}
		if p, err := exec.LookPath(c); err == nil {
			return p, c
		}
	}
	var extra []string
	switch runtime.GOOS {
	case "windows":
		extra = spec.winPaths
	case "darwin":
		extra = spec.macPaths
	}
	for _, p := range extra {
		p = os.ExpandEnv(strings.NewReplacer("%ProgramFiles%", "${ProgramFiles}", "%ProgramFiles(x86)%", "${ProgramFiles(x86)}", "%LocalAppData%", "${LocalAppData}").Replace(p))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, strings.TrimSuffix(filepath.Base(p), ".exe")
		}
	}
	return "", ""
}

// toolVersion runs the tool's version command. Python on Windows can be the
// Microsoft Store stub, which exits non-zero, so a failing run means "not usable".
func toolVersion(path string, spec toolSpec) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, path, spec.versionArg...).CombinedOutput()
	if err != nil {
		return "", false
	}
	line := strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)[0]
	if spec.name == "zip" { // `zip -v` starts with a copyright line
		if m := regexp.MustCompile(`(?i)this is zip (\d+(\.\d+)*)`).FindStringSubmatch(string(b)); m != nil {
			return m[1], true
		}
	}
	return versionRe.FindString(line), true
}

// execEnv is the environment for exec jobs: the process environment plus the Go
// that Outpost installed on PATH, and cache folders when the service has no HOME.
func (n *Node) execEnv() []string {
	env := os.Environ()
	if st, err := os.Stat(n.goBinDir()); err == nil && st.IsDir() {
		env = append(env, "PATH="+n.goBinDir()+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if _, err := os.UserCacheDir(); err != nil && os.Getenv("GOCACHE") == "" {
		env = append(env, "GOCACHE="+filepath.Join(n.toolchainDir(), "gocache"))
	}
	if os.Getenv("GOPATH") == "" {
		if _, err := os.UserHomeDir(); err != nil {
			env = append(env, "GOPATH="+filepath.Join(n.toolchainDir(), "gopath"))
		}
	}
	return env
}

// toolsLoop rescans the tools every hour and tells the controller about changes.
func (n *Node) toolsLoop(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			before, _ := json.Marshal(n.toolInventory(false))
			after, _ := json.Marshal(n.toolInventory(true))
			if string(before) != string(after) {
				n.resendHello()
			}
		}
	}
}

// installToolchain downloads a toolchain archive from the controller, unpacks it
// beside the old one and swaps it in once `go version` runs.
func (n *Node) installToolchain(ti ToolInstall) {
	const op = "toolchain"
	fail := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		log.Printf("toolchain %s %s: %s", ti.Tool, ti.Version, msg)
		n.opStatus(op, ti.Tool, "failed", 0, ti.Bytes, msg)
	}
	if ti.Tool != "go" || !validName(ti.Version) || !validName(ti.Archive) || strings.ContainsAny(ti.Archive, `/\`) {
		fail("bad toolchain request")
		return
	}
	root := n.toolchainDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		fail("%v", err)
		return
	}
	archive := filepath.Join(root, ti.Archive)
	n.opStatus(op, ti.Tool, "downloading", 0, ti.Bytes, "")
	err := n.fetchFile("/v1/toolchain/"+ti.Tool+"/"+ti.Archive, archive, ti.SHA256, ti.Bytes, func(d int64) {
		n.opStatus(op, ti.Tool, "downloading", d, ti.Bytes, "")
	})
	if err != nil {
		fail("%s", withDefenderHint("download: "+err.Error(), n.dataDir))
		return
	}
	n.opStatus(op, ti.Tool, "installing", ti.Bytes, ti.Bytes, "")
	stage := filepath.Join(root, ".go-new")
	os.RemoveAll(stage)
	if strings.HasSuffix(ti.Archive, ".zip") {
		err = extractZip(archive, stage)
	} else {
		err = extractTarGz(archive, stage)
	}
	os.Remove(archive)
	if err != nil {
		os.RemoveAll(stage)
		fail("extract: %v", err)
		return
	}
	// The official archives hold one top-level "go" folder.
	bin := filepath.Join(stage, "go", "bin", "go"+exeSuffix(runtime.GOOS))
	ver, ok := toolVersion(bin, toolSpecs[0])
	if !ok {
		os.RemoveAll(stage)
		fail("the unpacked go does not run on this node")
		return
	}
	final := filepath.Join(root, "go")
	old := filepath.Join(root, ".go-old")
	os.RemoveAll(old)
	if _, err := os.Stat(final); err == nil {
		if err := os.Rename(final, old); err != nil {
			os.RemoveAll(stage)
			fail("replace old go: %v", err)
			return
		}
	}
	if err := os.Rename(filepath.Join(stage, "go"), final); err != nil {
		os.Rename(old, final)
		os.RemoveAll(stage)
		fail("%v", err)
		return
	}
	os.RemoveAll(stage)
	os.RemoveAll(old)
	log.Printf("go %s installed at %s", ver, final)
	n.toolInventory(true)
	n.opStatus(op, ti.Tool, "done", ti.Bytes, ti.Bytes, "")
	n.resendHello()
}
