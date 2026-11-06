package main

// Node side of models and inference: installs llama.cpp builds pushed by the
// controller, downloads models from the controller, supervises one
// llama-server process bound to 127.0.0.1, and runs "inference" jobs.

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------- paths ----------

func (n *Node) llamaDir() string  { return filepath.Join(n.dataDir, "llama") }
func (n *Node) modelsDir() string { return filepath.Join(n.dataDir, "models") }

type runtimeRecord struct {
	RuntimeInfo
	ServerPath string `json:"server_path"`
}

func (n *Node) currentRuntime() *runtimeRecord {
	b, err := os.ReadFile(filepath.Join(n.llamaDir(), "current.json"))
	if err != nil {
		return nil
	}
	var r runtimeRecord
	if json.Unmarshal(b, &r) != nil || r.ServerPath == "" {
		return nil
	}
	if _, err := os.Stat(r.ServerPath); err != nil {
		return nil
	}
	return &r
}

func (n *Node) opStatus(op, target, state string, done, total int64, errMsg string) {
	st := OpStatus{Op: op, Target: target, State: state, BytesDone: done, BytesTotal: total, Error: errMsg}
	if total > 0 {
		st.Progress = float64(done) / float64(total)
	}
	_ = n.send("node.op_status", st)
}

// vulkanAvailable reports whether a Vulkan loader is installed (Linux/Windows).
func vulkanAvailable() bool {
	switch runtime.GOOS {
	case "linux":
		if strings.Contains(cmdOut("ldconfig", "-p"), "libvulkan.so.1") {
			return true
		}
		for _, p := range []string{"/usr/lib/x86_64-linux-gnu/libvulkan.so.1", "/usr/lib64/libvulkan.so.1", "/usr/lib/libvulkan.so.1", "/usr/lib/aarch64-linux-gnu/libvulkan.so.1"} {
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}
	case "windows":
		if _, err := os.Stat(filepath.Join(os.Getenv("SystemRoot"), "System32", "vulkan-1.dll")); err == nil {
			return true
		}
	}
	return false
}

// ---------- downloads from the controller ----------

func (n *Node) controllerClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{TLSClientConfig: pinnedTLS(n.pin())}}
}

// fetchFile downloads path from the controller into dst, resuming a partial
// dst+".part" and verifying size and SHA-256. progress is called periodically.
func (n *Node) fetchFile(urlPath, dst, wantSHA string, wantBytes int64, progress func(done int64)) error {
	part := dst + ".part"
	h := sha256.New()
	var have int64
	if f, err := os.Open(part); err == nil {
		have, _ = io.Copy(h, f)
		f.Close()
		if wantBytes > 0 && have > wantBytes {
			os.Remove(part)
			h.Reset()
			have = 0
		}
	}
	req, err := http.NewRequest("GET", strings.TrimRight(n.cfg.Controller, "/")+urlPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+n.cfg.NodeKey)
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := n.controllerClient(0).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0:
	case resp.StatusCode == http.StatusOK:
		if have > 0 { // server ignored the range: start over
			h.Reset()
			have = 0
		}
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && have == wantBytes:
	default:
		return fmt.Errorf("controller returned %s", resp.Status)
	}
	flags := os.O_CREATE | os.O_WRONLY
	if have > 0 && resp.StatusCode == http.StatusPartialContent {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	done := have
	last := time.Now()
	buf := make([]byte, 1<<20)
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		for {
			k, rerr := resp.Body.Read(buf)
			if k > 0 {
				if _, werr := f.Write(buf[:k]); werr != nil {
					f.Close()
					return werr
				}
				h.Write(buf[:k])
				done += int64(k)
				if progress != nil && time.Since(last) > 2*time.Second {
					progress(done)
					last = time.Now()
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				f.Close()
				return rerr // keep .part for resume
			}
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if wantBytes > 0 && done != wantBytes {
		return fmt.Errorf("size mismatch: got %d bytes, want %d", done, wantBytes)
	}
	if wantSHA != "" && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), wantSHA) {
		os.Remove(part)
		return errors.New("sha256 mismatch")
	}
	return os.Rename(part, dst)
}

// ---------- llama.cpp runtime ----------

