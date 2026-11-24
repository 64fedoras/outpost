package main

import "testing"

func TestParseLlamaBenchTG(t *testing.T) {
	out := []byte(`ggml_metal_device_init: GPU name: MTL0
[
  {"build_commit": "abc", "n_prompt": 0, "n_gen": 32, "n_gpu_layers": 99, "avg_ts": 3.88, "stddev_ts": 0.01}
]`)
	v, err := parseLlamaBenchTG(out)
	if err != nil || v != 3.88 {
		t.Fatalf("got %v %v", v, err)
	}
	if _, err := parseLlamaBenchTG([]byte("[]")); err == nil {
		t.Error("empty output must fail")
	}
	if _, err := parseLlamaBenchTG([]byte("crash")); err == nil {
		t.Error("garbage must fail")
	}
}

func TestGPUSlowerAndWanted(t *testing.T) {
	if !gpuSlower(3.9, 22.5) || gpuSlower(21, 22) || gpuSlower(60, 20) || gpuSlower(5, 0) {
		t.Error("gpuSlower thresholds")
	}
	amd := []GPUInfo{{Vendor: "amd", Name: "D700", VRAMBytes: 6 << 30}}
	for v, want := range map[string]bool{"linux-x64-vulkan": true, "win-x64-vulkan": true, "macos-x64-compat-metal": true,
		"macos-x64": true, "linux-x64-cpu": false, "macos-x64-compat": false} {
		if gpuCheckWanted(v, amd) != want {
			t.Errorf("%s: want %v", v, want)
		}
	}
	if gpuCheckWanted("macos-arm64", []GPUInfo{{Vendor: "apple"}}) || gpuCheckWanted("linux-x64-vulkan", nil) {
		t.Error("Apple Silicon and no-GPU nodes are not checked")
	}
	a := gpuCheckKey(&runtimeRecord{RuntimeInfo: RuntimeInfo{Build: "b1", Variant: "linux-x64-vulkan"}}, amd)
	b := gpuCheckKey(&runtimeRecord{RuntimeInfo: RuntimeInfo{Build: "b2", Variant: "linux-x64-vulkan"}}, amd)
	if a == b {
		t.Error("a new build needs a new check")
	}
}

func TestGPUSpeedupFromCheck(t *testing.T) {
	if gpuSpeedup(&Hello{}) != splitGPUSpeedup {
		t.Error("default")
	}
	if gpuSpeedup(&Hello{GPUCheck: &GPUCheck{GPUTPS: 60, CPUTPS: 10}}) != 6 {
		t.Error("measured ratio")
	}
	if gpuSpeedup(&Hello{GPUCheck: &GPUCheck{GPUTPS: 1, CPUTPS: 10}}) != 1 || gpuSpeedup(&Hello{GPUCheck: &GPUCheck{GPUTPS: 900, CPUTPS: 10}}) != 20 {
		t.Error("clamp")
	}
}
