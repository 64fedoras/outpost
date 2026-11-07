package main

// Node side of image generation: install stable-diffusion.cpp, run its sd-server
// on demand, and turn a "generate" job into a file the controller collects. The
// node keeps the runtime and the model files, never the pictures: the job's
// workspace is deleted once the controller has the result.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sdIdleAfter   = 3 * time.Minute // the server stops (freeing GPU memory) after this long without a job
	sdMaxResponse = 256 << 20
	sdInlineMax   = 16 << 20 // results up to this size travel inside the job result; bigger ones need the peer download
)

func (n *Node) sdDir() string { return filepath.Join(n.dataDir, "sd") }

type sdRuntimeRecord struct {
	RuntimeInfo
	ServerPath string `json:"server_path"`
}

func (n *Node) currentSD() *sdRuntimeRecord {
	b, err := os.ReadFile(filepath.Join(n.sdDir(), "current.json"))
	if err != nil {
		return nil
	}
	var r sdRuntimeRecord
	if json.Unmarshal(b, &r) != nil || r.ServerPath == "" {
		return nil
	}
	if _, err := os.Stat(r.ServerPath); err != nil {
		return nil
	}
	return &r
}

// installSDRuntime downloads and unpacks a stable-diffusion.cpp release the controller mirrors.
func (n *Node) installSDRuntime(ri RuntimeInstall) {
	op := "runtime.sd"
	fail := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		log.Printf("stable-diffusion.cpp %s %s: %s", ri.Build, ri.Variant, msg)
		n.opStatus(op, ri.Variant, "failed", 0, ri.Bytes, msg)
	}
	if !validName(ri.Build) || !validName(ri.Variant) || strings.ContainsAny(ri.Archive, `/\`) {
		fail("bad runtime name")
		return
	}
	if cur := n.currentSD(); cur != nil && cur.Build == ri.Build && cur.Variant == ri.Variant {
		n.opStatus(op, ri.Variant, "done", ri.Bytes, ri.Bytes, "")
		return
	}
	if err := os.MkdirAll(n.sdDir(), 0o755); err != nil {
		fail("%v", err)
		return
	}
	archive := filepath.Join(n.sdDir(), ri.Archive)
	n.opStatus(op, ri.Variant, "downloading", 0, ri.Bytes, "")
	err := n.fetchFile("/v1/runtime/sd-"+ri.Variant, archive, ri.SHA256, ri.Bytes, func(d int64) {
		n.opStatus(op, ri.Variant, "downloading", d, ri.Bytes, "")
	})
	if err != nil {
		fail("%s", withDefenderHint("download: "+err.Error(), n.dataDir))
		return
	}
	n.opStatus(op, ri.Variant, "installing", ri.Bytes, ri.Bytes, "")
	n.sd.stop()
	prev := ""
	if cur := n.currentSD(); cur != nil {
		prev = filepath.Dir(cur.ServerPath)
	}
	dest := filepath.Join(n.sdDir(), ri.Build+"-"+ri.Variant)
	os.RemoveAll(dest)
	err = extractZip(archive, dest)
	os.Remove(archive)
	if err != nil {
		os.RemoveAll(dest)
		fail("extract: %v", err)
		return
	}
	server := findFile(dest, "sd-server"+exeSuffix(runtime.GOOS))
	if server == "" {
		os.RemoveAll(dest)
		fail("sd-server not found in the archive")
		return
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(server, 0o755)
	}
	if msg := checkSDRuns(server); msg != "" {
		os.RemoveAll(dest)
		fail("%s", msg)
		return
	}
	rec := sdRuntimeRecord{RuntimeInfo: RuntimeInfo{Build: ri.Build, Variant: ri.Variant}, ServerPath: server}
	b, _ := json.MarshalIndent(rec, "", "  ")
	if err := writeFileAtomic(filepath.Join(n.sdDir(), "current.json"), b, 0o644); err != nil {
		fail("%v", err)
		return
	}
	if prev != "" && prev != dest {
		os.RemoveAll(prev) // one build is enough; the controller keeps the archive
	}
	log.Printf("stable-diffusion.cpp %s (%s) installed at %s", ri.Build, ri.Variant, server)
	n.opStatus(op, ri.Variant, "done", ri.Bytes, ri.Bytes, "")
	n.resendHello()
}

// checkSDRuns starts sd-server once with --version, the way llama-server is checked,
// supplying bundled libraries (libgomp) the system lacks.
func checkSDRuns(server string) string {
	return checkBinaryRuns(server, "sd-server", "stable-diffusion.cpp", "version")
}

// ---------- the server process ----------

// sdMgr supervises one sd-server. It starts for a job, stays for the next one
// and stops after sdIdleAfter (the Wait is called once, in watch, as everywhere).
type sdMgr struct {
	n        *Node
	mu       sync.Mutex
	cmd      *exec.Cmd
	exited   chan struct{}
	port     int
	loaded   string // which model and options the running server has loaded
	tail     *tailBuf
	lastUsed time.Time
	busy     int
	gen      int
	testURL  string // tests: a stand-in server's address
	runMu    sync.Mutex
}

func newSDMgr(n *Node) *sdMgr { return &sdMgr{n: n} }

func genKey(spec GenSpec) string {
	b, _ := json.Marshal(struct {
		D string
		F []GenArg
		O bool
	}{spec.Dir, spec.Files, spec.Offload})
	return string(b)
}

func (m *sdMgr) running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cmd != nil || m.testURL != ""
}

// newSDCmd builds the sd-server command: its own process group, so stop() can kill
// the whole group. setProcGroup also sets a Cancel function on Unix, which Go only
// accepts on a command made with CommandContext; this one is stopped by stop() instead
// (killProcGroup), so Cancel is cleared. Without that, Start fails with "command with a
// non-nil Cancel was not created with CommandContext".
func newSDCmd(server string, args []string) *exec.Cmd {
	cmd := exec.Command(server, args...)
	cmd.Dir = filepath.Dir(server)
	cmd.Env = llamaEnv(filepath.Dir(server))
	cmd.WaitDelay = 5 * time.Second
	setProcGroup(cmd)
	cmd.Cancel = nil
	return cmd
}

func (m *sdMgr) stop() {
	m.mu.Lock()
	cmd, exited := m.cmd, m.exited
	m.cmd, m.exited, m.loaded = nil, nil, ""
	m.gen++
	m.mu.Unlock()
	if cmd == nil {
		return
	}
	killProcGroup(cmd)
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	if exited != nil {
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
		}
	}
	m.n.resendHello()
}

func (m *sdMgr) baseURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.testURL != "" {
		return m.testURL
	}
	return fmt.Sprintf("http://127.0.0.1:%d", m.port)
}

// ensure makes the server run with spec's model and returns once it answers.
func (m *sdMgr) ensure(ctx context.Context, spec GenSpec, modelDir string) error {
	key := genKey(spec)
	m.mu.Lock()
	if m.testURL != "" {
		m.mu.Unlock()
		return nil
	}
	same := m.cmd != nil && m.loaded == key
	m.mu.Unlock()
	if same {
		return m.waitReady(ctx, 30*time.Second)
	}
	m.stop()
	rt := m.n.currentSD()
	if rt == nil {
		return errors.New("stable-diffusion.cpp is not installed on this node yet")
	}
	port, err := freePort()
	if err != nil {
		return err
	}
	args := sdServerArgs(spec, modelDir, port)
	cmd := newSDCmd(rt.ServerPath, args)
	tail := &tailBuf{}
	cmd.Stdout, cmd.Stderr = tail, tail
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan struct{})
	log.Printf("sd-server started (pid %d, port %d): %s", cmd.Process.Pid, port, strings.Join(args, " "))
	m.mu.Lock()
	m.cmd, m.exited, m.port, m.loaded, m.tail = cmd, exited, port, key, tail
	m.gen++
	gen := m.gen
	m.mu.Unlock()
	go func() {
		err := cmd.Wait()
		close(exited)
		m.mu.Lock()
		if m.gen == gen {
			m.cmd, m.exited, m.loaded = nil, nil, ""
			log.Printf("sd-server exited (%v): %s", err, lastLines(tail.String(), 4))
		}
		m.mu.Unlock()
		m.n.resendHello()
	}()
	// Loading a large model from disk can take minutes.
	return m.waitReady(ctx, 15*time.Minute)
}

func (m *sdMgr) waitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}
	for time.Now().Before(deadline) {
		m.mu.Lock()
		running, tail := m.cmd != nil || m.testURL != "", m.tail
		m.mu.Unlock()
		if !running {
			msg := "sd-server stopped while loading the model"
			if tail != nil {
				msg += ": " + lastLines(tail.String(), 4)
			}
			return errors.New(msg)
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", m.baseURL()+"/sdcpp/v1/models/status", nil)
		if resp, err := client.Do(req); err == nil {
			ok := resp.StatusCode == 200
			resp.Body.Close()
			if ok {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("timed out waiting for sd-server to load the model")
}

// idleLoop stops the server after sdIdleAfter without work, freeing GPU memory.
func (m *sdMgr) idleLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.mu.Lock()
			idle := m.cmd != nil && m.busy == 0 && now.Sub(m.lastUsed) >= sdIdleAfter
			m.mu.Unlock()
			if idle {
				log.Printf("sd-server was idle for %s; stopping it to free memory", sdIdleAfter)
				m.stop()
			}
		}
	}
}

// ---------- the job ----------

type sdJobStatus struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Started int64  `json:"started"`
	Done    int64  `json:"completed"`
	Result  *struct {
		Images []struct {
			B64 string `json:"b64_json"`
		} `json:"images"`
		// a clip (vid_gen): the whole video file in one field
		OutputFormat string `json:"output_format"`
		B64          string `json:"b64_json"`
	} `json:"result"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Preview *struct {
		Step  int `json:"step"`
		Total int `json:"total_steps"`
	} `json:"preview"`
}

