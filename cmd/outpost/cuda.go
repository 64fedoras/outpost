package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CUDA for NVIDIA cards (Windows). Outpost's GPU build is Vulkan, which runs on every card.
// An NVIDIA card can usually generate faster with llama.cpp's CUDA build, so the node
// tries it once: Vulkan is installed and timed first (the GPU speed check), then the CUDA
// build is installed and timed the same way, and the faster one stays. Vulkan is always the
// fallback: a CUDA build that will not start, fails its timing or is not faster is dropped.
// The result is kept per GPU set and driver, so the trial happens once per machine.

const (
	// cudaAssetSuffix is the upstream llama.cpp Windows CUDA build (CUDA 12.4: the oldest
	// driver requirement among the published builds). Its runtime libraries come in a
	// separate "cudart" zip of the same release, which the controller merges into one archive.
	cudaAssetSuffix = "-bin-win-cuda-12.4-x64.zip"
	cudartAssetName = "cudart-llama-bin-win-cuda-12.4-x64.zip"
	cudaMinDriver   = "551.61" // the NVIDIA Windows driver CUDA 12.4 needs
	cudaFasterBy    = 1.05     // CUDA must generate at least 5% faster than Vulkan to be kept
	cudaRetryAfter  = 24 * time.Hour
	gpuTrialsFile   = "gputrials.json"
	nvidiaDriverTTL = 10 * time.Minute
)

// cudaUsable says whether a node can try the CUDA build: Windows x64, every GPU an NVIDIA
// card, and a driver new enough. blocked is the controller's memory of a failed install.
func cudaUsable(h *Hello) bool {
	if h.OS != "windows" || h.Arch != "amd64" || h.CUDABlocked || len(h.GPUs) == 0 {
		return false
	}
	for _, g := range h.GPUs {
		if g.Vendor != "nvidia" {
			return false // a mixed machine keeps Vulkan, which drives every card
		}
	}
	return h.NvidiaDriver != "" && compareVersions(h.NvidiaDriver, cudaMinDriver) >= 0
}

// pickWinGPUVariant chooses between the Vulkan and CUDA builds for a Windows node with a GPU.
// Vulkan comes first so there is something to compare against; then CUDA is tried once; the
// faster one is kept. Without a Vulkan timing (no model small enough yet) the node stays on Vulkan.
func pickWinGPUVariant(h *Hello) string {
	const vk, cu = "win-x64-vulkan", "win-x64-cuda"
	if !cudaUsable(h) {
		return vk
	}
	var vt, ct *GPUTrial
	for i := range h.GPUTrials {
		switch h.GPUTrials[i].Variant {
		case vk:
			vt = &h.GPUTrials[i]
		case cu:
			ct = &h.GPUTrials[i]
		}
	}
	switch {
	case vt == nil || vt.GPUTPS <= 0:
		return vk
	case ct == nil:
		return cu // the one-time trial
	case ct.Failed == "" && ct.GPUTPS >= vt.GPUTPS*cudaFasterBy:
		return cu
	}
	return vk
}

// ---------- controller: one archive from the CUDA build and its runtime libraries ----------

// companionFor returns the extra release asset a variant needs, or "".
func companionFor(variant string) string {
	if variant == "win-x64-cuda" {
		return cudartAssetName
	}
	return ""
}

// findAssetNamed finds an asset by exact name in a release.
func findAssetNamed(rels []ghRelease, build, name string) (url string, size int64) {
	for _, r := range rels {
		if r.TagName != build {
			continue
		}
		for _, a := range r.Assets {
			if a.Name == name {
				return a.URL, a.Size
			}
		}
	}
	return "", 0
}