func (n *Node) installRuntime(ri RuntimeInstall) {
	op := "runtime"
	fail := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		log.Printf("runtime %s %s: %s", ri.Build, ri.Variant, msg)
		n.opStatus(op, ri.Variant, "failed", 0, ri.Bytes, msg)
	}
	if !validName(ri.Build) || !validName(ri.Variant) || strings.ContainsAny(ri.Archive, `/\`) {
		fail("bad runtime name")
		return
	}
	if cur := n.currentRuntime(); cur != nil && cur.Build == ri.Build && cur.Variant == ri.Variant {
		n.opStatus(op, ri.Variant, "done", ri.Bytes, ri.Bytes, "")
		return
	}
	if err := os.MkdirAll(n.llamaDir(), 0o755); err != nil {
		fail("%v", err)
		return
	}
	archive := filepath.Join(n.llamaDir(), ri.Archive)
	n.opStatus(op, ri.Variant, "downloading", 0, ri.Bytes, "")
	err := n.fetchFile("/v1/runtime/"+ri.Variant, archive, ri.SHA256, ri.Bytes, func(d int64) {
		n.opStatus(op, ri.Variant, "downloading", d, ri.Bytes, "")
	})
	if err != nil {
		fail("%s", withDefenderHint("download: "+err.Error(), n.dataDir))
		return
	}
	n.opStatus(op, ri.Variant, "installing", ri.Bytes, ri.Bytes, "")
	// The running llama-server lives in the old build's folder; stop it before
	// replacing that folder (the controller loads the model again afterwards).
	n.llama.stop()
	prev := ""
	if cur := n.currentRuntime(); cur != nil {
		prev = filepath.Join(n.llamaDir(), cur.Build+"-"+cur.Variant)
	}
	dest := filepath.Join(n.llamaDir(), ri.Build+"-"+ri.Variant)
	os.RemoveAll(dest)
	if strings.HasSuffix(ri.Archive, ".zip") {
		err = extractZip(archive, dest)
	} else {
		err = extractTarGz(archive, dest)
	}
	os.Remove(archive)
	if err != nil {
		os.RemoveAll(dest)
		fail("extract: %v", err)
		return
	}
	server := findFile(dest, "llama-server"+exeSuffix(runtime.GOOS))
	if server == "" {
		os.RemoveAll(dest)
		fail("llama-server not found in the archive")
		return
	}
	// Self-check: the binary must start here. Libraries Outpost carries
	// (bundledLibs) are dropped next to llama-server when the system lacks them.
	if msg := checkLlamaRuns(server); msg != "" {
		os.RemoveAll(dest)
		fail("%s", msg)
		return
	}
	rec := runtimeRecord{RuntimeInfo: RuntimeInfo{Build: ri.Build, Variant: ri.Variant}, ServerPath: server}
	b, _ := json.MarshalIndent(rec, "", "  ")
	if err := writeFileAtomic(filepath.Join(n.llamaDir(), "current.json"), b, 0o644); err != nil {
		fail("%v", err)
		return
	}
	pruneLlamaBuilds(n.llamaDir(), dest, prev)
	log.Printf("llama.cpp %s (%s) installed at %s", ri.Build, ri.Variant, server)
	n.opStatus(op, ri.Variant, "done", ri.Bytes, ri.Bytes, "")
	n.resendHello()
}

func validName(s string) bool {
	if s == "" || len(s) > 120 || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if !(r == '_' || r == '-' || r == '.' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func lastLines(s string, k int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > k {
		lines = lines[len(lines)-k:]
	}
	return strings.Join(lines, " | ")
}

func findFile(root, name string) string {
	var found string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == name && found == "" {
			found = p
		}
		return nil
	})
	return found
}

func safeArchivePath(dest, name string) (string, error) {
	p := filepath.Join(dest, filepath.FromSlash(name))
	if r, err := filepath.Rel(dest, p); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the target directory", name)
	}
	return p, nil
}

func extractTarGz(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		p, err := safeArchivePath(dest, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(h.Mode)&0o755|0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink:
			// Shared-library version links (libllama.so -> libllama.so.0). Only
			// relative targets inside the archive are allowed.
			if filepath.IsAbs(h.Linkname) {
				return fmt.Errorf("absolute symlink %q in archive", h.Linkname)
			}
			if _, err := safeArchivePath(dest, filepath.Join(filepath.Dir(h.Name), h.Linkname)); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			os.Remove(p)
			if err := os.Symlink(h.Linkname, p); err != nil {
				return err
			}
		}
	}
}

func extractZip(src, dest string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, zf := range zr.File {
		p, err := safeArchivePath(dest, zf.Name)
		if err != nil {
			return err
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func llamaEnv(dir string) []string {
	env := os.Environ()
	switch runtime.GOOS {
	case "linux":
		env = append(env, "LD_LIBRARY_PATH="+dir+string(os.PathListSeparator)+os.Getenv("LD_LIBRARY_PATH"))
	case "darwin":
		env = append(env, "DYLD_LIBRARY_PATH="+dir)
	}
	return env
}

// ---------- models ----------

type modelManifest struct {
	Name  string      `json:"name"`
	Files []ModelFile `json:"files"`
}

func (n *Node) modelManifest(name string) (*modelManifest, error) {
	b, err := os.ReadFile(filepath.Join(n.modelsDir(), name, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m modelManifest
	if err := json.Unmarshal(b, &m); err != nil || len(m.Files) == 0 {
		return nil, errors.New("bad manifest")
	}
	return &m, nil
}

func (n *Node) listModels() []ModelInfo {
	out := []ModelInfo{}
	entries, _ := os.ReadDir(n.modelsDir())
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := n.modelManifest(e.Name())
		if err != nil {
			continue
		}
		var total int64
		for _, f := range m.Files {
			total += f.Bytes
		}
		out = append(out, ModelInfo{Name: m.Name, Bytes: total})
	}
	return out
}

func (n *Node) pushModel(mp ModelPush) {
	op := "model.push"
	var total int64
	for _, f := range mp.Files {
		total += f.Bytes
	}
	var badPeers []string
	fail := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		log.Printf("model %s: %s", mp.Name, msg)
		st := OpStatus{Op: op, Target: mp.Name, State: "failed", BytesTotal: total, Error: msg, BadPeers: badPeers}
		_ = n.send("node.op_status", st)
	}
	if !validName(mp.Name) || len(mp.Files) == 0 {
		fail("bad model name")
		return
	}
	defer n.markModelBusy(mp.Name)()
	if _, err := n.modelManifest(mp.Name); err == nil {
		n.opStatus(op, mp.Name, "done", total, total, "")
		return
	}
	dir := filepath.Join(n.modelsDir(), mp.Name)
	var have int64
	for _, f := range mp.Files {
		if st, err := os.Stat(filepath.Join(dir, f.Path)); err == nil {
			have += st.Size()
		} else if st, err := os.Stat(filepath.Join(dir, f.Path+".part")); err == nil {
			have += st.Size()
		}
	}
	if err := n.makeRoom(total-have, mp.Name, mp.Reclaim); err != nil {
		fail("%v", err)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail("%v", err)
		return
	}
	var before int64
	n.opStatus(op, mp.Name, "downloading", 0, total, "")
	for i, f := range mp.Files {
		if !validName(f.Path) {
			fail("bad file name %q", f.Path)
			return
		}
		dst := filepath.Join(dir, f.Path)
		if st, err := os.Stat(dst); err == nil && st.Size() == f.Bytes {
			before += f.Bytes
			continue
		}
		base := before
		report := func(d int64) { n.opStatus(op, mp.Name, "downloading", base+d, total, "") }
		var err error
		if len(mp.Peers) > 0 {
			var bad []string
			bad, err = n.fetchFromPeers(mp, f, dst, report)
			badPeers = append(badPeers, bad...)
		} else {
			err = n.fetchFile(fmt.Sprintf("/v1/models/%s/files/%d", mp.Name, i), dst, f.SHA256, f.Bytes, report)
		}
		if err != nil {
			fail("download %s: %v", f.Path, err)
			return
		}
		before += f.Bytes
	}
	b, _ := json.MarshalIndent(modelManifest{Name: mp.Name, Files: mp.Files}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o644); err != nil {
		fail("%v", err)
		return
	}
	n.markVerified(mp.Name, true) // just checked on arrival
	n.touchModel(mp.Name)
	log.Printf("model %s stored (%s)", mp.Name, humanBytes(total))
	n.resendHello() // before "done": the controller checks the model list next
	n.opStatus(op, mp.Name, "done", total, total, "")
}

func (n *Node) removeModel(name string) {
	if !validName(name) {
		return
	}
	if n.protectedModels()[name] {
		n.opStatus("model.remove", name, "failed", 0, 0, "in use: loaded, set to reload, or being copied; unload it first")
		return
	}
	dir := filepath.Join(n.modelsDir(), name)
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() {
		n.opStatus("model.remove", name, "done", 0, 0, "")
		return // gone already, or a symlink or file: never followed
	}
	if err := os.RemoveAll(dir); err != nil {
		n.opStatus("model.remove", name, "failed", 0, 0, err.Error())
		return
	}
	n.opStatus("model.remove", name, "done", 0, 0, "")
	n.resendHello()
}

func humanBytes(b int64) string {
	const u = 1024
	if b < u {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(u), 0
	for k := b / u; k >= u; k /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// resendHello refreshes the controller's view of models and runtime.
func (n *Node) resendHello() { _ = n.send("node.hello_update", n.hello()) }

// ---------- llama-server supervision ----------

type llamaMgr struct {
	n        *Node
	mu       sync.Mutex
	cmd      *exec.Cmd
	exited   chan struct{} // closed by watch once cmd.Wait returns (Wait must run only once)
	port     int
	model    string
	opts     ModelLoad
	ready    bool
	busy     int
	lastErr  string
	logTail  *tailBuf
	stopping bool
	gen      int // bumps on every start/stop so stale watchers exit

	// idle unload: after idleAfter without a request the process is stopped and
	// cold remembers what to load again when the next request arrives.
	idleAfter time.Duration // 0 = never
	lastUsed  time.Time
	cold      *ModelLoad
	wakeMu    sync.Mutex // one reload at a time
}

func newLlamaMgr(n *Node) *llamaMgr { return &llamaMgr{n: n} }

// draftModel is the draft model of the loaded (or idle-unloaded) model.
func (l *llamaMgr) draftModel() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cold != nil && l.cmd == nil {
		return l.cold.Draft
	}
	if l.cmd != nil {
		return l.opts.Draft
	}
	return ""
}

func (l *llamaMgr) loadedModel() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cmd == nil {
		return ""
	}
	return l.model
}

func (l *llamaMgr) status() (model string, ready bool, busy int, lastErr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cmd != nil {
		model = l.model
	}
	return model, l.ready, l.busy, l.lastErr
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// stop is a deliberate stop (unload, replace, shutdown): nothing is kept to reload.
func (l *llamaMgr) stop() {
	l.mu.Lock()
	l.cold = nil
	l.mu.Unlock()
	l.stopProc()
}

func (l *llamaMgr) stopProc() {
	l.mu.Lock()
	cmd, exited := l.cmd, l.exited
	l.cmd, l.exited, l.ready, l.model = nil, nil, false, ""
	l.gen++
	l.mu.Unlock()
	if cmd != nil {
		l.n.pushHeartbeat()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(osInterrupt())
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
	}
}

func (l *llamaMgr) load(ml ModelLoad) {
	op := "model.load"
	fail := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		log.Printf("load %s: %s", ml.Name, msg)
		l.mu.Lock()
		l.lastErr = msg
		l.mu.Unlock()
		l.n.opStatus(op, ml.Name, "failed", 0, 0, msg)
	}
	rt := l.n.currentRuntime()
	if rt == nil {
		fail("llama.cpp is not installed on this node yet")
		return
	}
	m, err := l.n.modelManifest(ml.Name)
	if err != nil {
		fail("model %s is not on this node", ml.Name)
		return
	}
	l.mu.Lock()
	busy := l.busy
	l.mu.Unlock()
	if busy > 0 {
		fail("inference is running; try again when the node is idle")
		return
	}
	if err := l.n.verifyModel(ml.Name); err != nil {
		fail("model %s failed its integrity check: %v", ml.Name, err)
		return
	}
	l.stop()
	l.touch()
	l.n.touchModel(ml.Name)
	files := make([]string, 0, len(m.Files))
	ml.MMProj = ""
	for _, f := range m.Files {
		if strings.Contains(strings.ToLower(f.Path), "mmproj") {
			ml.MMProj = filepath.Join(l.n.modelsDir(), ml.Name, f.Path) // vision projector, passed as --mmproj
			continue
		}
		files = append(files, f.Path)
	}
	if len(files) == 0 {
		fail("model %s has no model file", ml.Name)
		return
	}
	sort.Strings(files) // split models: llama.cpp is given the first part
	modelPath := filepath.Join(l.n.modelsDir(), ml.Name, files[0])
	ml.DraftPath = ""
	if ml.Draft != "" {
		p, err := l.n.mainModelPath(ml.Draft)
		if err != nil {
			fail("draft model %s: %v", ml.Draft, err)
			return
		}
		l.n.touchModel(ml.Draft)
		ml.DraftPath = p
	}
	if err := l.start(rt, ml, modelPath, 0); err != nil {
		fail("%v", err)
		return
	}
	l.n.opStatus(op, ml.Name, "loading", 0, 0, "")
	l.n.pushHeartbeat()
	if err := l.waitReady(15 * time.Minute); err != nil {
		l.stop()
		fail("%v", err)
		return
	}
	log.Printf("model %s loaded", ml.Name)
	l.touch()
	l.n.pushHeartbeat()
	l.n.opStatus(op, ml.Name, "ready", 0, 0, "")
	l.n.resendHello()
}

// servingArgs are the cache, flash attention and draft model arguments.
func servingArgs(ml ModelLoad) []string {
	var a []string
	if ml.CacheType != "" {
		a = append(a, "-ctk", ml.CacheType, "-ctv", ml.CacheType)
	}
	if ml.FlashAttn != "" {
		a = append(a, "-fa", ml.FlashAttn)
	}
	if ml.DraftPath != "" {
		a = append(a, "-md", ml.DraftPath)
		if ml.GPULayers != "0" {
			a = append(a, "-ngld", "99") // the draft is small: keep it on the GPU when there is one
		}
	}
	return a
}

// mainModelPath is the file llama.cpp is given for a stored model.
func (n *Node) mainModelPath(name string) (string, error) {
	m, err := n.modelManifest(name)
	if err != nil {
		return "", fmt.Errorf("not on this node")
	}
	var files []string
	for _, f := range m.Files {
		if !strings.Contains(strings.ToLower(f.Path), "mmproj") {
			files = append(files, f.Path)
		}
	}
	if len(files) == 0 {
		return "", fmt.Errorf("no model file")
	}
	sort.Strings(files)
	return filepath.Join(n.modelsDir(), name, files[0]), nil
}

func hasJinjaArg(args []string) bool {
	for _, a := range args {
		if a == "--jinja" || a == "--no-jinja" {
			return true
		}
	}
	return false
}

func (l *llamaMgr) start(rt *runtimeRecord, ml ModelLoad, modelPath string, restarts int) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	args := []string{"-m", modelPath, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--no-webui", "-a", ml.Name}
	if ml.MMProj != "" {
		args = append(args, "--mmproj", ml.MMProj)
	}
	parallel := ml.Parallel
	if parallel <= 0 {
		parallel = 1
	}
	if ml.Ctx > 0 {
		// llama-server splits the context between slots: give each the full size.
		args = append(args, "-c", strconv.Itoa(ml.Ctx*parallel))
	}
	args = append(args, "-np", strconv.Itoa(parallel))
	args = append(args, servingArgs(ml)...)
	if ml.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(ml.Threads))
	}
	if ml.GPULayers != "" {
		args = append(args, "-ngl", ml.GPULayers)
	}
	if len(ml.RPC) > 0 {
		args = append(args, "--rpc", strings.Join(ml.RPC, ","))
	}
	if !hasJinjaArg(ml.ExtraArgs) {
		args = append(args, "--jinja") // the model's own chat template, which tool calling needs
	}
	args = append(args, ml.ExtraArgs...)
	cmd := exec.Command(rt.ServerPath, args...)
	cmd.Dir = filepath.Dir(rt.ServerPath)
	cmd.Env = llamaEnv(filepath.Dir(rt.ServerPath))
	tail := &tailBuf{}
	cmd.Stdout, cmd.Stderr = tail, tail
	cmd.WaitDelay = 5 * time.Second // don't hang on output pipes a stray child keeps open
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan struct{})
	log.Printf("llama-server started (pid %d, port %d): %s", cmd.Process.Pid, port, strings.Join(args, " "))
	l.mu.Lock()
	l.cmd, l.exited, l.port, l.model, l.opts, l.ready, l.lastErr, l.logTail = cmd, exited, port, ml.Name, ml, false, "", tail
	l.gen++
	gen := l.gen
	l.mu.Unlock()
	go l.watch(cmd, exited, gen, rt, ml, modelPath, restarts)
	return nil
}

// watch restarts llama-server if it exits unexpectedly (3 tries).
func (l *llamaMgr) watch(cmd *exec.Cmd, exited chan struct{}, gen int, rt *runtimeRecord, ml ModelLoad, modelPath string, restarts int) {
	err := cmd.Wait()
	close(exited)
	l.mu.Lock()
	if l.gen != gen { // stopped or replaced on purpose
		l.mu.Unlock()
		return
	}
	tail := ""
	if l.logTail != nil {
		tail = lastLines(l.logTail.String(), 4)
	}
	l.cmd, l.exited, l.ready = nil, nil, false
	l.lastErr = fmt.Sprintf("llama-server exited (%v): %s", err, tail)
	l.mu.Unlock()
	log.Printf("%s", l.lastErr)
	if restarts >= 3 {
		l.n.opStatus("model.load", ml.Name, "failed", 0, 0, l.lastErr)
		return
	}
	time.Sleep(2 * time.Second)
	if err := l.start(rt, ml, modelPath, restarts+1); err == nil {
		_ = l.waitReady(15 * time.Minute)
	}
}

func (l *llamaMgr) baseURL() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return fmt.Sprintf("http://127.0.0.1:%d", l.port)
}

func (l *llamaMgr) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 3 * time.Second}
	for time.Now().Before(deadline) {
		l.mu.Lock()
		running, tail := l.cmd != nil, l.logTail
		l.mu.Unlock()
		if !running {
			msg := "llama-server stopped while loading"
			if tail != nil {
				msg += ": " + lastLines(tail.String(), 4)
			}
			return errors.New(msg)
		}
		resp, err := client.Get(l.baseURL() + "/health")
		if err == nil {
			ok := resp.StatusCode == 200
			resp.Body.Close()
			if ok {
				l.mu.Lock()
				l.ready = true
				l.mu.Unlock()
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	return errors.New("timed out waiting for llama-server to load the model")
}

// chat runs one OpenAI-style chat completion against llama-server, streaming
// tokens through emit. It returns the final text and usage.
func (l *llamaMgr) chat(ctx context.Context, spec InferenceSpec, emit func(string)) (*InferenceOutput, *ErrInfo) {
	return l.chatP(ctx, spec, func(_ bool, s string) { emit(s) }, nil)
}

// promptProgress is llama-server's report while it reads the prompt
// (return_progress; older builds ignore the request and send none).
type promptProgress struct {
	Total     int   `json:"total"`
	Cache     int   `json:"cache"`
	Processed int   `json:"processed"`
	TimeMs    int64 `json:"time_ms"`
}

// chatP is chat with an optional prompt-progress callback.
// emit gets reasoning (thinking models) with reasoning true, the answer with false.
func (l *llamaMgr) chatP(ctx context.Context, spec InferenceSpec, emit func(reasoning bool, text string), prog func(promptProgress)) (*InferenceOutput, *ErrInfo) {
	if e := l.wake(spec.Model); e != nil {
		return nil, e
	}
	l.mu.Lock()
	if l.cmd == nil || !l.ready {
		l.mu.Unlock()
		return nil, errf("inference_unavailable", true, "no model is loaded and ready on this node")
	}
	if spec.Model != "" && spec.Model != l.model {
		cur := l.model
		l.mu.Unlock()
		return nil, errf("model_missing", true, "node has %s loaded, not %s", cur, spec.Model)
	}
	l.busy++
	l.lastUsed = time.Now()
	model := l.model
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.busy--
		l.lastUsed = time.Now()
		l.mu.Unlock()
	}()

	body := map[string]any{"messages": chatPayload(spec.Messages), "stream": true, "stream_options": map[string]bool{"include_usage": true}}
	if prog != nil {
		body["return_progress"] = true
	}
	if spec.Params.Temperature != nil {
		body["temperature"] = *spec.Params.Temperature
	}
	if spec.Params.MaxTokens > 0 {
		body["max_tokens"] = spec.Params.MaxTokens
	}
	if len(spec.Params.Stop) > 0 {
		body["stop"] = spec.Params.Stop
	}
	if len(spec.Tools) > 0 {
		body["tools"] = spec.Tools
	}
	if len(spec.Params.JSONSchema) > 0 {
		body["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "answer", "schema": spec.Params.JSONSchema}}
	}
	b, _ := json.Marshal(body)
	// The watchdog cancels this request (and restarts llama-server) if the
	// server goes quiet and stops answering its health check.
	outer := ctx
	ctx, cancelReq := context.WithCancel(ctx)
	defer cancelReq()
	dog := l.startWatchdog(ctx, cancelReq)
	req, err := http.NewRequestWithContext(ctx, "POST", l.baseURL()+"/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, errf("bad_request", false, "%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if dog.hung() {
			return nil, dog.err()
		}
		if ctx.Err() != nil {
			return nil, nil // cancelled or timed out; caller sets status
		}
		return nil, errf("inference_unavailable", true, "%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 400 && len(spec.Params.JSONSchema) > 0 {
		// An older llama.cpp build (or a schema it cannot turn into a grammar):
		// answer without the constraint instead of failing.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		log.Printf("llama-server refused the JSON schema (%s); answering without it", strings.TrimSpace(string(msg)))
		spec.Params.JSONSchema = nil
		cancelReq()
		return l.chatP(outer, spec, emit, prog)
	}
	if resp.StatusCode != 200 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		retry := resp.StatusCode >= 500
		return nil, errf("inference_failed", retry, "llama-server %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	out := &InferenceOutput{Model: model}
	var text strings.Builder
	var calls []WireToolCall
	var firstTok time.Time
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		dog.alive()
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   *string `json:"content"`
					Reasoning *string `json:"reasoning_content"` // thinking models: shown live, never part of the answer
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
			Progress *promptProgress `json:"prompt_progress"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if chunk.Progress != nil && prog != nil {
			prog(*chunk.Progress)
		}
		for _, c := range chunk.Choices {
			if c.Delta.Reasoning != nil && *c.Delta.Reasoning != "" {
				emit(true, *c.Delta.Reasoning)
			}
			if c.Delta.Content != nil && *c.Delta.Content != "" {
				if firstTok.IsZero() {
					firstTok = time.Now()
				}
				text.WriteString(*c.Delta.Content)
				emit(false, *c.Delta.Content)
			}
			for _, tc := range c.Delta.ToolCalls { // arrives in pieces, keyed by index
				for len(calls) <= tc.Index {
					calls = append(calls, WireToolCall{Type: "function"})
				}
				w := &calls[tc.Index]
				if tc.ID != "" {
					w.ID = tc.ID
				}
				w.Function.Name += tc.Function.Name
				w.Function.Arguments += tc.Function.Arguments
			}
			if c.FinishReason != nil {
				out.FinishReason = *c.FinishReason
			}
		}
		if chunk.Usage != nil {
			out.PromptTokens, out.OutputTokens = chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens
		}
	}
	if dog.hung() {
		return nil, dog.err()
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return nil, errf("inference_failed", true, "stream: %v", err)
	}
	if outer.Err() != nil || ctx.Err() != nil {
		return nil, nil
	}
	out.Text = text.String()
	for _, w := range calls {
		if w.Function.Name != "" {
			out.ToolCalls = append(out.ToolCalls, w)
		}
	}
	if !firstTok.IsZero() && out.OutputTokens > 1 {
		if d := time.Since(firstTok).Seconds(); d > 0 {
			out.TokensPerSec = float64(int(10*float64(out.OutputTokens-1)/d)) / 10
		}
	} else if d := time.Since(start).Seconds(); d > 0 && out.OutputTokens > 0 {
		out.TokensPerSec = float64(int(10*float64(out.OutputTokens)/d)) / 10
	}
	return out, nil
}