var computeBufRe = regexp.MustCompile(`compute buffer size: ([0-9.]+) (MB|GB)`)

// peakCompute reads the biggest compute buffer sd-server reported in its log, in bytes.
func peakCompute(log string) int64 {
	var best int64
	for _, m := range computeBufRe.FindAllStringSubmatch(log, -1) {
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		if m[2] == "GB" {
			v *= 1024
		}
		if b := int64(v * (1 << 20)); b > best {
			best = b
		}
	}
	return best
}

// sdRequestBody is the POST /sdcpp/v1/img_gen body for a spec.
func sdRequestBody(spec GenSpec) map[string]any {
	sp := map[string]any{"sample_steps": spec.Steps, "guidance": map[string]any{"txt_cfg": spec.CFG}}
	if spec.Sampler != "" {
		sp["sample_method"] = spec.Sampler
	}
	if spec.Kind == genVideo {
		return map[string]any{
			"prompt": spec.Prompt, "negative_prompt": spec.Negative,
			"width": spec.Width, "height": spec.Height, "seed": spec.Seed,
			"video_frames": spec.Frames, "fps": spec.FPS,
			"sample_params": sp, "output_format": "webm",
		}
	}
	body := map[string]any{
		"prompt": spec.Prompt, "negative_prompt": spec.Negative,
		"width": spec.Width, "height": spec.Height, "seed": spec.Seed, "batch_count": 1,
		"sample_params": sp, "output_format": "png", "preview": "proj", "preview_interval": 1,
	}
	return body
}

