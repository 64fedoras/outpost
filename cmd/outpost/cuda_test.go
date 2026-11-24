package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func winNvidia(driver string, trials ...GPUTrial) *Hello {
	return &Hello{OS: "windows", Arch: "amd64", NvidiaDriver: driver, GPUTrials: trials,
		GPUs: []GPUInfo{{Vendor: "nvidia", Name: "RTX 2080 Super", VRAMBytes: 8 << 30}}}
}

func TestPickWinGPUVariant(t *testing.T) {
	const vk, cu = "win-x64-vulkan", "win-x64-cuda"
	cases := []struct {
		name string
		h    *Hello
		want string
	}{
		{"no timing yet: Vulkan first", winNvidia("560.94"), vk},
		{"Vulkan timed, CUDA not tried: try CUDA", winNvidia("560.94", GPUTrial{Variant: vk, GPUTPS: 50, CPUTPS: 10}), cu},
		{"CUDA clearly faster: keep it", winNvidia("560.94", GPUTrial{Variant: vk, GPUTPS: 50}, GPUTrial{Variant: cu, GPUTPS: 70}), cu},
		{"CUDA about equal: Vulkan", winNvidia("560.94", GPUTrial{Variant: vk, GPUTPS: 50}, GPUTrial{Variant: cu, GPUTPS: 51}), vk},
		{"CUDA slower: Vulkan", winNvidia("560.94", GPUTrial{Variant: vk, GPUTPS: 50}, GPUTrial{Variant: cu, GPUTPS: 30}), vk},
		{"CUDA failed: Vulkan", winNvidia("560.94", GPUTrial{Variant: vk, GPUTPS: 50}, GPUTrial{Variant: cu, Failed: "no device"}), vk},
		{"old driver: Vulkan", winNvidia("535.10", GPUTrial{Variant: vk, GPUTPS: 50}), vk},
		{"no nvidia-smi: Vulkan", winNvidia("", GPUTrial{Variant: vk, GPUTPS: 50}), vk},
	}
	for _, c := range cases {
		if got := pickWinGPUVariant(c.h); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
	h := winNvidia("560.94", GPUTrial{Variant: vk, GPUTPS: 50})
	h.CUDABlocked = true
	if pickWinGPUVariant(h) != vk {
		t.Error("a blocked node must stay on Vulkan")
	}
	h = winNvidia("560.94", GPUTrial{Variant: vk, GPUTPS: 50})
	h.GPUs = append(h.GPUs, GPUInfo{Vendor: "amd", Name: "Radeon", VRAMBytes: 8 << 30})
	if pickWinGPUVariant(h) != vk {
		t.Error("a mixed NVIDIA and AMD machine must stay on Vulkan")
	}
}

func TestNodeVariantWindowsNvidia(t *testing.T) {
	v, _ := nodeVariant(winNvidia("560.94", GPUTrial{Variant: "win-x64-vulkan", GPUTPS: 40}))
	if v != "win-x64-cuda" {
		t.Errorf("variant = %s", v)
	}
	if _, ok := runtimeAssets[v]; !ok {
		t.Error("the CUDA variant has no asset suffix")
	}
}

func TestParseNvidiaDriver(t *testing.T) {
	for in, want := range map[string]string{"551.61\n": "551.61", "560.94\r\n572.1\n": "560.94", "": "", "N/A": "", "NVIDIA-SMI has failed": ""} {
		if got := parseNvidiaDriver(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func writeTestZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for n, c := range files {
		w, _ := zw.Create(n)
		w.Write([]byte(c))
	}
	zw.Close()
	f.Close()
}

func TestMergeZips(t *testing.T) {
	d := t.TempDir()
	a, b, out := filepath.Join(d, "a.zip"), filepath.Join(d, "b.zip"), filepath.Join(d, "m.zip")
	writeTestZip(t, a, map[string]string{"llama-server.exe": "server", "ggml-cuda.dll": "cuda"})
	writeTestZip(t, b, map[string]string{"cudart64_12.dll": "rt", "ggml-cuda.dll": "other"})
	sum, size, err := mergeZips(out, a, b)
	if err != nil || sum == "" || size == 0 {
		t.Fatalf("merge: %v %q %d", err, sum, size)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	got := map[string]bool{}
	for _, f := range zr.File {
		got[f.Name] = true
	}
	if len(zr.File) != 3 || !got["llama-server.exe"] || !got["cudart64_12.dll"] || !got["ggml-cuda.dll"] {
		t.Errorf("merged entries: %v", got)
	}
	// extractZip must read the merged archive
	dest := filepath.Join(d, "x")
	if err := extractZip(out, dest); err != nil || findFile(dest, "cudart64_12.dll") == "" {
		t.Errorf("extract: %v", err)
	}
}

func TestCUDATrialsKeptAcrossSwitches(t *testing.T) {
	n := &Node{dataDir: t.TempDir()}
	os.MkdirAll(n.llamaDir(), 0o755)
	gpus := []GPUInfo{{Vendor: "nvidia", Name: "RTX", VRAMBytes: 8 << 30}}
	n.recordGPUTrial(gpus, GPUTrial{Variant: "win-x64-vulkan", GPUTPS: 50, CPUTPS: 10})
	n.recordGPUTrial(gpus, GPUTrial{Variant: "win-x64-cuda", GPUTPS: 70, CPUTPS: 10})
	n.recordGPUTrial(gpus, GPUTrial{Variant: "win-x64-vulkan", GPUTPS: 55, CPUTPS: 10})
	tr := n.gpuTrials(gpus)
	if len(tr) != 2 || trialFor(tr, "win-x64-vulkan").GPUTPS != 55 || trialFor(tr, "win-x64-cuda").GPUTPS != 70 {
		t.Errorf("trials: %+v", tr)
	}
	if got := n.gpuTrials([]GPUInfo{{Vendor: "nvidia", Name: "Other card", VRAMBytes: 8 << 30}}); got != nil {
		t.Errorf("a different GPU set must not reuse the timings: %+v", got)
	}
}
