//go:build linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The test binary doubles as the sandbox child, the same way the real binary does.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "sandbox-child" {
		runSandboxChild(os.Args[2:])
		return
	}
	os.Exit(m.Run())
}

type sbRun struct {
	exit           int
	stdout, stderr string
	err            *ErrInfo
}

func runSandboxed(t *testing.T, mode string, spec ExecSpec) (r sbRun, dir string) {
	t.Helper()
	root := t.TempDir()
	dir = filepath.Join(root, "work", "ws1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(root, "sandbox-cache")
	os.MkdirAll(cache, 0o700)
	goBin := ""
	if g, err := exec.LookPath("go"); err == nil {
		goBin = filepath.Dir(g)
	}
	pol := buildSandboxPolicy(sandboxInput{Mode: mode, GoBin: goBin, Dir: dir, ToolDir: filepath.Join(root, "toolchains"), CacheDir: cache,
		HostEnv: append(os.Environ(), "OUTPOST_SECRET=hunter2"), Spec: spec})
	cmd := exec.CommandContext(context.Background(), spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = dir
	setProcGroup(cmd)
	if e := sandboxWrap(cmd, pol); e != nil {
		r.err = e
		return
	}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil && cmd.ProcessState == nil {
		t.Logf("start failed: %v", err)
	}
	killProcGroup(cmd)
	r.stdout, r.stderr = so.String(), se.String()
	if cmd.ProcessState != nil {
		r.exit = cmd.ProcessState.ExitCode()
	} else {
		r.exit = -1
	}
	return
}

func needLandlock(t *testing.T) {
	t.Helper()
	if sandboxProbe().ABI == 0 {
		t.Skip("this kernel has no Landlock")
	}
}

func sh(script string) ExecSpec { return ExecSpec{Argv: []string{"sh", "-c", script}} }

func TestSandboxBasicConfinesFiles(t *testing.T) {
	needLandlock(t)
	outside := t.TempDir() // stands in for the node's data dir
	os.WriteFile(filepath.Join(outside, "secret"), []byte("tls-key"), 0o600)

	r, dir := runSandboxed(t, sandboxBasic, sh("echo hi > out.txt && echo fine"))
	if r.err != nil || r.exit != 0 || !strings.Contains(r.stdout, "fine") {
		t.Fatalf("writing in the workspace should work: %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "out.txt")); strings.TrimSpace(string(b)) != "hi" {
		t.Fatalf("file not written: %q", b)
	}

	for name, script := range map[string]string{
		"read /etc/shadow":  "cat /etc/shadow",
		"read outside file": "cat " + filepath.Join(outside, "secret"),
		"write outside":     "echo x > " + filepath.Join(outside, "new"),
		"write /usr":        "touch /usr/outpost-test",
		"write /tmp":        "touch /tmp/outpost-sandbox-test-file",
		"read root's home":  "ls /root",
		"delete outside":    "rm " + filepath.Join(outside, "secret"),
	} {
		r, _ := runSandboxed(t, sandboxBasic, sh(script))
		if r.err != nil {
			t.Fatalf("%s: %v", name, r.err.Message)
		}
		if r.exit == 0 {
			t.Errorf("%s: should have been refused (stdout %q)", name, r.stdout)
		}
	}
	if _, err := os.Stat("/tmp/outpost-sandbox-test-file"); err == nil {
		os.Remove("/tmp/outpost-sandbox-test-file")
		t.Error("sandboxed job wrote to /tmp")
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret")); string(b) != "tls-key" {
		t.Error("outside file was changed")
	}
}

func TestSandboxEnvIsScrubbed(t *testing.T) {
	needLandlock(t)
	t.Setenv("OUTPOST_SECRET_TOKEN", "hunter2")
	r, dir := runSandboxed(t, sandboxBasic, sh("env"))
	if r.exit != 0 {
		t.Fatalf("env failed: %+v", r)
	}
	if strings.Contains(r.stdout, "hunter2") || strings.Contains(r.stdout, "OUTPOST_SECRET") {
		t.Errorf("node environment leaked:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "HOME="+filepath.Join(dir, ".outpost-tmp")) {
		t.Errorf("HOME not private:\n%s", r.stdout)
	}
	r, _ = runSandboxed(t, sandboxBasic, ExecSpec{Argv: []string{"sh", "-c", "echo $MYVAR"}, Env: map[string]string{"MYVAR": "ok"}})
	if strings.TrimSpace(r.stdout) != "ok" {
		t.Errorf("spec env lost: %q", r.stdout)
	}
}

func TestSandboxCommandNotFound(t *testing.T) {
	needLandlock(t)
	r, _ := runSandboxed(t, sandboxBasic, ExecSpec{Argv: []string{"definitely-not-a-command-xyz"}})
	if r.err == nil && r.exit == 0 {
		t.Fatalf("should not succeed: %+v", r)
	}
}

func TestSandboxStrictNetworkOff(t *testing.T) {
	needLandlock(t)
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("needs bash for /dev/tcp")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	script := fmt.Sprintf("exec 3<>/dev/tcp/127.0.0.1/%d && echo connected", port)
	spec := ExecSpec{Argv: []string{"bash", "-c", script}}

	r, _ := runSandboxed(t, sandboxBasic, spec)
	if r.exit != 0 || !strings.Contains(r.stdout, "connected") {
		t.Fatalf("basic keeps the network (%+v)", r)
	}
	r, _ = runSandboxed(t, sandboxStrict, spec)
	if r.err != nil {
		t.Skipf("strict unavailable here: %s", r.err.Message)
	}
	if r.exit == 0 || strings.Contains(r.stdout, "connected") {
		t.Errorf("strict should block the connection: %+v", r)
	}
	spec.Network = "allow"
	r, _ = runSandboxed(t, sandboxStrict, spec)
	if r.exit != 0 || !strings.Contains(r.stdout, "connected") {
		t.Errorf("network allow should open it: %+v", r)
	}
}

func TestSandboxStrictSeccompAndCaps(t *testing.T) {
	needLandlock(t)
	r, _ := runSandboxed(t, sandboxStrict, sh("grep -E 'Seccomp:|NoNewPrivs:|CapEff:' /proc/self/status"))
	if r.err != nil {
		t.Skipf("strict unavailable: %s", r.err.Message)
	}
	if !strings.Contains(r.stdout, "NoNewPrivs:\t1") || !strings.Contains(r.stdout, "CapEff:\t0000000000000000") {
		t.Errorf("no_new_privs/caps: exit %d stdout %q stderr %q", r.exit, r.stdout, r.stderr)
	}
	if seccompArch != 0 && !strings.Contains(r.stdout, "Seccomp:\t2") {
		t.Errorf("seccomp filter not active: exit %d stdout %q stderr %q", r.exit, r.stdout, r.stderr)
	}
}

func TestSandboxStrictCanStillCompile(t *testing.T) {
	needLandlock(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH")
	}
	goroot, _ := exec.Command("go", "env", "GOROOT").Output()
	if len(goroot) == 0 || !strings.HasPrefix(strings.TrimSpace(string(goroot)), "/usr") {
		t.Skip("Go is not under /usr here; a node would need OUTPOST_SANDBOX_EXTRA_RO")
	}
	script := "printf 'module t\\n\\ngo 1.21\\n' > go.mod && printf 'package main\\nfunc main(){println(1)}\\n' > m.go && go run ."
	r, _ := runSandboxed(t, sandboxStrict, sh(script))
	if r.err != nil {
		t.Skipf("strict unavailable: %s", r.err.Message)
	}
	if r.exit != 0 {
		t.Errorf("go run failed in strict: %+v", r)
	}
}

func TestSeccompProgramShape(t *testing.T) {
	p := seccompProgram(0xC000003E, []uint32{101, 165})
	// arch load, arch check, kill, nr load, 2 compares, allow, errno
	if len(p) != 8 {
		t.Fatalf("len %d", len(p))
	}
	// first compare jumps over the second compare and the allow to the errno return
	if p[4].Jt != 2 || p[5].Jt != 1 {
		t.Errorf("jump offsets: %d %d", p[4].Jt, p[5].Jt)
	}
}