func (n *Node) runGenerate(ctx context.Context, jd JobDispatch, res *JobResult, dir string) {
	var spec GenSpec
	if err := json.Unmarshal(jd.Spec, &spec); err != nil {
		res.Status, res.Error = "failed", errf("bad_request", false, "bad generation spec")
		return
	}
	if err := spec.validate(); err != nil {
		res.Status, res.Error = "failed", errf("bad_request", false, "%v", err)
		return
	}
	if spec.Kind != genImage && spec.Kind != genVideo {
		res.Status, res.Error = "failed", errf("bad_request", false, "this node version cannot make a %q", spec.Kind)
		return
	}
	if !strings.HasPrefix(spec.Dir, "img-") || !validName(spec.Dir) {
		res.Status, res.Error = "failed", errf("bad_request", false, "bad model folder")
		return
	}
	man, err := n.modelManifest(spec.Dir)
	if err != nil {
		res.Status, res.Error = "failed", errf("model_missing", true, "model %s is not on this node", spec.Dir)
		return
	}
	have := map[string]bool{}
	for _, f := range man.Files {
		have[f.Path] = true
	}
	for _, f := range spec.Files {
		if !have[f.File] {
			res.Status, res.Error = "failed", errf("model_missing", true, "%s is missing from %s on this node", f.File, spec.Dir)
			return
		}
	}
	n.sd.runMu.Lock() // one picture at a time: a GPU does not share well
	defer n.sd.runMu.Unlock()
	n.sd.mu.Lock()
	n.sd.busy++
	n.sd.mu.Unlock()
	defer func() {
		n.sd.mu.Lock()
		n.sd.busy--
		n.sd.lastUsed = time.Now()
		n.sd.mu.Unlock()
	}()

	if !n.sd.running() || n.sd.loadedKey() != genKey(spec) {
		// Take the GPU from the chat model; it loads again on its next request.
		if _, err := n.llama.unloadNow("making room for an image model"); err != nil && !errors.Is(err, errUnloadNothing) && !errors.Is(err, errUnloadPooled) {
			res.Status, res.Error = "failed", errf("busy", true, "cannot free GPU memory: %v", err)
			return
		}
		if err := n.verifyModel(spec.Dir); err != nil {
			res.Status, res.Error = "failed", errf("model_corrupt", false, "model %s failed its integrity check: %v", spec.Dir, err)
			return
		}
	}
	n.touchModel(spec.Dir)
	started := time.Now()
	if err := n.sd.ensure(ctx, spec, filepath.Join(n.modelsDir(), spec.Dir)); err != nil {
		if ctx.Err() != nil {
			res.Status = ctxStatus(ctx)
			return
		}
		res.Status, res.Error = "failed", errf("load_failed", true, "%v", err)
		return
	}
	img, ext, e := n.sd.generateExt(ctx, jd.JobID, n, spec)
	switch {
	case ctx.Err() != nil:
		res.Status = ctxStatus(ctx)
		return
	case e != nil:
		res.Status, res.Error = "failed", e
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "out."+ext), img, 0o600); err != nil {
		res.Status, res.Error = "failed", errf("disk_full", true, "%v", err)
		return
	}
	out := GenOutput{Seed: spec.Seed, Seconds: time.Since(started).Seconds(), Format: ext}
	n.sd.mu.Lock()
	if n.sd.tail != nil {
		out.ComputeBytes = peakCompute(n.sd.tail.String())
	}
	n.sd.mu.Unlock()
	res.Output, _ = json.Marshal(out)
	res.Status = "succeeded"
}

