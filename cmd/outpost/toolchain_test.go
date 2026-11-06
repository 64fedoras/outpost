package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestToolsInCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		{"go build ./... && go test ./...", []string{"go"}},
		{"cd app && npm install && npm test", []string{"node"}},
		{"CGO_ENABLED=0 go vet ./... ; python3 -m unittest", []string{"go", "python"}},
		{"sudo git status | zip -r out.zip .", []string{"git", "zip"}},
		{`C:\tools\Go\bin\go.exe test`, []string{"go"}},
		{"./build.sh", nil},
		{"echo go", nil},
		{"", nil},
	}
	for _, tc := range cases {
		if got := toolsInCommand(tc.cmd); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("toolsInCommand(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

func TestShellArgv(t *testing.T) {
	if got := shellChainArgv(nil, "a", " ", "b"); !reflect.DeepEqual(got, []string{"sh", "-c", "a && b"}) {
		t.Errorf("sh: %v", got)
	}
	if got := shellChainArgv(&Hello{OS: "windows"}, "a", "b"); !reflect.DeepEqual(got, []string{"cmd", "/C", "a && b"}) {
		t.Errorf("old windows agent keeps cmd: %v", got)
	}
	ps := shellChainArgv(&Hello{OS: "windows", Shell: shellPowerShell}, "go build", "go test")
	if ps[0] != "powershell.exe" || ps[len(ps)-2] != "-Command" {
		t.Fatalf("powershell argv: %v", ps)
	}
	script := ps[len(ps)-1]
	if strings.Contains(script, "&&") || !strings.HasPrefix(script, "go build; $__ok") || strings.Count(script, "exit $__c") != 2 {
		t.Errorf("powershell script: %s", script)
	}
	if got := shellArgv(&Hello{Shell: shellSh}, "x"); !reflect.DeepEqual(got, []string{"sh", "-c", "x"}) {
		t.Errorf("shellArgv: %v", got)
	}
}

func TestDetectTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as a fake tool")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "go")
	os.WriteFile(fake, []byte("#!/bin/sh\necho go version go1.24.7 linux/amd64\n"), 0o755)
	t.Setenv("PATH", dir)
	got := detectTools([]string{dir}, dir)
	if len(got) != 1 || got[0].Name != "go" || got[0].Version != "1.24.7" || !got[0].Managed || got[0].Cmd != "go" {
		t.Fatalf("detectTools = %+v", got)
	}
	// A tool whose version command fails is not usable (Windows Store python stub).
	os.WriteFile(filepath.Join(dir, "python3"), []byte("#!/bin/sh\nexit 9\n"), 0o755)
	if got := detectTools(nil, ""); hasTool(got, "python") {
		t.Errorf("failing python should not be listed: %+v", got)
	}
}

func TestExecNodesWith(t *testing.T) {
	c := &Controller{nodes: map[string]*NodeRec{
		"a": {Hello: &Hello{Shell: "sh", Tools: []ToolInfo{{Name: "go"}, {Name: "git"}}}},
		"b": {Hello: &Hello{Shell: "sh", Tools: []ToolInfo{{Name: "python"}}}},
		"c": {Hello: &Hello{}}, // old agent: unknown
	}}
	ids := []string{"a", "b", "c"}
	if got := c.execNodesWith(ids, nil); len(got) != 3 {
		t.Errorf("no need: %v", got)
	}
	if got := c.execNodesWith(ids, []string{"go"}); !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Errorf("go: %v", got)
	}
	if got := c.execNodesWith(ids, []string{"go", "python"}); !reflect.DeepEqual(got, []string{"c"}) {
		t.Errorf("go+python: %v", got)
	}
	if note := c.missingToolsNote([]string{"a", "b"}, []string{"go", "zip", "node"}); note != "node, zip" {
		t.Errorf("missing note: %q", note)
	}
}

