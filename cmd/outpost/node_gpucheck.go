package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// GPU speed check: before the first load on a new llama.cpp build (or new GPUs), the node
// times the same small model with everything on the GPU and with everything on the CPU,
// using the llama-bench tool that ships with llama.cpp. Old or weak GPUs (a 2013 Mac Pro's
// FirePro D700 under Metal 2 generated about 6x slower than its CPU) are then left alone:
// loads run on the CPU and the controller stops counting that VRAM. The result is kept in
// the llama folder per build, variant and GPU set, so it runs once.

const (
	gpuCheckTimeout = 3 * time.Minute // per run (CPU and GPU)
	gpuSlowerShare  = 0.9             // the GPU counts as slower when it generates under 90% of the CPU's speed
	gpuCheckMinFile = 100 << 20       // ignore tiny files (drafts, embeddings) as test models
)

// gpuCheckWanted says whether this node's build can use a GPU at all, so a check means something.
// Apple Silicon is left out: its GPU shares memory with the CPU and is always the faster one.
func gpuCheckWanted(variant string, gpus []GPUInfo) bool {
	if len(gpus) == 0 {
		return false
	}
	for _, g := range gpus {
		if g.Vendor == "apple" {
			return false
		}
	}
	return strings.HasSuffix(variant, "-vulkan") || strings.HasSuffix(variant, "-cuda") || variant == "macos-x64" || variant == "macos-x64-compat-metal"
}

// gpuCheckKey names what a check result is valid for.
func gpuCheckKey(rt *runtimeRecord, gpus []GPUInfo) string {
	parts := []string{rt.Build, rt.Variant}
	for _, g := range gpus {
		parts = append(parts, fmt.Sprintf("%s/%d", g.Name, g.VRAMBytes>>20))
	}
	return strings.Join(parts, "|")
}

func (n *Node) gpuCheckFile() string { return filepath.Join(n.llamaDir(), "gpucheck.json") }

// savedGPUCheck returns the stored result if it matches key.
func (n *Node) savedGPUCheck(key string) *GPUCheck {
	b, err := os.ReadFile(n.gpuCheckFile())
	if err != nil {
		return nil
	}
	var c GPUCheck
	if json.Unmarshal(b, &c) != nil || c.Key != key {
		return nil
	}
	return &c
}

// currentGPUCheck is the result for the installed build and the GPUs now present, or nil.
func (n *Node) currentGPUCheck(gpus []GPUInfo) *GPUCheck {
	rt := n.currentRuntime()
	if rt == nil || !gpuCheckWanted(rt.Variant, gpus) {
		return nil
	}
	return n.savedGPUCheck(gpuCheckKey(rt, gpus))
}

// clearGPUCheck forgets the result, so the next load checks again.
func (n *Node) clearGPUCheck() {
	_ = os.Remove(n.gpuCheckFile())
	go n.resendHello()
}

// ensureGPUCheck runs the check if this build and GPU set have none yet, and returns the
// result (nil when no check applies). It is called with no llama-server running, so the
// GPU is free.
func (n *Node) ensureGPUCheck(rt *runtimeRecord, model string) *GPUCheck {
	gpus := detectHardware().GPUs
	if !gpuCheckWanted(rt.Variant, gpus) {
		return nil
	}
	key := gpuCheckKey(rt, gpus)
	if c := n.savedGPUCheck(key); c != nil {
		return c
	}
	// A build that was timed before (the node switches between Vulkan and CUDA while it
	// compares them) reuses its timing instead of running again.
	if t := trialFor(n.gpuTrials(gpus), rt.Variant); t != nil && t.Failed == "" && t.GPUTPS > 0 && t.CPUTPS > 0 {
		c := &GPUCheck{Key: key, At: time.Now(), GPUTPS: t.GPUTPS, CPUTPS: t.CPUTPS, Slower: gpuSlower(t.GPUTPS, t.CPUTPS)}
		if b, err := json.Marshal(c); err == nil {
			_ = os.WriteFile(n.gpuCheckFile(), b, 0o644)
		}
		go n.resendHello()
		return c
	}
	n.opStatus("model.load", model, "checking GPU speed", 0, 0, "")
	c := n.runGPUCheck(rt, gpus)
	c.Key, c.At = key, time.Now()
	if c.Skipped == "" {
		t := GPUTrial{Variant: rt.Variant, GPUTPS: c.GPUTPS, CPUTPS: c.CPUTPS}
		if c.GPUTPS == 0 {
			t.Failed = c.Note
		}
		n.recordGPUTrial(gpus, t)
	}
	if b, err := json.Marshal(c); err == nil {
		_ = os.WriteFile(n.gpuCheckFile(), b, 0o644)
	}
	switch {
	case c.Skipped != "":
		log.Printf("GPU speed check skipped: %s", c.Skipped)
	case c.Slower:
		log.Printf("GPU speed check: GPU %.1f tok/s, CPU %.1f tok/s; the GPU is slower here, so models run on the CPU (%s)", c.GPUTPS, c.CPUTPS, c.Note)
	default:
		log.Printf("GPU speed check: GPU %.1f tok/s, CPU %.1f tok/s", c.GPUTPS, c.CPUTPS)
	}
	go n.resendHello() // the controller sizes models from the result
	return c
}