// runInference handles a job with kind "inference".
func (n *Node) runInference(ctx context.Context, jd JobDispatch, res *JobResult) {
	var spec InferenceSpec
	if err := json.Unmarshal(jd.Spec, &spec); err != nil || len(spec.Messages) == 0 {
		res.Status, res.Error = "failed", errf("bad_request", false, "inference spec needs messages")
		return
	}
	sw := newStreamer(n, jd.JobID, spec.Stream)
	w, rw := sw.writer("tokens"), sw.writer("reasoning")
	pw := sw.writer("progress")
	out, e := n.llama.chatP(ctx, spec, func(reasoning bool, s string) {
		if reasoning {
			_, _ = rw.Write([]byte(s))
		} else {
			_, _ = w.Write([]byte(s))
		}
		sw.token()
	}, func(p promptProgress) {
		// "processed total cached ms" per report; the controller reads the last line
		fmt.Fprintf(pw, "%d %d %d %d\n", p.Processed, p.Total, p.Cache, p.TimeMs)
	})
	sw.close()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.Status = "timed_out"
	case errors.Is(ctx.Err(), context.Canceled):
		res.Status = "cancelled"
	case e != nil:
		res.Status, res.Error = "failed", e
	default:
		res.Status = "succeeded"
		res.Output, _ = json.Marshal(out)
	}
}

