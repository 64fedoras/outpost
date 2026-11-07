package sandbox

import (
	"strings"
	"testing"
)

func TestEffectiveSandbox(t *testing.T) {
	cases := []struct{ g, n, want string }{
		{"", "", "basic"}, {"strict", "", "strict"}, {"strict", "off", "off"}, {"bogus", "", "basic"}, {"off", "strict", "strict"}, {" STRICT ", "", "strict"},
	}
	for _, c := range cases {
		if got := Effective(c.g, c.n); got != c.want {
			t.Errorf("Effective(%q,%q)=%q want %q", c.g, c.n, got, c.want)
		}
	}
}

func TestBuildSandboxPolicy(t *testing.T) {
	in := Input{Mode: Strict, Dir: "/data/work/ws1", ToolDir: "/data/toolchains", CacheDir: "/data/sandbox-cache",
		GoBin:   "/data/toolchains/go/bin",
		HostEnv: []string{"PATH=/x", "ANTHROPIC_API_KEY=secret", "HTTPS_PROXY=http://p:3128", "HOME=/root", "GOFLAGS=-v"},
		Argv:    []string{"go", "test"}, Env: map[string]string{"FOO": "1", "GOFLAGS": "-race"}}
	p := Build(in)
	env := strings.Join(p.Env, "\n")
	if strings.Contains(env, "secret") || strings.Contains(env, "/root") {
		t.Fatalf("host secrets leaked into the job env:\n%s", env)
	}
	for _, want := range []string{"HTTPS_PROXY=http://p:3128", "FOO=1", "GOFLAGS=-race", "HOME=/data/work/ws1/.outpost-tmp", "GOPROXY=off", "PATH=/data/toolchains/go/bin:"} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %q:\n%s", want, env)
		}
	}
	if strings.Count(env, "GOFLAGS=") != 1 {
		t.Errorf("GOFLAGS duplicated:\n%s", env)
	}
	if p.Net || !p.Seccomp {
		t.Errorf("strict without network opt-in: net=%v seccomp=%v", p.Net, p.Seccomp)
	}
	for _, rw := range p.RW {
		if rw == "/etc" || rw == "/" || rw == "/data" {
			t.Errorf("too broad a writable path: %s", rw)
		}
	}
	for _, ro := range p.RO {
		if ro == "/etc" || ro == "/" || ro == "/home" || ro == "/root" || ro == "/var" {
			t.Errorf("too broad a readable path: %s", ro)
		}
	}
	in.Network = "allow"
	if p2 := Build(in); !p2.Net || strings.Contains(strings.Join(p2.Env, " "), "GOPROXY=off") {
		t.Errorf("network allow not honoured: %+v", p2.Env)
	}
	in.Mode, in.Network = Basic, ""
	if p3 := Build(in); !p3.Net || p3.Seccomp {
		t.Errorf("basic should keep the network and skip seccomp")
	}
}

func TestSandboxPolicyRoundTrip(t *testing.T) {
	p := &Policy{Mode: "basic", Dir: "/w", Argv: []string{"ls"}, Env: []string{"A=1"}, RO: []string{"/usr"}}
	q, err := DecodePolicy(p.Encode())
	if err != nil || q.Dir != "/w" || q.Argv[0] != "ls" || q.RO[0] != "/usr" {
		t.Fatalf("round trip: %+v %v", q, err)
	}
	if _, err := DecodePolicy("!!"); err == nil {
		t.Error("garbage accepted")
	}
}

func TestNeedsNetwork(t *testing.T) {
	yes := []string{"go mod download", "cd x && npm ci", "pip install -r requirements.txt", "git clone https://x/y", "sh -c go get example.com/x"}
	no := []string{"go test ./...", "go build ./...", "pytest -q", "ls", "make test"}
	for _, c := range yes {
		if !NeedsNetwork([]string{c}) {
			t.Errorf("%q should need network", c)
		}
	}
	for _, c := range no {
		if NeedsNetwork([]string{c}) {
			t.Errorf("%q should not need network", c)
		}
	}
	if ExecNetwork([]string{"go test"}) != "" || ExecNetwork([]string{"npm install"}) != "allow" {
		t.Error("ExecNetwork")
	}
}

func TestSandboxNotes(t *testing.T) {
	if Note(Off, true) != "" {
		t.Error("off has no note")
	}
	if !strings.Contains(Note(Strict, false), "network was OFF") {
		t.Error("strict note should say the network was off")
	}
	if !LooksDenied(1, "", "dial tcp: lookup proxy.golang.org: no such host") || LooksDenied(0, "permission denied", "") || LooksDenied(1, "FAIL TestX", "") {
		t.Error("LooksDenied")
	}
}
