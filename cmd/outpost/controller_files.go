package main

// Big job files and build outputs. Inputs over maxRepoFileSize are not pasted
// into the job message: the node downloads them from the controller (route
// /v1/jobs/{id}/inputs). Collected outputs over inlineMax stay on the node,
// and the controller pulls them from the node's peer server in hash-checked
// chunks with package transfer, the same code that copies models between
// nodes. After the tests pass, runBuild runs the plan's build_command on an
// exec node and stores what the artifact patterns match under the task.

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"outpost/transfer"
)

const buildTimeout = 30 * time.Minute

// Artifact is one file a task's build produced, kept under the task.
type Artifact struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

func (t *Task) artifactsDir() string { return filepath.Join(t.dir, "artifacts") }

// repoJobInputs lists the project's files as job inputs: small ones inline,
// larger ones as blobs the node downloads. srcs maps each blob's path to the
// file on disk. Files beyond maxJobFiles in total are skipped.
func repoJobInputs(repo string) (inputs []FileEntry, srcs map[string]string, skipped []string) {
	srcs = map[string]string{}
	var blobs int64
	for _, f := range listRepo(repo) {
		abs := filepath.Join(repo, filepath.FromSlash(f.Path))
		if f.Bytes > maxRepoFileSize {
			if blobs+f.Bytes > maxJobFiles {
				skipped = append(skipped, f.Path)
				continue
			}
			sum, err := hashFile(abs)
			if err != nil {
				continue
			}
			blobs += f.Bytes
			srcs[f.Path] = abs
			inputs = append(inputs, FileEntry{Path: f.Path, SHA256: sum, Bytes: f.Bytes, Mode: "0644", Blob: true})
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		inputs = append(inputs, inlineFile(f.Path, data))
	}
	return inputs, srcs, skipped
}

// handleServeJobInput gives a node a blob input of the job dispatched to it.
func (c *Controller) handleServeJobInput(w http.ResponseWriter, r *http.Request) {
	n := c.nodeFromBearer(r)
	if n == nil {
		writeJSON(w, 401, ErrInfo{Code: "bad_key", Message: "unknown node key"})
		return
	}
	id := r.PathValue("id")
	c.mu.Lock()
	j := c.jobs[id]
	p, ok := "", false
	if j != nil && j.NodeID == n.ID {
		p, ok = c.jobBlobs[id][r.URL.Query().Get("path")]
	}
	c.mu.Unlock()
	if !ok {
		writeJSON(w, 404, ErrInfo{Code: "no_file", Message: "no such input for this job"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, p)
}

// runExecJobFiles is runExecJob with blob inputs, collected output files and
// the job's result files returned.
func (c *Controller) runExecJobFiles(ctx context.Context, nodeID string, argv []string, inputs []FileEntry, srcs map[string]string, collect []string, label string, timeout time.Duration) (*ExecOutput, *JobResult, error) {
	spec, _ := json.Marshal(ExecSpec{Argv: argv, Stream: true, Network: execNetwork(argv)})
	job := &JobDispatch{JobID: newID("job"), Kind: "exec", TimeoutS: int(timeout.Seconds()), Inputs: inputs, Collect: collect, Spec: spec}
	if len(srcs) > 0 {
		c.mu.Lock()
		if c.jobBlobs == nil {
			c.jobBlobs = map[string]map[string]string{}
		}
		c.jobBlobs[job.JobID] = srcs
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			delete(c.jobBlobs, job.JobID)
			c.mu.Unlock()
		}()
	}
	ch, err := c.dispatchJob(nodeID, job, label)
	if err != nil {
		return nil, nil, err
	}
	select {
	case res := <-ch:
		if res.Status != "succeeded" {
			if res.Error != nil {
				return nil, res, res.Error
			}
			return nil, res, errors.New("job " + res.Status)
		}
		var out ExecOutput
		_ = json.Unmarshal(res.Output, &out)
		return &out, res, nil
	case <-ctx.Done():
		c.cancelJob(job.JobID)
		return nil, nil, ctx.Err()
	}
}

// peerFor returns where to fetch files from a node: its address and pinned certificate.
func (c *Controller) peerFor(nodeID string) (transfer.Peer, *nodeConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, nc := c.nodes[nodeID], c.conns[nodeID]
	if n == nil || nc == nil || n.Hello == nil || n.Hello.Peer == nil || n.Hello.Peer.Fingerprint == "" {
		return transfer.Peer{}, nil, errors.New("the node is offline or is not sharing files")
	}
	return transfer.Peer{Name: nodeID, Addr: net.JoinHostPort(hostOf(nc.addr), fmt.Sprint(n.Hello.Peer.Port)), Fingerprint: n.Hello.Peer.Fingerprint}, nc, nil
}

// fetchJobFiles stores the files a job collected under dst: inline ones are
// decoded, blobs are fetched from the node in hash-checked chunks. The node's
// workspace is closed afterwards. progress, if set, gets the bytes done so far.
func (c *Controller) fetchJobFiles(ctx context.Context, nodeID, jobID string, files []FileEntry, dst string, progress func(done int64)) ([]Artifact, error) {
	var blobs bool
	var total int64
	for _, f := range files {
		blobs = blobs || f.Blob
		total += f.Bytes
	}
	if total > maxJobFiles {
		return nil, fmt.Errorf("the files total %s, over the %s limit", humanBytes(total), humanBytes(maxJobFiles))
	}
	if blobs {
		if nc := c.connOf(nodeID); nc != nil {
			defer func() { _ = nc.send("ws.close", WSClose{WorkspaceID: "ws_" + jobID}) }()
		}
	}
	var peer transfer.Peer
	var token string
	if blobs {
		p, nc, err := c.peerFor(nodeID)
		if err != nil {
			return nil, err
		}
		peer = p
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		token = hex.EncodeToString(b)
		if err := nc.send("peer.grant", PeerGrant{Token: token, TTLSeconds: int(time.Hour / time.Second)}); err != nil {
			return nil, err
		}
		defer func() { _ = nc.send("peer.grant", PeerGrant{Token: token}) }()
	}
	var out []Artifact
	var done int64
	for _, f := range files {
		p, err := safeJoin(dst, f.Path)
		if err != nil {
			return out, fmt.Errorf("%s: %v", f.Path, err)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return out, err
		}
		if f.Blob {
			base := done
			_, err := transfer.Fetch(ctx, transfer.Request{
				ID: "art/" + jobID + "/" + f.Path, Dest: p, Size: f.Bytes, SHA256: f.SHA256,
				Peers: []transfer.Peer{peer}, Token: token,
				Progress: func(d int64) {
					if progress != nil {
						progress(base + d)
					}
				},
			})
			if err != nil {
				return out, fmt.Errorf("%s: %w", f.Path, err)
			}
		} else {
			data, err := base64.StdEncoding.DecodeString(f.InlineB64)
			if err != nil {
				return out, fmt.Errorf("%s: bad base64", f.Path)
			}
			sum := sha256.Sum256(data)
			if f.SHA256 != "" && !strings.EqualFold(hex.EncodeToString(sum[:]), f.SHA256) {
				return out, fmt.Errorf("%s failed its SHA-256 check", f.Path)
			}
			if err := os.WriteFile(p, data, 0o644); err != nil {
				return out, err
			}
		}
		done += f.Bytes
		out = append(out, Artifact{Path: f.Path, Bytes: f.Bytes, SHA256: f.SHA256})
	}
	return out, nil
}

func (c *Controller) connOf(nodeID string) *nodeConn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conns[nodeID]
}

// runBuild runs the plan's build_command after the tests passed (or when
// there are no tests) and stores the artifacts. It returns false only when
// the task was cancelled; a failed build is recorded and the task still
// finishes, with the source available.
func (c *Controller) runBuild(ctx context.Context, t *Task) bool {
	t.mu.Lock()
	plan := t.Plan
	t.mu.Unlock()
	build := strings.TrimSpace(plan.BuildCommand)
	if build == "" {
		return ctx.Err() == nil
	}
	skip := func(format string, a ...any) bool {
		t.mu.Lock()
		t.BuildStatus = "skipped"
		t.mu.Unlock()
		t.logf("warn", format, a...)
		return ctx.Err() == nil
	}
	chain := build
	if setup := strings.TrimSpace(plan.SetupCommand); setup != "" {
		chain = setup + " && " + build
	}
	execs := c.execNodes()
	if len(execs) == 0 {
		return skip("No node has exec enabled, so the build was not run.")
	}
	if need := toolsInCommand(chain); len(need) > 0 {
		capable := c.execNodesWith(execs, need)
		if len(capable) == 0 {
			return skip("No exec node has %s, so the build was not run.", c.missingToolsNote(execs, need))
		}
		execs = capable
	}
	node := execs[0]
	t.setStatus("building")
	t.logf("info", "Building on %s: %s", c.nodeLabel(node), build)
	inputs, srcs, skipped := repoJobInputs(t.repoDir())
	if len(skipped) > 0 {
		t.logf("warn", "Not sent to the build node (over the 2 GiB limit): %s", strings.Join(skipped, ", "))
	}
	var patterns []string
	for _, p := range plan.Artifacts {
		if p = strings.TrimSpace(p); p != "" {
			patterns = append(patterns, p)
		}
	}
	out, res, err := c.runExecJobFiles(ctx, node, c.execArgv(node, plan.SetupCommand, plan.BuildCommand), inputs, srcs, patterns, t.Title+" · build", buildTimeout)
	if ctx.Err() != nil || errors.Is(err, errCancelled) {
		return false
	}
	fail := func(output string) bool {
		if len(output) > 12000 {
			output = "…" + output[len(output)-12000:]
		}
		t.mu.Lock()
		t.BuildStatus, t.BuildOutput = "failed", output
		t.mu.Unlock()
		return true
	}
	switch {
	case err != nil && res != nil && res.Status == "timed_out":
		t.logf("warn", "The build timed out after %s. The source is still available.", buildTimeout)
		return fail("The build command timed out.")
	case err != nil:
		t.logf("warn", "Could not run the build: %v", err)
		return skip("The build was skipped.")
	}
	output := strings.TrimSpace(out.StdoutTail + "\n" + out.StderrTail)
	if out.ExitCode != 0 {
		t.logf("warn", "The build failed (exit %d). The source is still available, with the build output.", out.ExitCode)
		return fail(withSandboxNote(output, out))
	}
	// Replace the artifacts of an earlier round only once the new ones arrived.
	tmp := t.artifactsDir() + ".new"
	os.RemoveAll(tmp)
	arts, err := c.fetchJobFiles(ctx, node, res.JobID, res.Files, tmp, nil)
	if ctx.Err() != nil {
		os.RemoveAll(tmp)
		return false
	}
	if err != nil {
		os.RemoveAll(tmp)
		t.logf("warn", "The build ran but its files could not be collected: %v", err)
		return fail(output + "\n\nCollecting the artifacts failed: " + err.Error())
	}
	os.RemoveAll(t.artifactsDir())
	if len(arts) > 0 {
		if err := os.Rename(tmp, t.artifactsDir()); err != nil {
			t.logf("warn", "Could not store the artifacts: %v", err)
			return fail(output + "\n\nStoring the artifacts failed: " + err.Error())
		}
	}
	os.RemoveAll(tmp)
	sort.Slice(arts, func(i, j int) bool { return arts[i].Path < arts[j].Path })
	t.mu.Lock()
	t.BuildStatus, t.BuildOutput, t.Artifacts = "passed", "", arts
	t.mu.Unlock()
	if len(patterns) > 0 && len(arts) == 0 {
		t.logf("warn", "The build passed but nothing matched %s.", strings.Join(patterns, ", "))
	} else {
		var size int64
		for _, a := range arts {
			size += a.Bytes
		}
		t.logf("info", "Build passed: %d file%s, %s.", len(arts), plural(len(arts)), humanBytes(size))
	}
	return true
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ---------- downloads ----------

func (c *Controller) handleTaskArtifact(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	p, err := safeJoin(t.artifactsDir(), r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad path"})
		return
	}
	f, err := os.Open(p)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "no such artifact"})
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		writeJSON(w, 404, map[string]string{"error": "no such artifact"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+strings.ReplaceAll(filepath.Base(p), "\"", "")+"\"")
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// zipItem is one file for a streamed zip.
type zipItem struct{ name, path string }

// writeZip streams the items as a zip. Contents are stored, not compressed:
// build outputs are mostly compressed already and a 2 GB deflate would hold a
// CPU for minutes.
func writeZip(w io.Writer, items []zipItem) error {
	zw := zip.NewWriter(w)
	for _, it := range items {
		in, err := os.Open(it.path)
		if err != nil {
			continue
		}
		st, _ := in.Stat()
		hdr := &zip.FileHeader{Name: it.name, Method: zip.Store}
		if st != nil {
			hdr.Modified = st.ModTime()
			hdr.SetMode(0o644)
		}
		fw, err := zw.CreateHeader(hdr)
		if err == nil {
			_, err = io.Copy(fw, in)
		}
		in.Close()
		if err != nil {
			return err
		}
	}
	return zw.Close()
}

func dirItems(root, prefix string) []zipItem {
	var items []zipItem
	for _, f := range listRepo(root) {
		items = append(items, zipItem{prefix + f.Path, filepath.Join(root, filepath.FromSlash(f.Path))})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })
	return items
}

func (c *Controller) handleTaskArtifactsZip(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	items := dirItems(t.artifactsDir(), "")
	if len(items) == 0 {
		writeJSON(w, 404, map[string]string{"error": "this task has no artifacts"})
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="artifacts.zip"`)
	_ = writeZip(w, items)
}

// handleTaskZip zips the project files the user ticked in the file browser.
// A listed folder includes everything under it.
func (c *Controller) handleTaskZip(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	_ = r.ParseForm()
	var want []string
	for _, v := range r.Form["paths"] {
		for _, p := range strings.Split(v, "\n") {
			if p = strings.Trim(strings.TrimSpace(p), "/"); p != "" {
				want = append(want, p)
			}
		}
	}
	var items []zipItem
	for _, f := range dirItems(t.repoDir(), "") {
		for _, p := range want {
			if f.name == p || strings.HasPrefix(f.name, p+"/") {
				items = append(items, f)
				break
			}
		}
	}
	if len(items) == 0 {
		writeJSON(w, 400, map[string]string{"error": "tick at least one file"})
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="files.zip"`)
	_ = writeZip(w, items)
}

// ---------- publish to Updates ----------

// buildArtifactPlatform returns the platform a build output is named for
// ("outpost-linux-amd64", "outpost-windows-amd64.exe"), or "" if it is not
// named like an Outpost binary.
func buildArtifactPlatform(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".exe")
	plat, ok := strings.CutPrefix(name, "outpost-")
	if !ok {
		return ""
	}
	if goos, _, _ := strings.Cut(plat, "-"); strings.HasSuffix(filepath.Base(path), ".exe") != (goos == "windows") {
		return ""
	}
	switch plat {
	case "linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64", "windows-amd64", "windows-arm64":
		return plat
	}
	return ""
}