// ---------- idle unload ----------

func (l *llamaMgr) touch() {
	l.mu.Lock()
	l.lastUsed = time.Now()
	l.mu.Unlock()
}

func (l *llamaMgr) setIdleAfter(d time.Duration) {
	l.mu.Lock()
	l.idleAfter = d
	l.lastUsed = time.Now() // a changed setting starts a fresh wait
	l.mu.Unlock()
}

// coldModel is the model unloaded for idleness ("" when none).
func (l *llamaMgr) coldModel() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cold != nil && l.cmd == nil {
		return l.cold.Name
	}
	return ""
}

// idleCheck unloads the model if it has been idle long enough. A pooled model is
// never unloaded: its helpers would have to be restarted by the controller.
func (l *llamaMgr) idleCheck(now time.Time) bool {
	l.mu.Lock()
	if l.idleAfter <= 0 || l.cmd == nil || !l.ready || l.busy > 0 || len(l.opts.RPC) > 0 ||
		now.Sub(l.lastUsed) < l.idleAfter {
		l.mu.Unlock()
		return false
	}
	idle := l.idleAfter
	name := l.goCold()
	log.Printf("model %s was idle for %s; unloading it (it loads again on the next request)", name, formatIdleDuration(idle))
	l.finishCold()
	return true
}

