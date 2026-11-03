package main

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Sandbox for exec jobs on nodes. See docs/ARCHITECTURE.md ("Sandbox") and
// README.md. Modes:
//
//	off    exactly the old behaviour: the command runs with the node's own rights and environment
//	basic  scrubbed environment, filesystem confined to the job workspace plus read-only system and
//	       toolchain folders, capabilities dropped, file size and core limits; network allowed
//	strict basic + network off unless the job asks for it, plus a syscall deny list
//
// The Linux implementation is in sandbox_linux.go. Other systems run jobs unsandboxed
// and say so in the node's Hello (sandbox_other.go).
const (
	sandboxOff    = "off"
	sandboxBasic  = "basic"
	sandboxStrict = "strict"
)

// normSandbox returns a valid mode or "" (meaning: use the default).
func normSandbox(s string) string {
	switch s = strings.ToLower(strings.TrimSpace(s)); s {
	case sandboxOff, sandboxBasic, sandboxStrict:
		return s
	}
	return ""
}

// effectiveSandbox is the node's own choice, else the global one, else basic.
func effectiveSandbox(global, node string) string {
	if m := normSandbox(node); m != "" {
		return m
	}
	if m := normSandbox(global); m != "" {
		return m
	}
	return sandboxBasic
}

// sandboxPolicy is what the sandbox-child process applies to itself before it
// execs the job's command. It travels as base64 JSON in argv.
type sandboxPolicy struct {
	Mode     string   `json:"mode"`
	Dir      string   `json:"dir"`      // job workspace, read/write
	Tmp      string   `json:"tmp"`      // private TMPDIR and HOME, read/write
	RO       []string `json:"ro"`       // read-only (and executable) folders or files; missing ones are skipped
	RW       []string `json:"rw"`       // read/write folders
	Net      bool     `json:"net"`      // network allowed
	FSize    int64    `json:"fsize"`    // largest file a job may write, bytes
	Argv     []string `json:"argv"`     // command, resolved against PATH inside the child
	Env      []string `json:"env"`      // the whole environment of the command
	Landlock bool     `json:"landlock"` // parent found Landlock; the child must apply it
	NetNS    bool     `json:"netns"`    // child runs in an empty network namespace (set up by the parent)
	LLNet    bool     `json:"llnet"`    // deny TCP bind/connect with Landlock (fallback when no network namespace)
	Seccomp  bool     `json:"seccomp"`
}

func (p *sandboxPolicy) encode() string {
	b, _ := json.Marshal(p)
	return base64.StdEncoding.EncodeToString(b)
}

func decodeSandboxPolicy(s string) (*sandboxPolicy, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	var p sandboxPolicy
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	if len(p.Argv) == 0 {
		return nil, errBadPolicy
	}
	return &p, nil
}

type policyError string

func (e policyError) Error() string { return string(e) }

const errBadPolicy = policyError("empty sandbox policy")

const defaultFSize = 20 << 30

// System folders a job may read and run programs from. /etc is listed file by
// file: it holds /etc/shadow and other secrets.
var sandboxSystemRO = []string{
	"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/opt", "/snap", "/proc",
	"/etc/alternatives", "/etc/ssl", "/etc/ca-certificates", "/etc/pki", "/etc/ld.so.cache", "/etc/ld.so.conf",
	"/etc/ld.so.conf.d", "/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf", "/etc/host.conf", "/etc/gai.conf",
	"/etc/passwd", "/etc/group", "/etc/localtime", "/etc/timezone", "/etc/os-release", "/etc/mime.types",
	"/etc/gitconfig", "/etc/terminfo", "/etc/protocols", "/etc/services", "/etc/inputrc", "/etc/shells",
	"/etc/environment", "/etc/lsb-release", "/etc/debian_version", "/etc/redhat-release", "/etc/java",
	"/etc/java-17-openjdk", "/etc/java-21-openjdk", "/etc/fonts", "/etc/magic", "/usr/local",
}

// Variables from the node's environment that a job may inherit: proxies,
// certificate and locale settings, and Go's module settings.
var sandboxPassEnv = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "GOPROXY", "GONOSUMDB", "GONOSUMCHECK", "GOSUMDB", "GOPRIVATE", "GOFLAGS", "GOINSECURE",
	"LANG", "LC_ALL", "TZ",
}

type sandboxInput struct {
	Mode     string
	Dir      string   // workspace
	GoDir    string   // Outpost-installed Go (bin folder's parent); may not exist
	ToolDir  string   // toolchains folder, read-only
	CacheDir string   // shared build cache, read/write
	HostEnv  []string // the node's environment
	ExtraRO  []string // OUTPOST_SANDBOX_EXTRA_RO
	Spec     ExecSpec
	GoBin    string
}

