package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnsureChromiumAndOpenSCAD(t *testing.T) {
	chrome := zipOf(t, map[string]string{"chrome-linux64/chrome": "x"})
	hits := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/chrome-for-testing.json":
			w.Write([]byte(`{"channels":{"Stable":{"version":"131.0.1.2","downloads":{"chrome":[
			 {"platform":"linux64","url":"` + srv.URL + `/c/linux64.zip"},{"platform":"win64","url":"` + srv.URL + `/c/win64.zip"}]}}}}`))
		case strings.HasPrefix(r.URL.Path, "/c/"):
			hits++
			w.Write(chrome)
		case strings.HasSuffix(r.URL.Path, ".AppImage"):
			w.Write([]byte("appimage"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_TOOLS_DL", srv.URL)
	c := &Controller{dataDir: t.TempDir()}
	rec, err := c.ensureToolArchive(t.Context(), "chromium", "linux", "amd64")
	if err != nil || rec.Archive != "chrome-linux64-131.0.1.2.zip" || rec.Bytes != int64(len(chrome)) || len(rec.SHA256) != 64 {
		t.Fatalf("chromium: %+v %v", rec, err)
	}
	if rec2, err := c.ensureToolArchive(t.Context(), "chromium", "linux", "amd64"); err != nil || rec2.SHA256 != rec.SHA256 || hits != 1 {
		t.Fatalf("second call should reuse the download: %+v %v hits=%d", rec2, err, hits)
	}
	if rec, err := c.ensureToolArchive(t.Context(), "openscad", "linux", "amd64"); err != nil || rec.Archive != "OpenSCAD-2021.01-x86_64.AppImage" || rec.Version != "2021.01" {
		t.Fatalf("openscad: %+v %v", rec, err)
	}
	if _, err := c.ensureToolArchive(t.Context(), "chromium", "darwin", "arm64"); err == nil || !strings.Contains(err.Error(), "Linux and Windows") {
		t.Errorf("mac should be refused with a reason: %v", err)
	}
	if _, err := c.ensureToolArchive(t.Context(), "chromium", "linux", "arm64"); err == nil {
		t.Error("linux arm64 has no Chrome for Testing build")
	}
}

func TestNodeUnpacksTools(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fake tools are shell scripts")
	}
	n := &Node{dataDir: t.TempDir()}
	root := n.toolchainDir()
	os.MkdirAll(root, 0o755)

	// Chromium: unpack, start once, install into toolchains/chromium.
	arch := filepath.Join(root, "c.zip")
	os.WriteFile(arch, zipOf(t, map[string]string{"chrome-linux64/chrome": "#!/bin/sh\necho Google Chrome for Testing 131.0.1.2\n"}), 0o644)
	stage := filepath.Join(root, "chromium")
	if err := unpackTool("chromium", arch, stage); err != nil {
		t.Fatal(err)
	}
	if v, ok := toolVersion(toolTestPath("chromium", stage), toolSpecFor("chromium")); !ok || v != "131.0.1.2" {
		t.Fatalf("chromium test start: %q %v", v, ok)
	}

	// OpenSCAD: a fake AppImage that unpacks itself like the real one.
	img := "#!/bin/sh\nif [ \"$1\" = --appimage-extract ]; then mkdir -p squashfs-root; printf '#!/bin/sh\\necho OpenSCAD version 2021.01 $QT_QPA_PLATFORM\\n' > squashfs-root/AppRun; chmod +x squashfs-root/AppRun; fi\n"
	arch = filepath.Join(root, "o.AppImage")
	os.WriteFile(arch, []byte(img), 0o755)
	stage = filepath.Join(root, "openscad")
	os.MkdirAll(stage, 0o755)
	if err := unpackTool("openscad", arch, stage); err != nil {
		t.Fatal(err)
	}
	out, _ := execOut(toolTestPath("openscad", stage))
	if !strings.Contains(out, "OpenSCAD version 2021.01 offscreen") {
		t.Fatalf("wrapper output: %q", out)
	}

	tools := n.toolInventory(true)
	for _, name := range []string{"chromium", "openscad"} {
		found := false
		for _, tl := range tools {
			if tl.Name == name {
				found = true
				if !tl.Managed {
					t.Errorf("%s not flagged as installed by Outpost: %+v", name, tl)
				}
			}
		}
		if !found {
			t.Errorf("%s missing from inventory: %+v", name, tools)
		}
	}
	if !strings.Contains(strings.Join(n.execEnv(), "\n"), filepath.Join(root, "openscad", "bin")) {
		t.Error("exec PATH lacks the installed OpenSCAD")
	}

	// Remove deletes only Outpost's copy.
	n.removeToolFiles("openscad")
	if _, err := os.Stat(filepath.Join(root, "openscad")); err == nil {
		t.Error("openscad folder should be gone")
	}
	if _, err := os.Stat(filepath.Join(root, "chromium")); err != nil {
		t.Error("chromium should stay")
	}
}

func execOut(path string) (string, error) {
	b, err := execCombined(path)
	return string(b), err
}

func TestToolRecommendations(t *testing.T) {
	c := &Controller{nodes: map[string]*NodeRec{}, conns: map[string]*nodeConn{}, lastHB: map[string]*Heartbeat{}}
	if got := c.toolRecsLocked(); got != nil {
		t.Fatalf("no exec node, no notice: %+v", got)
	}
	add := func(id, name, os_, arch string, tools ...string) {
		h := &Hello{OS: os_, Arch: arch, Shell: "sh"}
		for _, tl := range tools {
			h.Tools = append(h.Tools, ToolInfo{Name: tl})
		}
		c.nodes[id] = &NodeRec{ID: id, Name: name, ExecEnabled: true, Hello: h}
		c.conns[id] = &nodeConn{}
	}
	add("w", "Win", "windows", "amd64", "go")
	add("m", "Mac", "darwin", "arm64", "go")
	add("l", "Lin", "linux", "amd64", "go")
	recs := c.toolRecsLocked()
	if len(recs) != 2 || recs[0].Tool != "chromium" || recs[1].Tool != "openscad" {
		t.Fatalf("recs: %+v", recs)
	}
	if ns := recs[0].Nodes; len(ns) != 2 || ns[0].ID != "l" || ns[1].ID != "w" {
		t.Errorf("Linux first, Mac left out: %+v", ns)
	}
	// Once any exec node has Chromium the notice goes away.
	c.nodes["m"].Hello.Tools = append(c.nodes["m"].Hello.Tools, ToolInfo{Name: "chromium"})
	if recs := c.toolRecsLocked(); len(recs) != 1 || recs[0].Tool != "openscad" {
		t.Errorf("after chromium appeared: %+v", recs)
	}
	// Only a Mac: the notice says why nothing can be offered.
	d := &Controller{nodes: map[string]*NodeRec{"m": c.nodes["m"]}, conns: c.conns, lastHB: c.lastHB}
	if recs := d.toolRecsLocked(); len(recs) != 1 || recs[0].Reason == "" || len(recs[0].Nodes) != 0 {
		t.Errorf("mac only: %+v", recs)
	}
}

func execCombined(path string) ([]byte, error) { return exec.Command(path).CombinedOutput() }