// goCold remembers the loaded model for reloading and returns its name. It must be
// called with l.mu held and releases it; finishCold then stops the process.
func (l *llamaMgr) goCold() string {
	ml := l.opts
	l.cold = &ml // set before stopping, so a request arriving now waits and reloads
	name := l.model
	l.mu.Unlock()
	return name
}

func (l *llamaMgr) finishCold() {
	l.stopProc()
	l.n.resendHello()
}

// errUnloadBusy and friends are the reasons unloadNow refuses; the tray shows them.
var (
	errUnloadNothing = errors.New("no model is loaded")
	errUnloadLoading = errors.New("the model is still loading; try again when it is ready")
	errUnloadBusy    = errors.New("the model is answering a request; try again when the node is idle")
	errUnloadPooled  = errors.New("this model is pooled across nodes; unload it from the controller's Nodes page")
)

// unloadNow unloads the model at once, as an idle unload does: memory is freed and
// the model loads again on the next request for it. It refuses while the model is
// loading, answering a request or pooled (the controller owns pooled helpers).
// Callers check for running jobs first.
func (l *llamaMgr) unloadNow(why string) (string, error) {
	l.mu.Lock()
	switch {
	case l.cmd == nil:
		l.mu.Unlock()
		return "", errUnloadNothing
	case len(l.opts.RPC) > 0:
		l.mu.Unlock()
		return "", errUnloadPooled
	case !l.ready:
		l.mu.Unlock()
		return "", errUnloadLoading
	case l.busy > 0:
		l.mu.Unlock()
		return "", errUnloadBusy
	}
	name := l.goCold()
	log.Printf("unloading model %s (%s); it loads again on the next request", name, why)
	l.finishCold()
	return name, nil
}