func (n *Node) runGPUCheck(rt *runtimeRecord, gpus []GPUInfo) *GPUCheck {
	bench := filepath.Join(filepath.Dir(rt.ServerPath), "llama-bench")
	if runtime.GOOS == "windows" {
		bench += ".exe"
	}
	if _, err := os.Stat(bench); err != nil {
		return &GPUCheck{Skipped: "this llama.cpp build has no llama-bench"}
	}
	var maxVRAM uint64
	for _, g := range gpus {
		maxVRAM = max(maxVRAM, g.VRAMBytes)
	}
	model := n.gpuCheckModel(int64(maxVRAM) * 6 / 10)
	if model == "" {
		return &GPUCheck{Skipped: "no model on this node is small enough to fit its GPU; it is checked after a smaller model is stored here"}
	}
	c := &GPUCheck{Model: filepath.Base(filepath.Dir(model))}
	cpu, err := runLlamaBench(bench, model, "0")
	if err != nil {
		return &GPUCheck{Skipped: "the CPU run failed: " + err.Error()}
	}
	c.CPUTPS = cpu
	gpu, err := runLlamaBench(bench, model, "99")
	if err != nil {
		c.Slower, c.Note = true, "the GPU run failed: "+err.Error()
		return c
	}
	c.GPUTPS = gpu
	c.Slower = gpuSlower(gpu, cpu)
	return c
}

func gpuSlower(gpu, cpu float64) bool { return cpu > 0 && gpu < cpu*gpuSlowerShare }

// gpuCheckModel picks the smallest stored model file of at least 100 MB that fits under
// limit bytes, or "".
func (n *Node) gpuCheckModel(limit int64) string {
	type cand struct {
		path string
		size int64
	}
	var all []cand
	for _, m := range n.listModels() {
		man, err := n.modelManifest(m.Name)
		if err != nil {
			continue
		}
		var files []string
		var total int64
		for _, f := range man.Files {
			if strings.Contains(strings.ToLower(f.Path), "mmproj") {
				continue
			}
			files = append(files, f.Path)
			total += f.Bytes
		}
		if len(files) == 0 || total < gpuCheckMinFile || total > limit {
			continue
		}
		sort.Strings(files)
		all = append(all, cand{filepath.Join(n.modelsDir(), m.Name, files[0]), total})
	}
	if len(all) == 0 {
		return ""
	}
	sort.Slice(all, func(i, j int) bool { return all[i].size < all[j].size })
	return all[0].path
}

// runLlamaBench times generation for one -ngl setting and returns tokens per second.
func runLlamaBench(bench, model, ngl string) (float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gpuCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bench, "-m", model, "-ngl", ngl, "-p", "0", "-n", "32", "-r", "2", "-o", "json")
	cmd.Dir = filepath.Dir(bench)
	cmd.Env = llamaEnv(cmd.Dir)
	cmd.WaitDelay = 5 * time.Second
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return 0, errors.New("timed out")
	}
	if err != nil {
		return 0, fmt.Errorf("%v: %s", err, lastLines(errBuf.String(), 2))
	}
	return parseLlamaBenchTG(out)
}

// parseLlamaBenchTG reads llama-bench's JSON output and returns the generation speed.
func parseLlamaBenchTG(out []byte) (float64, error) {
	// Some builds print log lines before the JSON array.
	if i := strings.IndexByte(string(out), '['); i > 0 {
		out = out[i:]
	}
	var rows []struct {
		NGen  int     `json:"n_gen"`
		AvgTS float64 `json:"avg_ts"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return 0, fmt.Errorf("unreadable llama-bench output: %v", err)
	}
	for _, r := range rows {
		if r.NGen > 0 && r.AvgTS > 0 {
			return r.AvgTS, nil
		}
	}
	return 0, errors.New("llama-bench reported no generation speed")
}