// publishArtifacts uploads the task's outpost-<platform> outputs as releases.
// Each file must still match its recorded hash, and its embedded platform must
// equal the platform in its name; anything else is refused. It only stores the
// releases: updating the controller or a node stays a click on the Updates tab.
func (c *Controller) publishArtifacts(t *Task) (published []*Release, refused map[string]string) {
	refused = map[string]string{}
	for _, a := range t.Artifacts {
		want := buildArtifactPlatform(a.Path)
		if want == "" {
			continue
		}
		src, err := safeJoin(t.artifactsDir(), a.Path)
		if err != nil {
			refused[a.Path] = "bad path"
			continue
		}
		tmp, err := copyToTemp(src, c.releasesDir())
		if err != nil {
			refused[a.Path] = err.Error()
			continue
		}
		sum, _, err := fileSHA256(tmp)
		if err == nil && a.SHA256 != "" && sum != a.SHA256 {
			err = errors.New("file changed since the build recorded it")
		}
		if err == nil {
			var plat string
			plat, _, err = inspectBinary(tmp)
			if err == nil && plat != want {
				err = fmt.Errorf("named for %s but the binary is for %s", want, plat)
			}
		}
		if err == nil {
			var rel *Release
			if rel, err = c.installRelease(tmp, filepath.Base(a.Path)); err == nil {
				published = append(published, rel)
			}
		}
		if err != nil {
			refused[a.Path] = err.Error()
		}
		os.Remove(tmp)
	}
	return published, refused
}

func copyToTemp(src, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

func (c *Controller) handlePublishArtifacts(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	pub, refused := c.publishArtifacts(t)
	if len(pub) == 0 && len(refused) == 0 {
		writeJSON(w, 400, map[string]string{"error": "this task has no outpost-<platform> build outputs"})
		return
	}
	writeJSON(w, 200, map[string]any{"published": pub, "refused": refused})
}