func (l *llamaMgr) idleLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			l.idleCheck(now)
		}
	}
}

// wake loads the idle-unloaded model again when a request for it arrives.
func (l *llamaMgr) wake(model string) *ErrInfo {
	l.mu.Lock()
	cold := l.cold
	l.mu.Unlock()
	if cold == nil || (model != "" && model != cold.Name) {
		return nil
	}
	l.wakeMu.Lock()
	defer l.wakeMu.Unlock()
	l.mu.Lock()
	cold = l.cold
	l.mu.Unlock()
	if cold == nil {
		return nil // another request already reloaded it
	}
	log.Printf("request for idle-unloaded model %s; loading it again", cold.Name)
	l.load(*cold)
	l.mu.Lock()
	ready, msg := l.ready, l.lastErr
	l.mu.Unlock()
	if !ready {
		if msg == "" {
			msg = "the model did not load"
		}
		// load() ends a failed start with stop(), which forgets the model. Put it back so the
		// node stays available and the next request tries again, instead of waiting for the
		// Nodes menu.
		l.mu.Lock()
		if l.cmd == nil && l.cold == nil {
			l.cold = cold
		}
		l.mu.Unlock()
		l.n.resendHello()
		return errf("inference_unavailable", true, "reloading %s after idle unload failed: %s", cold.Name, msg)
	}
	return nil
}