func TestToolsPrompt(t *testing.T) {
	c := &Controller{
		conns: map[string]*nodeConn{"w": {}, "l": {}}, lastHB: map[string]*Heartbeat{},
		nodes: map[string]*NodeRec{
			"w": {ID: "w", Name: "win", ExecEnabled: true, Hello: &Hello{OS: "windows", Shell: shellPowerShell, Tools: []ToolInfo{{Name: "git", Version: "2.45"}}}},
			"l": {ID: "l", Name: "lin", ExecEnabled: true, Hello: &Hello{Shell: shellSh, Tools: []ToolInfo{{Name: "go", Version: "1.24.7"}}}},
		}}
	p := c.toolsPrompt()
	for _, want := range []string{"win: shell powershell; tools: git 2.45", "lin: shell sh; tools: go 1.24.7", "no && or ||"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	c.nodes["w"].ExecEnabled, c.nodes["l"].ExecEnabled = false, false
	if p := c.toolsPrompt(); p != "" {
		t.Errorf("no exec nodes should give an empty prompt, got %q", p)
	}
}

func TestPickGo(t *testing.T) {
	js := `[{"version":"go1.25rc1","stable":false,"files":[]},
	{"version":"go1.23.9","stable":true,"files":[{"filename":"go1.23.9.linux-amd64.tar.gz","os":"linux","arch":"amd64","sha256":"aa","size":1,"kind":"archive"}]},
	{"version":"go1.24.7","stable":true,"files":[
	 {"filename":"go1.24.7.linux-amd64.tar.gz","os":"linux","arch":"amd64","sha256":"bb","size":5,"kind":"archive"},
	 {"filename":"go1.24.7.windows-amd64.zip","os":"windows","arch":"amd64","sha256":"cc","size":6,"kind":"archive"},
	 {"filename":"go1.24.7.windows-amd64.msi","os":"windows","arch":"amd64","sha256":"dd","size":7,"kind":"installer"}]}]`
	var rels []goRelease
	if err := json.Unmarshal([]byte(js), &rels); err != nil {
		t.Fatal(err)
	}
	r, err := pickGo(rels, "windows", "amd64")
	if err != nil || r.Archive != "go1.24.7.windows-amd64.zip" || r.Version != "1.24.7" || r.SHA256 != "cc" {
		t.Fatalf("windows pick: %+v %v", r, err)
	}
	if _, err := pickGo(rels, "darwin", "arm64"); err == nil {
		t.Error("expected an error for a platform with no archive")
	}
	if goAtLeast("go1.23.9", 24) || !goAtLeast("go1.24", 24) || !goAtLeast("go2.0", 24) {
		t.Error("goAtLeast")
	}
}

// goTarball builds a minimal archive shaped like the official one.
func goTarball(t *testing.T) []byte {
	t.Helper()
	var buf strings.Builder
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	script := "#!/bin/sh\necho go version go1.24.7 linux/amd64\n"
	tw.WriteHeader(&tar.Header{Name: "go/bin/", Typeflag: tar.TypeDir, Mode: 0o755})
	tw.WriteHeader(&tar.Header{Name: "go/bin/go", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(script))})
	tw.Write([]byte(script))
	tw.Close()
	gz.Close()
	return []byte(buf.String())
}

func TestEnsureGoAndServe(t *testing.T) {
	data := goTarball(t)
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])
	name := "go1.24.7.linux-amd64.tar.gz"
	bad := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "" {
			sh := hexSum
			if bad {
				sh = strings.Repeat("0", 64)
			}
			js := `[{"version":"go1.24.7","stable":true,"files":[{"filename":"` + name + `","os":"linux","arch":"amd64","sha256":"` + sh + `","size":` + itoa(len(data)) + `,"kind":"archive"}]}]`
			w.Write([]byte(js))
			return
		}
		w.Write(data)
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_GO_DL", srv.URL)
	c := &Controller{dataDir: t.TempDir()}
	rec, err := c.ensureGo(context.Background(), "linux", "amd64")
	if err != nil || rec.Version != "1.24.7" {
		t.Fatalf("ensureGo: %+v %v", rec, err)
	}
	if _, err := os.Stat(filepath.Join(c.toolchainDir("go"), name)); err != nil {
		t.Errorf("archive not stored: %v", err)
	}
	// A wrong published hash must be rejected and the file removed.
	os.Remove(filepath.Join(c.toolchainDir("go"), name))
	bad = true
	if _, err := c.ensureGo(context.Background(), "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("expected a hash error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.toolchainDir("go"), name)); err == nil {
		t.Error("bad archive should be deleted")
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestNodeInstallsGo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake go is a shell script")
	}
	data := goTarball(t)
	sum := sha256.Sum256(data)
	n := &Node{dataDir: t.TempDir()}
	// Unpack as installToolchain does, without a controller connection.
	arch := filepath.Join(n.toolchainDir(), "x.tar.gz")
	os.MkdirAll(n.toolchainDir(), 0o755)
	os.WriteFile(arch, data, 0o644)
	stage := filepath.Join(n.toolchainDir(), ".go-new")
	if err := extractTarGz(arch, stage); err != nil {
		t.Fatal(err)
	}
	ver, ok := toolVersion(filepath.Join(stage, "go", "bin", "go"), toolSpecs[0])
	if !ok || ver != "1.24.7" {
		t.Fatalf("unpacked go: %q %v (sha %x)", ver, ok, sum[:2])
	}
	os.Rename(filepath.Join(stage, "go"), filepath.Join(n.toolchainDir(), "go"))
	env := strings.Join(n.execEnv(), "\n")
	if !strings.Contains(env, "PATH="+n.goBinDir()) {
		t.Errorf("exec PATH lacks the installed Go:\n%s", env)
	}
	tools := n.toolInventory(true)
	if !hasTool(tools, "go") {
		t.Errorf("inventory after install: %+v", tools)
	}
	for _, tl := range tools {
		if tl.Name == "go" && !tl.Managed {
			t.Errorf("managed Go not flagged: %+v", tl)
		}
	}
}