func ctxStatus(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timed_out"
	}
	return "cancelled"
}

func (m *sdMgr) loadedKey() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loaded
}

// generate submits one image job to sd-server, reports the step to the controller
// and returns the PNG bytes.
func (m *sdMgr) generate(ctx context.Context, jobID string, n *Node, spec GenSpec) ([]byte, *ErrInfo) {
	b, _, e := m.generateExt(ctx, jobID, n, spec)
	return b, e
}

// generateExt is generate that also says which file type the result is (png, or
// webm, avi or webp for a clip).
func (m *sdMgr) generateExt(ctx context.Context, jobID string, n *Node, spec GenSpec) ([]byte, string, *ErrInfo) {
	client := &http.Client{Timeout: 30 * time.Second}
	endpoint, ext := "/sdcpp/v1/img_gen", "png"
	if spec.Kind == genVideo {
		endpoint, ext = "/sdcpp/v1/vid_gen", "webm"
	}
	submit := func(body map[string]any) (*http.Response, []byte, error) {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(ctx, "POST", m.baseURL()+endpoint, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, nil, err
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		return resp, b, nil
	}
	reqBody := sdRequestBody(spec)
	resp, b, err := submit(reqBody)
	if err == nil && resp.StatusCode == 400 && spec.Kind == genVideo {
		// A build without WebM support refuses the default container; AVI is always there.
		reqBody["output_format"], ext = "avi", "avi"
		resp, b, err = submit(reqBody)
	}
	if err != nil {
		return nil, "", errf("gen_failed", true, "sd-server: %v", err)
	}
	if resp.StatusCode != 202 && resp.StatusCode != 200 {
		return nil, "", errf("gen_failed", resp.StatusCode >= 500, "sd-server refused the job (%s): %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var sub struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(b, &sub) != nil || sub.ID == "" {
		return nil, "", errf("gen_failed", true, "sd-server gave no job id")
	}
	seq, lastStep := 0, -1
	cancel := func() {
		r, _ := http.NewRequest("POST", m.baseURL()+"/sdcpp/v1/jobs/"+sub.ID+"/cancel", nil)
		if resp, err := client.Do(r); err == nil {
			resp.Body.Close()
		}
	}
	for {
		select {
		case <-ctx.Done():
			cancel()
			return nil, "", errf("cancelled", false, "cancelled")
		case <-time.After(500 * time.Millisecond):
		}
		r, _ := http.NewRequestWithContext(ctx, "GET", m.baseURL()+"/sdcpp/v1/jobs/"+sub.ID, nil)
		resp, err := client.Do(r)
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			return nil, "", errf("gen_failed", true, "sd-server: %v", err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, sdMaxResponse))
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, "", errf("gen_failed", true, "sd-server job status %s", resp.Status)
		}
		var st sdJobStatus
		if json.Unmarshal(raw, &st) != nil {
			return nil, "", errf("gen_failed", true, "unreadable answer from sd-server")
		}
		switch st.Status {
		case "completed":
			if spec.Kind == genVideo {
				if st.Result == nil || st.Result.B64 == "" {
					return nil, "", errf("gen_failed", true, "sd-server returned no video")
				}
				vid, err := base64.StdEncoding.DecodeString(st.Result.B64)
				if err != nil || len(vid) == 0 {
					return nil, "", errf("gen_failed", true, "sd-server returned an unreadable video")
				}
				if f := st.Result.OutputFormat; f != "" {
					ext = genExt(genVideo, f)
				}
				return vid, ext, nil
			}
			if st.Result == nil || len(st.Result.Images) == 0 {
				return nil, "", errf("gen_failed", true, "sd-server returned no image")
			}
			img, err := base64.StdEncoding.DecodeString(st.Result.Images[0].B64)
			if err != nil || len(img) == 0 {
				return nil, "", errf("gen_failed", true, "sd-server returned an unreadable image")
			}
			return img, ext, nil
		case "failed", "cancelled":
			msg := st.Status
			if st.Error != nil {
				msg = st.Error.Message
			}
			return nil, "", errf("gen_failed", false, "%s", msg)
		}
		if st.Preview != nil && st.Preview.Total > 0 && st.Preview.Step != lastStep {
			lastStep = st.Preview.Step
			_ = n.send("job.output", JobOutput{JobID: jobID, Seq: seq, Stream: "progress", Data: fmt.Sprintf("%d %d\n", st.Preview.Step, st.Preview.Total)})
			seq++
		}
	}
}

// idle reports that no generation is running (a chat request may take the GPU back).
func (m *sdMgr) idle() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busy == 0 && m.testURL == ""
}

// collectGenerated lists the picture or clip a generate job wrote. Up to sdInlineMax it
// travels inside the result (no peer server needed); bigger files use the blob route.
func (n *Node) collectGenerated(dir, jobID string) ([]FileEntry, *ErrInfo) {
	var name string
	var st os.FileInfo
	for ext := range genExts {
		if fi, err := os.Stat(filepath.Join(dir, "out."+ext)); err == nil {
			name, st = "out."+ext, fi
			break
		}
	}
	if st == nil {
		return nil, errf("collect_failed", true, "no result was written")
	}
	if st.Size() <= sdInlineMax {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, errf("collect_failed", true, "%v", err)
		}
		return []FileEntry{inlineFile(name, data)}, nil
	}
	return n.collectFiles(dir, jobID, []string{name})
}