// ---------- autoload: reload the last model after a reboot or update ----------

func (n *Node) autoloadPath() string { return filepath.Join(n.llamaDir(), "autoload.json") }

func (n *Node) saveAutoload(ml *ModelLoad) {
	if ml == nil {
		os.Remove(n.autoloadPath())
		return
	}
	_ = os.MkdirAll(n.llamaDir(), 0o755)
	b, _ := json.Marshal(ml)
	_ = writeFileAtomic(n.autoloadPath(), b, 0o644)
}

func (n *Node) autoload() {
	b, err := os.ReadFile(n.autoloadPath())
	if err != nil {
		return
	}
	var ml ModelLoad
	if json.Unmarshal(b, &ml) != nil || ml.Name == "" {
		return
	}
	if n.currentRuntime() == nil {
		return
	}
	if len(ml.RPC) > 0 {
		return // pooled models are started by the controller once the helpers are back
	}
	if _, err := n.modelManifest(ml.Name); err != nil {
		return
	}
	log.Printf("reloading %s (it was loaded before this restart)", ml.Name)
	n.llama.load(ml)
}

// missingLibRe matches the dynamic loader's message for a missing library.
var missingLibRe = regexp.MustCompile(`error while loading shared libraries: ([^:\s]+)`)

// aptPackage names the Debian/Ubuntu package for libraries llama.cpp needs.
var aptPackage = map[string]string{
	"libgomp.so.1": "libgomp1", "libvulkan.so.1": "libvulkan1", "libssl.so.3": "libssl3",
	"libcrypto.so.3": "libssl3", "libstdc++.so.6": "libstdc++6", "libgcc_s.so.1": "libgcc-s1",
}

