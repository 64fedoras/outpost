package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"outpost/transfer"
)

func TestValidatePlanArtifacts(t *testing.T) {
	sub := []PlanSubtask{{ID: "t1", Files: []string{"a.go"}}}
	ok := &Plan{Subtasks: sub, BuildCommand: "sh build.sh", Artifacts: []string{" ./dist/* ", "out.bin"}}
	if err := validatePlan(ok); err != nil {
		t.Fatal(err)
	}
	if ok.Artifacts[0] != "dist/*" {
		t.Errorf("not normalized: %q", ok.Artifacts)
	}
	for _, bad := range []string{"../etc/*", "/etc/passwd", ""} {
		if validatePlan(&Plan{Subtasks: []PlanSubtask{{ID: "t1", Files: []string{"a.go"}}}, Artifacts: []string{bad}}) == nil {
			t.Errorf("pattern %q accepted", bad)
		}
	}
}

func bigData(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 31)
	}
	return b
}

// A node keeps big collected files for the controller, serves only those, and
// the controller-side fetch (package transfer) checks them against the hash.
func TestCollectBlobsAndServe(t *testing.T) {
	n := newTestPeerNode(t)
	t.Setenv("OUTPOST_PEER_PORT", "0")
	dir := n.wsDir("ws_job1")
	os.MkdirAll(filepath.Join(dir, "dist"), 0o755)
	big := bigData(inlineMax + 12345)
	os.WriteFile(filepath.Join(dir, "dist", "app.bin"), big, 0o644)
	os.WriteFile(filepath.Join(dir, "dist", "notes.txt"), []byte("hi"), 0o644)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("no"), 0o644)

	if _, e := n.collectFiles(dir, "job1", []string{"dist/*"}); e == nil {
		t.Fatal("a big file should be refused while the node is not sharing")
	}
	n.startPeerServer()
	info := n.peerInfo()
	if info == nil {
		t.Fatal("peer server did not start")
	}
	files, e := n.collectFiles(dir, "job1", []string{"dist/*", "dist/app.bin"})
	if e != nil {
		t.Fatal(e)
	}
	if len(files) != 2 {
		t.Fatalf("files: %+v", files)
	}
	sum := sha256.Sum256(big)
	var blob FileEntry
	for _, f := range files {
		switch f.Path {
		case "dist/app.bin":
			blob = f
		case "dist/notes.txt":
			if f.Blob || f.InlineB64 == "" {
				t.Errorf("small file should be inline: %+v", f)
			}
		}
	}
	if !blob.Blob || blob.InlineB64 != "" || blob.SHA256 != hex.EncodeToString(sum[:]) || blob.Bytes != int64(len(big)) {
		t.Fatalf("blob entry: %+v", blob)
	}

	if _, ok := n.resolvePeerFile("art/job1/secret.txt"); ok {
		t.Error("an uncollected file is served")
	}
	if _, ok := n.resolvePeerFile("art/job1/dist/notes.txt"); ok {
		t.Error("an inline file should not be registered for fetching")
	}
	n.peerGrant(PeerGrant{Token: "tok", TTLSeconds: 60})
	dest := filepath.Join(t.TempDir(), "app.bin")
	_, err := transfer.Fetch(context.Background(), transfer.Request{
		ID: "art/job1/dist/app.bin", Dest: dest, Size: blob.Bytes, SHA256: blob.SHA256, Token: "tok",
		Peers: []transfer.Peer{{Name: "n", Addr: "127.0.0.1:" + strconv.Itoa(info.Port), Fingerprint: info.Fingerprint}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, big) {
		t.Fatal("fetched content differs")
	}

	n.closeWorkspace("ws_job1", "test")
	if _, ok := n.resolvePeerFile("art/job1/dist/app.bin"); ok {
		t.Error("files stay served after the workspace closed")
	}
}

func TestCollectLimits(t *testing.T) {
	n := newTestPeerNode(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0o644)
	if _, e := n.collectFiles(dir, "j", []string{"../*"}); e == nil {
		t.Error("a pattern leaving the workspace was accepted")
	}
	if _, e := n.collectFiles(dir, "j", []string{"/etc/*"}); e == nil {
		t.Error("an absolute pattern was accepted")
	}
	if maxJobFiles != 2<<30 {
		t.Errorf("per-job limit is %d", int64(maxJobFiles))
	}
}

func TestRepoJobInputs(t *testing.T) {
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main"), 0o644)
	big := bigData(maxRepoFileSize + 99)
	os.MkdirAll(filepath.Join(repo, "assets"), 0o755)
	os.WriteFile(filepath.Join(repo, "assets", "data.bin"), big, 0o644)
	inputs, srcs, skipped := repoJobInputs(repo)
	if len(skipped) != 0 || len(inputs) != 2 || len(srcs) != 1 {
		t.Fatalf("inputs %+v srcs %v skipped %v", inputs, srcs, skipped)
	}
	sum := sha256.Sum256(big)
	for _, in := range inputs {
		if in.Path == "assets/data.bin" {
			if !in.Blob || in.InlineB64 != "" || in.SHA256 != hex.EncodeToString(sum[:]) {
				t.Errorf("big input: %+v", in)
			}
		} else if in.Blob || in.InlineB64 == "" {
			t.Errorf("small input: %+v", in)
		}
	}
}

// A node can download only the blob inputs of a job dispatched to it.
func TestServeJobInput(t *testing.T) {
	f := filepath.Join(t.TempDir(), "data.bin")
	os.WriteFile(f, []byte("payload"), 0o644)
	c := &Controller{
		nodes:    map[string]*NodeRec{"n1": {ID: "n1", KeyHash: hashSecret("node", "key1")}, "n2": {ID: "n2", KeyHash: hashSecret("node", "key2")}},
		jobs:     map[string]*JobRec{"job1": {ID: "job1", NodeID: "n1"}},
		jobBlobs: map[string]map[string]string{"job1": {"assets/data.bin": f}},
	}
	get := func(key, job, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/v1/jobs/"+job+"/inputs?path="+url.QueryEscape(path), nil)
		r.SetPathValue("id", job)
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		c.handleServeJobInput(w, r)
		return w
	}
	if w := get("key1", "job1", "assets/data.bin"); w.Code != 200 || w.Body.String() != "payload" {
		t.Fatalf("owner: %d %q", w.Code, w.Body.String())
	}
	for name, w := range map[string]*httptest.ResponseRecorder{
		"no key":      get("", "job1", "assets/data.bin"),
		"other node":  get("key2", "job1", "assets/data.bin"),
		"other path":  get("key1", "job1", "/etc/passwd"),
		"unknown job": get("key1", "job9", "assets/data.bin"),
	} {
		if w.Code == 200 {
			t.Errorf("%s was served", name)
		}
	}
}

func zipNames(t *testing.T, b []byte) []string {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}

func TestTaskZipsAndArtifacts(t *testing.T) {
	dir := t.TempDir()
	tk := &Task{ID: "t1", Title: "Demo", dir: dir}
	for p, body := range map[string]string{"repo/a.go": "A", "repo/src/b.go": "B", "repo/src/sub/c.go": "C", "repo/d.go": "D", "artifacts/dist/app": "BIN"} {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(body), 0o644)
	}
	c := &Controller{tasks: map[string]*Task{"t1": tk}}
	req := func(method, target string, form url.Values, h http.HandlerFunc) *httptest.ResponseRecorder {
		var r *http.Request
		if form != nil {
			r = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			r = httptest.NewRequest(method, target, nil)
		}
		r.SetPathValue("id", "t1")
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}

	w := req("POST", "/api/tasks/t1/zip", url.Values{"paths": {"a.go\nsrc"}}, c.handleTaskZip)
	if got := strings.Join(zipNames(t, w.Body.Bytes()), ","); got != "a.go,src/b.go,src/sub/c.go" {
		t.Errorf("ticked zip: %s", got)
	}
	if w := req("POST", "/api/tasks/t1/zip", url.Values{"paths": {"../x"}}, c.handleTaskZip); w.Code != 400 {
		t.Errorf("nothing matched: %d", w.Code)
	}
	w = req("GET", "/api/tasks/t1/artifacts.zip", nil, c.handleTaskArtifactsZip)
	if got := strings.Join(zipNames(t, w.Body.Bytes()), ","); got != "dist/app" {
		t.Errorf("artifacts zip: %s", got)
	}
	w = req("GET", "/api/tasks/t1/download?artifacts=1", nil, c.handleDownloadTask)
	if got := strings.Join(zipNames(t, w.Body.Bytes()), ","); got != "a.go,artifacts/dist/app,d.go,src/b.go,src/sub/c.go" {
		t.Errorf("source with artifacts: %s", got)
	}
	w = req("GET", "/api/tasks/t1/artifact?path=dist/app", nil, c.handleTaskArtifact)
	if w.Code != 200 || w.Body.String() != "BIN" {
		t.Errorf("single artifact: %d %q", w.Code, w.Body.String())
	}
	if w := req("GET", "/api/tasks/t1/artifact?path=../repo/a.go", nil, c.handleTaskArtifact); w.Code == 200 {
		t.Error("artifact route escaped its folder")
	}
}

func TestFetchJobFilesInline(t *testing.T) {
	c := &Controller{}
	ok := inlineFile("dist/x.txt", []byte("hello"))
	dst := t.TempDir()
	arts, err := c.fetchJobFiles(context.Background(), "n", "job", []FileEntry{ok}, dst, nil)
	if err != nil || len(arts) != 1 || arts[0].Path != "dist/x.txt" {
		t.Fatalf("%v %+v", err, arts)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "dist", "x.txt")); string(b) != "hello" {
		t.Error("content")
	}
	tampered := ok
	tampered.SHA256 = strings.Repeat("0", 64)
	if _, err := c.fetchJobFiles(context.Background(), "n", "job", []FileEntry{tampered}, t.TempDir(), nil); err == nil {
		t.Error("a bad hash was accepted")
	}
	evil := inlineFile("../escape.txt", []byte("x"))
	if _, err := c.fetchJobFiles(context.Background(), "n", "job", []FileEntry{evil}, t.TempDir(), nil); err == nil {
		t.Error("a path outside the destination was accepted")
	}
}
