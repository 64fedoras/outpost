package main

// Embeddings on a node: a second llama-server, started on demand with
// --embeddings for a small embedding model, kept for a few minutes and then
// stopped. It runs on the CPU so the chat model keeps the GPU memory. The
// controller sends jobs of kind "embed" (embed.go); nothing is kept on the node.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	embedIdle     = 5 * time.Minute  // stop the server after this long without a request
	embedStartMax = 3 * time.Minute  // loading a small model
	embedCtx      = 2048             // tokens per text; the controller keeps chunks well under this
	embedCallMax  = 10 * time.Minute // one batch
)

// EmbedSpec is the spec of a job with kind "embed".
type EmbedSpec struct {
	Model string   `json:"model"`
	Texts []string `json:"texts"`
}

// EmbedOutput is the result of an "embed" job.
type EmbedOutput struct {
	Model   string      `json:"model"`
	Dim     int         `json:"dim"`
	Vectors [][]float32 `json:"vectors"`
	Tokens  int         `json:"tokens"`
}

type embedMgr struct {
	n        *Node
	callMu   sync.Mutex // one batch at a time
	mu       sync.Mutex
	cmd      *exec.Cmd
	exited   chan struct{}
	port     int
	model    string
	lastUsed time.Time
	watching bool
}

func newEmbedMgr(n *Node) *embedMgr { return &embedMgr{n: n} }

func (e *embedMgr) stop() {
	e.mu.Lock()
	cmd, exited := e.cmd, e.exited
	e.cmd, e.exited, e.model = nil, nil, ""
	e.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(osInterrupt())
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-exited
	}
}

// ensure starts (or reuses) the server for model and returns its port.
func (e *embedMgr) ensure(ctx context.Context, model string) (int, *ErrInfo) {
	e.mu.Lock()
	if e.cmd != nil && e.model == model {
		select {
		case <-e.exited: // died since the last call
		default:
			e.lastUsed = time.Now()
			p := e.port
			e.mu.Unlock()
			return p, nil
		}
	}
	e.mu.Unlock()
	e.stop()
	rt := e.n.currentRuntime()
	if rt == nil {
		return 0, errf("no_runtime", false, "llama.cpp is not installed on this node yet")
	}
	path, err := e.n.mainModelPath(model)
	if err != nil {
		return 0, errf("no_model", false, "embedding model %s is not on this node", model)
	}
	port, err := freePort()
	if err != nil {
		return 0, errf("internal", true, "%v", err)
	}
	args := []string{"-m", path, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--no-webui", "--embeddings",
		"-c", strconv.Itoa(embedCtx), "-b", strconv.Itoa(embedCtx), "-ub", strconv.Itoa(embedCtx), "-np", "1", "-ngl", "0", "-a", model}
	cmd := exec.Command(rt.ServerPath, args...)
	cmd.Dir = filepath.Dir(rt.ServerPath)
	cmd.Env = llamaEnv(filepath.Dir(rt.ServerPath))
	tail := &tailBuf{}
	cmd.Stdout, cmd.Stderr = tail, tail
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return 0, errf("internal", true, "could not start llama-server: %v", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }() // Wait runs once, here
	log.Printf("embedding server started (pid %d, port %d): %s", cmd.Process.Pid, port, strings.Join(args, " "))
	e.mu.Lock()
	e.cmd, e.exited, e.port, e.model, e.lastUsed = cmd, exited, port, model, time.Now()
	startWatch := !e.watching
	e.watching = true
	e.mu.Unlock()
	if startWatch {
		go e.idleLoop()
	}
	deadline := time.Now().Add(embedStartMax)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			e.mu.Lock()
			e.cmd, e.exited, e.model = nil, nil, ""
			e.mu.Unlock()
			return 0, errf("internal", true, "the embedding server stopped while starting: %s", lastLines(tail.String(), 3))
		case <-ctx.Done():
			e.stop()
			return 0, errf("cancelled", true, "cancelled")
		case <-time.After(300 * time.Millisecond):
		}
		resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/health")
		if err == nil {
			ok := resp.StatusCode == 200
			resp.Body.Close()
			if ok {
				return port, nil
			}
		}
	}
	e.stop()
	return 0, errf("timeout", true, "the embedding server did not become ready in time")
}

