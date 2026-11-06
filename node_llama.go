package main

// Node side of models and inference: installs llama.cpp builds pushed by the
// controller, downloads models from the controller, supervises one
// llama-server process bound to 127.0.0.1, and runs "inference" jobs.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	if !hasMoeArg(ml.ExtraArgs) {
		switch {
		case ml.CPUMoe == "all":
			a = append(a, "--cpu-moe")
		case ml.CPUMoe != "":
			a = append(a, "--n-cpu-moe", ml.CPUMoe)
		}
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

// hasMoeArg is true when the extra arguments already choose where experts go.
func hasMoeArg(args []string) bool {
	for _, a := range args {
		if a == "--cpu-moe" || a == "-cmoe" || a == "--n-cpu-moe" || a == "-ncmoe" || strings.HasPrefix(a, "--n-cpu-moe=") {
			return true
		}
	}
	return false
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
		if len(spec.Params.ToolChoice) > 0 {
			body["tool_choice"] = spec.Params.ToolChoice
		}
	}
	if p := spec.Params; p.TopP != nil {
		body["top_p"] = *p.TopP
	}
	if spec.Params.Seed != nil {
		body["seed"] = *spec.Params.Seed
	}
	if spec.Params.PresencePenalty != nil {
		body["presence_penalty"] = *spec.Params.PresencePenalty
	}
	if spec.Params.FrequencyPenalty != nil {
		body["frequency_penalty"] = *spec.Params.FrequencyPenalty
	}
	if len(spec.Params.ResponseFormat) > 0 && len(spec.Params.JSONSchema) == 0 {
		body["response_format"] = spec.Params.ResponseFormat
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