// buildSandboxPolicy turns the job into the rules the child applies. It does not
// touch the system, so it is the same on every OS and easy to test.
func buildSandboxPolicy(in sandboxInput) *sandboxPolicy {
	tmp := filepath.Join(in.Dir, ".outpost-tmp")
	p := &sandboxPolicy{Mode: in.Mode, Dir: in.Dir, Tmp: tmp, FSize: defaultFSize, Argv: in.Spec.Argv}
	p.RO = append(append([]string{}, sandboxSystemRO...), in.ToolDir)
	p.RO = append(p.RO, in.ExtraRO...)
	p.RW = []string{in.Dir, tmp, in.CacheDir, "/dev"}
	p.Net = in.Mode != sandboxStrict || in.Spec.Network == "allow"
	p.Seccomp = in.Mode == sandboxStrict

	path := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	if in.GoBin != "" {
		path = in.GoBin + ":" + path
	}
	env := []string{
		"PATH=" + path, "HOME=" + tmp, "TMPDIR=" + tmp, "LANG=C.UTF-8",
		"GOCACHE=" + filepath.Join(in.CacheDir, "go-build"), "GOPATH=" + filepath.Join(in.CacheDir, "gopath"),
		"npm_config_cache=" + filepath.Join(in.CacheDir, "npm"), "PIP_CACHE_DIR=" + filepath.Join(in.CacheDir, "pip"),
		"XDG_CACHE_HOME=" + filepath.Join(in.CacheDir, "xdg"), "CARGO_HOME=" + filepath.Join(in.CacheDir, "cargo"),
	}
	for _, k := range sandboxPassEnv {
		for _, kv := range in.HostEnv {
			if strings.HasPrefix(kv, k+"=") {
				env = append(env, kv)
			}
		}
	}
	if !p.Net { // fail fast instead of waiting on a network that is not there
		env = append(env, "GOTOOLCHAIN=local", "GOPROXY=off")
	}
	keys := make([]string, 0, len(in.Spec.Env))
	for k := range in.Spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+in.Spec.Env[k])
	}
	p.Env = dedupeEnv(env)
	return p
}

// dedupeEnv keeps the last value of each variable, in first-seen order.
func dedupeEnv(env []string) []string {
	idx := map[string]int{}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if i, ok := idx[k]; ok {
			out[i] = kv
			continue
		}
		idx[k] = len(out)
		out = append(out, kv)
	}
	return out
}

// sandboxNote is the one line the fixer gets so it does not "fix" correct code
// that the sandbox blocked.
func sandboxNote(mode string, net bool) string {
	switch {
	case mode == sandboxOff || mode == "":
		return ""
	case net:
		return "this command ran in the Outpost sandbox (" + mode + "): it can only change files in its job folder; the network was open"
	}
	return "this command ran in the Outpost sandbox (" + mode + "): it can only change files in its job folder, and the network was OFF (no downloads, no outside connections)"
}

var sandboxDeniedRe = regexp.MustCompile(`(?i)permission denied|operation not permitted|network is unreachable|temporary failure in name resolution|no such host|could not resolve|dial tcp|connection refused|read-only file system|name or service not known|cannot (?:fetch|download)`)

// looksSandboxDenied is a best-effort guess from the output of a failed run.
func looksSandboxDenied(exit int, stdout, stderr string) bool {
	return exit != 0 && sandboxDeniedRe.MatchString(stdout+"\n"+stderr)
}

// networkCommandRe finds commands that need to download something. Used by the
// controller to turn the network on for those steps only.
var networkCommandRe = regexp.MustCompile(`(?i)\b(go (get|mod download|mod tidy|install|work sync)|npm (i|install|ci|add|update)|npx |pnpm (i|install|add)|yarn( install| add)?|pip3? (install|download)|python3? -m pip|poetry install|cargo (fetch|build|install|add)|apt(-get)? |git (clone|fetch|pull|ls-remote)|curl |wget |bundle install|composer install|mvn |gradle|dotnet restore)\b`)

func needsNetwork(argv []string) bool {
	return networkCommandRe.MatchString(strings.Join(argv, " "))
}

// execNetwork is the ExecSpec.Network value for a command the controller builds.
func execNetwork(argv []string) string {
	if needsNetwork(argv) {
		return "allow"
	}
	return ""
}

// withSandboxNote adds the sandbox line to a failed command's output when the
// failure looks like the sandbox blocked it, so the fixer is not misled.
func withSandboxNote(text string, out *ExecOutput) string {
	if out != nil && out.SandboxDenied && out.SandboxNote != "" {
		return text + "\n[Outpost: " + out.SandboxNote + ". If the error above is about permissions or the network, the code may be fine; do not rewrite it for that.]"
	}
	return text
}