// idleLoop stops the server after embedIdle without a request.
func (e *embedMgr) idleLoop() {
	for {
		time.Sleep(30 * time.Second)
		e.mu.Lock()
		idle := e.cmd != nil && time.Since(e.lastUsed) > embedIdle
		gone := e.cmd == nil
		if gone {
			e.watching = false
		}
		e.mu.Unlock()
		if gone {
			return
		}
		if idle {
			e.callMu.Lock() // not in the middle of a batch
			e.mu.Lock()
			still := e.cmd != nil && time.Since(e.lastUsed) > embedIdle
			e.mu.Unlock()
			if still {
				log.Printf("embedding server idle; stopping it")
				e.stop()
			}
			e.callMu.Unlock()
		}
	}
}

// embed turns texts into vectors with the named model.
func (e *embedMgr) embed(ctx context.Context, model string, texts []string) (*EmbedOutput, *ErrInfo) {
	e.callMu.Lock()
	defer e.callMu.Unlock()
	port, ei := e.ensure(ctx, model)
	if ei != nil {
		return nil, ei
	}
	body, _ := json.Marshal(map[string]any{"input": texts, "model": model})
	cctx, cancel := context.WithTimeout(ctx, embedCallMax)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, "POST", "http://127.0.0.1:"+strconv.Itoa(port)+"/v1/embeddings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errf("cancelled", true, "cancelled")
		}
		e.stop() // restart on the next try
		return nil, errf("internal", true, "embedding request failed: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if resp.StatusCode != 200 {
		var er struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &er)
		msg := firstNonEmpty(er.Error.Message, strings.TrimSpace(string(raw)))
		return nil, errf("bad_request", false, "the embedding model refused the text: %s", clip(msg, 300))
	}
	vecs, tokens, err := parseEmbeddings(raw, len(texts))
	if err != nil {
		return nil, errf("internal", true, "%v", err)
	}
	e.mu.Lock()
	e.lastUsed = time.Now()
	e.mu.Unlock()
	out := &EmbedOutput{Model: model, Vectors: vecs, Tokens: tokens}
	if len(vecs) > 0 {
		out.Dim = len(vecs[0])
	}
	return out, nil
}

// parseEmbeddings reads llama-server's OpenAI-style /v1/embeddings answer.
func parseEmbeddings(raw []byte, want int) ([][]float32, int, error) {
	var r struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			Prompt int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, 0, fmt.Errorf("unreadable embedding answer: %v", err)
	}
	if len(r.Data) != want {
		return nil, 0, fmt.Errorf("the embedding server returned %d vectors for %d texts", len(r.Data), want)
	}
	vecs := make([][]float32, want)
	for _, d := range r.Data {
		if d.Index < 0 || d.Index >= want || len(d.Embedding) == 0 {
			return nil, 0, errors.New("the embedding server returned a bad vector")
		}
		vecs[d.Index] = d.Embedding
	}
	dim := len(vecs[0])
	for _, v := range vecs {
		if len(v) != dim {
			return nil, 0, errors.New("the embedding server returned vectors of different sizes")
		}
	}
	return vecs, r.Usage.Prompt, nil
}

// runEmbed handles a job with kind "embed".
func (n *Node) runEmbed(ctx context.Context, jd JobDispatch, res *JobResult) {
	var spec EmbedSpec
	if err := json.Unmarshal(jd.Spec, &spec); err != nil || spec.Model == "" || len(spec.Texts) == 0 {
		res.Status, res.Error = "failed", errf("bad_request", false, "embed spec needs a model and texts")
		return
	}
	out, ei := n.embed.embed(ctx, spec.Model, spec.Texts)
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.Status = "timed_out"
	case errors.Is(ctx.Err(), context.Canceled):
		res.Status = "cancelled"
	case ei != nil:
		res.Status, res.Error = "failed", ei
	default:
		res.Status = "succeeded"
		res.Output, _ = json.Marshal(out)
	}
}