// checkLlamaRuns runs llama-server --version, supplying bundled libraries the
// system is missing. It returns "" on success or a message saying what to do.
func checkLlamaRuns(server string) string {
	dir := filepath.Dir(server)
	for attempt := 0; attempt < 6; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := exec.CommandContext(ctx, server, "--version")
		cmd.Dir = dir
		cmd.Env = llamaEnv(dir)
		out, err := cmd.CombinedOutput()
		cancel()
		if err == nil || bytes.Contains(out, []byte("version:")) {
			return ""
		}
		m := missingLibRe.FindSubmatch(out)
		if m == nil {
			return fmt.Sprintf("llama-server does not run here: %v %s", err, strings.TrimSpace(lastLines(string(out), 3)))
		}
		lib := string(m[1])
		gz, ok := bundledLibs[lib]
		if ok {
			if _, statErr := os.Stat(filepath.Join(dir, lib)); statErr != nil {
				if werr := writeGunzip(filepath.Join(dir, lib), gz); werr == nil {
					log.Printf("supplied %s from the Outpost binary", lib)
					continue
				} else {
					return fmt.Sprintf("could not write %s: %v", lib, werr)
				}
			}
		}
		if pkg, ok := aptPackage[lib]; ok {
			return fmt.Sprintf("llama.cpp needs %s, which this machine does not have. Install it with: sudo apt install -y %s", lib, pkg)
		}
		return fmt.Sprintf("llama.cpp needs the system library %s, which this machine does not have.", lib)
	}
	return "llama-server still fails to start after supplying bundled libraries"
}

func writeGunzip(dst string, gz []byte) error {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return err
	}
	defer zr.Close()
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, zr); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// pruneLlamaBuilds keeps the current build and the previous one (for rollback).
func pruneLlamaBuilds(root, current, previous string) {
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		p := filepath.Join(root, e.Name())
		if e.IsDir() && p != current && p != previous {
			os.RemoveAll(p)
		}
	}
}

// fetchModel downloads a model straight from Hugging Face (resumable, verified
// by SHA-256 when Hugging Face provides one).
func (n *Node) fetchModel(mf ModelFetch) {
	op := "model.fetch"
	var total int64
	for _, f := range mf.Files {
		total += f.Bytes
	}
	fail := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		log.Printf("model %s: %s", mf.Name, msg)
		n.opStatus(op, mf.Name, "failed", 0, total, msg)
	}
	if !validName(mf.Name) || len(mf.Files) == 0 {
		fail("bad model name")
		return
	}
	defer n.markModelBusy(mf.Name)()
	if _, err := n.modelManifest(mf.Name); err == nil {
		n.opStatus(op, mf.Name, "done", total, total, "")
		return
	}
	dir := filepath.Join(n.modelsDir(), mf.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail("%v", err)
		return
	}
	var have int64
	for _, f := range mf.Files {
		if st, err := os.Stat(filepath.Join(dir, f.Path)); err == nil {
			have += st.Size()
		} else if st, err := os.Stat(filepath.Join(dir, f.Path+".part")); err == nil {
			have += st.Size()
		}
	}
	if err := n.makeRoom(total-have, mf.Name, mf.Reclaim); err != nil {
		fail("%v", err)
		return
	}
	n.opStatus(op, mf.Name, "downloading", 0, total, "")
	client := &http.Client{}
	var before int64
	files := make([]ModelFile, 0, len(mf.Files))
	for _, f := range mf.Files {
		if !validName(f.Path) || !strings.HasPrefix(f.URL, hfBase()+"/") {
			fail("bad file %q", f.Path)
			return
		}
		dst := filepath.Join(dir, f.Path)
		base := before
		sum, err := downloadResumable(context.Background(), client, f.URL, mf.Token, dst, f.Bytes, func(d int64) {
			n.opStatus(op, mf.Name, "downloading", base+d, total, "")
		})
		if err != nil {
			fail("download %s: %v", f.Path, err)
			return
		}
		if f.SHA256 != "" && !strings.EqualFold(sum, f.SHA256) {
			os.Remove(dst)
			fail("%s failed its SHA-256 check; it was deleted, try again", f.Path)
			return
		}
		before += f.Bytes
		files = append(files, ModelFile{Path: f.Path, SHA256: sum, Bytes: f.Bytes})
	}
	b, _ := json.MarshalIndent(modelManifest{Name: mf.Name, Files: files}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o644); err != nil {
		fail("%v", err)
		return
	}
	n.markVerified(mf.Name, true)
	n.touchModel(mf.Name)
	log.Printf("model %s downloaded from Hugging Face (%s)", mf.Name, humanBytes(total))
	n.resendHello() // before "done": the controller checks the model list next
	n.opStatus(op, mf.Name, "done", total, total, "")
}

// chatPayload turns messages into llama-server's OpenAI-style form. A message
// with images becomes a list of content parts (text, then one image_url data
// URI per image); plain messages are sent unchanged.
func chatPayload(msgs []ChatMessage) []any {
	out := make([]any, 0, len(msgs))
	for _, m := range msgs {
		if len(m.Images) == 0 {
			msg := map[string]any{"role": m.Role, "content": m.Content}
			if len(m.ToolCalls) > 0 { // agent mode
				msg["tool_calls"] = m.ToolCalls
			}
			if m.ToolCallID != "" {
				msg["tool_call_id"] = m.ToolCallID
			}
			out = append(out, msg)
			continue
		}
		parts := []map[string]any{{"type": "text", "text": m.Content}}
		for _, img := range m.Images {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": img}})
		}
		out = append(out, map[string]any{"role": m.Role, "content": parts})
	}
	return out
}