// mergeZips writes dst holding every entry of srcs (the first wins on a repeated name) and
// returns its SHA-256 and size.
func mergeZips(dst string, srcs ...string) (sum string, size int64, err error) {
	out, err := os.Create(dst)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	zw := zip.NewWriter(io.MultiWriter(out, h))
	seen := map[string]bool{}
	fail := func(e error) (string, int64, error) {
		out.Close()
		os.Remove(dst)
		return "", 0, e
	}
	for _, s := range srcs {
		zr, err := zip.OpenReader(s)
		if err != nil {
			return fail(err)
		}
		for _, f := range zr.File {
			if seen[f.Name] {
				continue
			}
			seen[f.Name] = true
			if err := zw.Copy(f); err != nil {
				zr.Close()
				return fail(err)
			}
		}
		zr.Close()
	}
	if err := zw.Close(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return "", 0, err
	}
	st, err := os.Stat(dst)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), st.Size(), nil
}

func (c *Controller) cudaBlocked(id string) bool {
	c.rtCache.mu.Lock()
	defer c.rtCache.mu.Unlock()
	return time.Since(c.rtCache.cudaBad[id]) < cudaRetryAfter
}

// markCUDABad remembers that the CUDA build could not be installed here, so the node stays on
// Vulkan for a day instead of downloading it at every load.
func (c *Controller) markCUDABad(id string) {
	c.rtCache.mu.Lock()
	if c.rtCache.cudaBad == nil {
		c.rtCache.cudaBad = map[string]time.Time{}
	}
	c.rtCache.cudaBad[id] = time.Now()
	c.rtCache.mu.Unlock()
}

// ---------- node: timings per build kept across switches ----------

var nvidiaDrv struct {
	mu  sync.Mutex
	at  time.Time
	val string
}

// nvidiaDriverVersion is the installed NVIDIA driver ("551.61"), or "" without nvidia-smi.
func nvidiaDriverVersion() string {
	nvidiaDrv.mu.Lock()
	defer nvidiaDrv.mu.Unlock()
	if time.Since(nvidiaDrv.at) < nvidiaDriverTTL {
		return nvidiaDrv.val
	}
	nvidiaDrv.at = time.Now()
	nvidiaDrv.val = parseNvidiaDriver(cmdOut("nvidia-smi", "--query-gpu=driver_version", "--format=csv,noheader"))
	return nvidiaDrv.val
}

// parseNvidiaDriver reads the first line of nvidia-smi's driver_version query.
func parseNvidiaDriver(out string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	line = strings.TrimSpace(line)
	if line == "" || strings.Trim(line, "0123456789.") != "" {
		return ""
	}
	return line
}

type gpuTrialFile struct {
	Key    string     `json:"key"` // GPUs and driver the timings were made on
	Trials []GPUTrial `json:"trials"`
}

func gpuTrialKey(gpus []GPUInfo) string {
	parts := []string{nvidiaDriverVersion()}
	for _, g := range gpus {
		parts = append(parts, fmt.Sprintf("%s/%d", g.Name, g.VRAMBytes>>20))
	}
	return strings.Join(parts, "|")
}

// gpuTrials returns the saved timings that belong to the GPUs and driver present now.
func (n *Node) gpuTrials(gpus []GPUInfo) []GPUTrial {
	b, err := os.ReadFile(filepath.Join(n.llamaDir(), gpuTrialsFile))
	if err != nil {
		return nil
	}
	var f gpuTrialFile
	if json.Unmarshal(b, &f) != nil || f.Key != gpuTrialKey(gpus) {
		return nil
	}
	return f.Trials
}

// recordGPUTrial stores one build's timing (replacing an older one of the same variant).
func (n *Node) recordGPUTrial(gpus []GPUInfo, t GPUTrial) {
	trials := n.gpuTrials(gpus)
	out := trials[:0:0]
	for _, o := range trials {
		if o.Variant != t.Variant {
			out = append(out, o)
		}
	}
	out = append(out, t)
	b, err := json.Marshal(gpuTrialFile{Key: gpuTrialKey(gpus), Trials: out})
	if err == nil {
		_ = os.WriteFile(filepath.Join(n.llamaDir(), gpuTrialsFile), b, 0o644)
	}
}

// trialFor returns the saved timing of a variant, if any.
func trialFor(trials []GPUTrial, variant string) *GPUTrial {
	for i := range trials {
		if trials[i].Variant == variant {
			return &trials[i]
		}
	}
	return nil
}
