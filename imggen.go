package main

// Image generation (design/image-video-generation.md, phase 1). Nodes run
// stable-diffusion.cpp's sd-server the way they run llama-server; the
// controller owns the catalog, the memory estimate and the stored results.
// This file is shared: the catalog, the memory estimate, the job spec and the
// sd-server command line are pure and tested without a GPU.

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
)

// Kinds of generation.
const (
	genImage = "image"
	genVideo = "video"
)

// ImageFileDef is one file of an image model: which sd-server argument takes it
// and where to download it.
type ImageFileDef struct {
	Flag  string `json:"flag"`  // sd-server argument, e.g. --diffusion-model, --vae, --llm, or -m for a single-file checkpoint
	Repo  string `json:"repo"`  // Hugging Face repository
	Match string `json:"match"` // file in the repo: an exact base name, or part of the path that matches exactly one file (case-insensitive)
	Bytes int64  `json:"bytes"` // approximate size for the fit estimate; the real size and checksum come from Hugging Face at download time
	Aux   bool   `json:"aux"`   // text encoder or VAE: can wait in system RAM while the diffusion model runs
}

// ImageModelDef is one entry of the built-in image catalog.
type ImageModelDef struct {
	ID         string         `json:"id"`
	Label      string         `json:"label"`
	Kind       string         `json:"kind"` // image | video
	About      string         `json:"about"`
	License    string         `json:"license"`
	LicenseURL string         `json:"license_url"`
	Restricted bool           `json:"restricted"` // the licence limits use (non-commercial, research): needs an accept click
	Files      []ImageFileDef `json:"files"`
	// Defaults the Create form starts from.
	Size      int     `json:"size"`             // default width and height (video: the width)
	Height    int     `json:"height,omitempty"` // video: default height (Size is the width)
	Frames    int     `json:"frames,omitempty"` // video: default frame count
	FPS       int     `json:"fps,omitempty"`    // video: default frames per second
	Steps     int     `json:"steps"`
	CFG       float64 `json:"cfg"`
	Sampler   string  `json:"sampler"`
	Negative  bool    `json:"negative"`   // the model uses a negative prompt
	ComputeMB int     `json:"compute_mb"` // compute buffer per megapixel, an estimate until a real run on the node reports it
}

// DefaultHeight is the height the form starts from.
func (d *ImageModelDef) DefaultHeight() int {
	if d.Height > 0 {
		return d.Height
	}
	return d.Size
}

// Dir is the model folder name on a node (nodes store image models next to LLMs).
func (d *ImageModelDef) Dir() string { return "img-" + d.ID }

// Bytes is the total download size (approximate).
func (d *ImageModelDef) Bytes() (total, aux int64) {
	for _, f := range d.Files {
		total += f.Bytes
		if f.Aux {
			aux += f.Bytes
		}
	}
	return
}

// imageCatalog is the curated list for v1. File names inside the repositories are
// matched, not hard-coded, and resolved against Hugging Face when a model is
// downloaded; sizes and compute figures are estimates (nothing here has run on a GPU).
var imageCatalog = []ImageModelDef{
	{
		ID: "flux1-schnell-q8", Label: "FLUX.1 schnell (Q8)", Kind: genImage,
		About:   "Fast, high quality images in 4 steps. Needs about 24 GB in total; on a 16 GB card the text encoders wait in system RAM.",
		License: "Apache-2.0", LicenseURL: "https://huggingface.co/black-forest-labs/FLUX.1-schnell",
		Files: []ImageFileDef{
			{Flag: "--diffusion-model", Repo: "leejet/FLUX.1-schnell-gguf", Match: "q8_0.gguf", Bytes: 12_700 << 20},
			{Flag: "--vae", Repo: "black-forest-labs/FLUX.1-schnell", Match: "ae.safetensors", Bytes: 335 << 20, Aux: true},
			{Flag: "--clip_l", Repo: "comfyanonymous/flux_text_encoders", Match: "clip_l.safetensors", Bytes: 246 << 20, Aux: true},
			{Flag: "--t5xxl", Repo: "comfyanonymous/flux_text_encoders", Match: "t5xxl_fp16.safetensors", Bytes: 9_800 << 20, Aux: true},
		},
		Size: 1024, Steps: 4, CFG: 1, Sampler: "euler", ComputeMB: 1300,
	},
	{
		ID: "sdxl-turbo", Label: "SDXL-Turbo", Kind: genImage,
		About:   "One file, 1 to 4 steps: very quick drafts at 512 to 1024 pixels. Works on small GPUs and, slowly, on CPUs.",
		License: "Stability AI non-commercial research licence", LicenseURL: "https://huggingface.co/stabilityai/sdxl-turbo", Restricted: true,
		Files: []ImageFileDef{
			{Flag: "-m", Repo: "stabilityai/sdxl-turbo", Match: "sd_xl_turbo_1.0_fp16.safetensors", Bytes: 6_900 << 20},
		},
		Size: 512, Steps: 4, CFG: 1, Sampler: "euler_a", Negative: true, ComputeMB: 1800,
	},
	{
		ID: "z-image-turbo-q4", Label: "Z-Image Turbo (Q4)", Kind: genImage,
		About:   "Modern 6B model in 8 steps with a Qwen text encoder; good at text in pictures. Fits a 12 GB card.",
		License: "Apache-2.0", LicenseURL: "https://huggingface.co/Tongyi-MAI/Z-Image-Turbo",
		Files: []ImageFileDef{
			{Flag: "--diffusion-model", Repo: "leejet/Z-Image-Turbo-GGUF", Match: "q4_k", Bytes: 3_900 << 20},
			{Flag: "--vae", Repo: "Comfy-Org/z_image_turbo", Match: "ae.safetensors", Bytes: 335 << 20, Aux: true},
			{Flag: "--llm", Repo: "unsloth/Qwen3-4B-Instruct-2507-GGUF", Match: "Qwen3-4B-Instruct-2507-Q4_K_M.gguf", Bytes: 2_500 << 20, Aux: true},
		},
		Size: 1024, Steps: 8, CFG: 1, Sampler: "euler", ComputeMB: 1200,
	},
	{
		ID: "wan21-t2v-1.3b", Label: "Wan 2.1 text-to-video 1.3B", Kind: genVideo,
		About:   "Short clips (about 2 seconds at 832 by 480) from a prompt. The smallest Wan model: slow, but it fits a 12 GB card and, very slowly, a CPU. Expect minutes per clip.",
		License: "Apache-2.0", LicenseURL: "https://huggingface.co/Wan-AI/Wan2.1-T2V-1.3B",
		Files: []ImageFileDef{
			{Flag: "--diffusion-model", Repo: "Comfy-Org/Wan_2.1_ComfyUI_repackaged", Match: "wan2.1_t2v_1.3B_fp16.safetensors", Bytes: 2_900 << 20},
			{Flag: "--vae", Repo: "Comfy-Org/Wan_2.1_ComfyUI_repackaged", Match: "wan_2.1_vae.safetensors", Bytes: 254 << 20, Aux: true},
			{Flag: "--t5xxl", Repo: "city96/umt5-xxl-encoder-gguf", Match: "umt5-xxl-encoder-Q8_0.gguf", Bytes: 6_200 << 20, Aux: true},
		},
		Size: 832, Height: 480, Frames: 33, FPS: 16, Steps: 20, CFG: 6, Sampler: "euler", Negative: true, ComputeMB: 150,
	},
}

// imageModel finds a catalog entry.
func imageModel(id string) *ImageModelDef {
	for i := range imageCatalog {
		if imageCatalog[i].ID == id {
			return &imageCatalog[i]
		}
	}
	return nil
}

const (
	genOverheadBytes = 500 << 20
	maxGenSide       = 2048
	minGenSide       = 256
	maxVideoSide     = 1280
	maxVideoFrames   = 129
)

// estimateImage returns the memory a generation needs: full with everything in
// GPU memory, and gpu with the text encoders and VAE waiting in system RAM.
// observed, when above zero, is a compute buffer a node measured on a real run
// at about this size and replaces the per-megapixel guess.
func estimateImage(d *ImageModelDef, w, h int, observed int64) (full, gpu int64) {
	return estimateGen(d, w, h, 0, observed)
}

// videoLatentFrames is how many latent frames a clip of n frames has (4 frames per latent, plus one).
func videoLatentFrames(frames int) int {
	if frames < 1 {
		return 1
	}
	return (frames-1)/4 + 1
}

// estimateGen is estimateImage for pictures (frames 0) and clips: for a clip the
// compute buffer also grows with the number of latent frames.
func estimateGen(d *ImageModelDef, w, h, frames int, observed int64) (full, gpu int64) {
	total, aux := d.Bytes()
	compute := observed
	if compute <= 0 {
		mp := float64(w) * float64(h) / 1e6
		if mp < 0.25 {
			mp = 0.25
		}
		if frames > 0 {
			mp *= float64(videoLatentFrames(frames))
		}
		compute = int64(float64(d.ComputeMB) * mp * (1 << 20))
	}
	gpu = total - aux + compute + genOverheadBytes
	full = total + compute + genOverheadBytes
	return
}

// imageFit classifies the model on a node with the given memory (the same levels
// as LLMs: fitVRAM, fitSplit, fitRAM, fitNo). offload reports that the node has to
// keep the text encoders and VAE in system RAM (--offload-to-cpu).
func imageFit(d *ImageModelDef, w, h int, observed, vram, ram int64) (fit string, offload bool) {
	return genFit(d, w, h, 0, observed, vram, ram)
}

// genFit is imageFit for clips too (frames above 0).
func genFit(d *ImageModelDef, w, h, frames int, observed, vram, ram int64) (fit string, offload bool) {
	full, gpu := estimateGen(d, w, h, frames, observed)
	_, aux := d.Bytes()
	usableVRAM := vram * 92 / 100
	usableRAM := ram * 75 / 100
	switch {
	case vram > 0 && full <= usableVRAM:
		return fitVRAM, false
	case vram > 0 && gpu <= usableVRAM && aux <= usableRAM:
		return fitSplit, true
	case vram == 0 && full <= usableRAM:
		return fitRAM, false
	}
	return fitNo, false
}

// sdVariant picks the stable-diffusion.cpp build for a node. The project ships
// Linux x86-64 (CPU and Vulkan), Windows x64 (CPU and Vulkan) and a macOS build;
// there is no Linux ARM or Windows ARM build.
func sdVariant(h *Hello) (variant, note string) {
	if h == nil {
		return "", "unknown node"
	}
	hasGPU := false
	for _, g := range h.GPUs {
		if g.Vendor != "apple" {
			hasGPU = true
		}
	}
	switch h.OS {
	case "linux":
		if h.Arch != "amd64" {
			return "", "stable-diffusion.cpp has no Linux build for this CPU"
		}
		if hasGPU && h.VulkanAvailable {
			return "linux-x64-vulkan", ""
		}
		if hasGPU {
			return "linux-x64-cpu", "GPU found but no Vulkan driver; install libvulkan1 and mesa-vulkan-drivers to use it"
		}
		return "linux-x64-cpu", ""
	case "windows":
		if h.Arch != "amd64" {
			return "", "stable-diffusion.cpp has no Windows build for this CPU"
		}
		if hasGPU {
			return "win-x64-vulkan", ""
		}
		return "win-x64-cpu", ""
	case "darwin":
		return "macos", ""
	}
	return "", "unsupported OS"
}

// sdAssetMatch says whether a release file is the stable-diffusion.cpp build for
// variant. Names look like sd-master-<sha>-bin-Linux-Ubuntu-24.04-x86_64-vulkan.zip,
// sd-master-<sha>-bin-win-vulkan-x64.zip and sd-master-<sha>-bin-Darwin-macOS-15-arm64.zip.
func sdAssetMatch(name, variant string) bool {
	n := strings.ToLower(name)
	if !strings.HasPrefix(n, "sd-") || !strings.HasSuffix(n, ".zip") || !strings.Contains(n, "-bin-") ||
		strings.Contains(n, "rocm") || strings.Contains(n, "cuda") || strings.Contains(n, "cudart") {
		return false
	}
	switch variant {
	case "linux-x64-vulkan":
		return strings.Contains(n, "-bin-linux-") && strings.HasSuffix(n, "-x86_64-vulkan.zip")
	case "linux-x64-cpu":
		return strings.Contains(n, "-bin-linux-") && strings.HasSuffix(n, "-x86_64.zip")
	case "win-x64-vulkan":
		return strings.Contains(n, "-bin-win-") && strings.HasSuffix(n, "-vulkan-x64.zip")
	case "win-x64-cpu":
		return strings.Contains(n, "-bin-win-") && strings.HasSuffix(n, "-cpu-x64.zip")
	case "macos":
		return strings.Contains(n, "-bin-darwin-")
	}
	return false
}

// GenArg is one model file on the sd-server command line.
type GenArg struct {
	Flag string `json:"flag"`
	File string `json:"file"` // file name inside the model folder
}

// GenSpec is the spec of a job with kind "generate" (image or video).
type GenSpec struct {
	Kind     string   `json:"kind"`
	Model    string   `json:"model"` // catalog ID
	Dir      string   `json:"dir"`   // model folder on the node
	Files    []GenArg `json:"files"`
	Offload  bool     `json:"offload,omitempty"`
	Prompt   string   `json:"prompt"`
	Negative string   `json:"negative,omitempty"`
	Width    int      `json:"width"`
	Height   int      `json:"height"`
	Steps    int      `json:"steps"`
	CFG      float64  `json:"cfg"`
	Sampler  string   `json:"sampler,omitempty"`
	Seed     int64    `json:"seed"` // -1 = random
	Threads  int      `json:"threads,omitempty"`
	Frames   int      `json:"frames,omitempty"` // video only
	FPS      int      `json:"fps,omitempty"`    // video only

}

// GenOutput is the result of a generate job.
type GenOutput struct {
	Seed         int64   `json:"seed"`
	Seconds      float64 `json:"seconds"`
	ComputeBytes int64   `json:"compute_bytes,omitempty"` // peak compute buffer sd-server reported, for the next estimate
	Format       string  `json:"format"`                  // file extension: png for pictures; webm, avi or webp for clips
}

// genExts are the file types a generation may be stored as, with their media types.
var genExts = map[string]string{"png": "image/png", "webm": "video/webm", "avi": "video/x-msvideo", "webp": "image/webp"}

// genExt cleans a node-reported format into a stored extension.
func genExt(kind, format string) string {
	if _, ok := genExts[format]; ok {
		return format
	}
	if kind == genVideo {
		return "webm"
	}
	return "png"
}

func (s *GenSpec) validate() error {
	if strings.TrimSpace(s.Prompt) == "" {
		return fmt.Errorf("the prompt is empty")
	}
	if len(s.Prompt) > 4000 || len(s.Negative) > 4000 {
		return fmt.Errorf("the prompt is too long")
	}
	maxSide := maxGenSide
	if s.Kind == genVideo {
		maxSide = maxVideoSide
		if s.Frames < 5 || s.Frames > maxVideoFrames {
			return fmt.Errorf("a clip needs 5 to %d frames", maxVideoFrames)
		}
		if s.FPS < 4 || s.FPS > 60 {
			return fmt.Errorf("frames per second must be 4 to 60")
		}
	}
	if s.Width < minGenSide || s.Height < minGenSide || s.Width > maxSide || s.Height > maxSide || s.Width%8 != 0 || s.Height%8 != 0 {
		return fmt.Errorf("the size must be %d to %d pixels each way, in steps of 8", minGenSide, maxSide)
	}
	if s.Steps < 1 || s.Steps > 150 {
		return fmt.Errorf("steps must be 1 to 150")
	}
	if s.CFG < 0 || s.CFG > 30 {
		return fmt.Errorf("guidance must be 0 to 30")
	}
	if !validSampler(s.Sampler) {
		return fmt.Errorf("unknown sampler %q", s.Sampler)
	}
	if len(s.Files) == 0 {
		return fmt.Errorf("the model has no files")
	}
	for _, f := range s.Files {
		if !validGenFlag(f.Flag) || !validName(f.File) {
			return fmt.Errorf("bad model file %q", f.File)
		}
	}
	return nil
}

var genSamplers = []string{"", "euler", "euler_a", "heun", "dpm2", "dpm++2m", "dpm++2mv2", "dpm++2s_a", "lcm", "er_sde"}

func validSampler(s string) bool {
	for _, k := range genSamplers {
		if s == k {
			return true
		}
	}
	return false
}

// genFlags are the model-file arguments a spec may carry. Nothing else reaches the command line.
var genFlags = map[string]bool{
	"-m": true, "--diffusion-model": true, "--vae": true, "--clip_l": true, "--clip_g": true,
	"--t5xxl": true, "--llm": true, "--clip_vision": true, "--taesd": true,
}

func validGenFlag(f string) bool { return genFlags[f] }

// sdServerArgs builds the sd-server command line for a spec. modelDir is the
// absolute model folder on the node; only file names from the node's own manifest reach here.
func sdServerArgs(spec GenSpec, modelDir string, port int) []string {
	args := []string{"--listen-ip", "127.0.0.1", "--listen-port", fmt.Sprint(port)}
	files := append([]GenArg(nil), spec.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Flag < files[j].Flag })
	for _, f := range files {
		args = append(args, f.Flag, path.Join(modelDir, f.File))
	}
	if spec.Kind == genVideo {
		args = append(args, sdVideoModeArgs()...)
	}
	if spec.Offload {
		args = append(args, "--offload-to-cpu")
	}
	if spec.Threads > 0 {
		args = append(args, "-t", fmt.Sprint(spec.Threads))
	}
	return args
}

// pickRepoFile finds the file a catalog entry means among a repository's files.
// An exact base name (case-insensitive) wins; otherwise the match must be part of
// exactly one path. Several candidates are an error rather than a guess.
func pickRepoFile(paths []string, match string) (string, error) {
	m := strings.ToLower(match)
	var exact, part []string
	for _, p := range paths {
		lp := strings.ToLower(p)
		if strings.HasSuffix(lp, ".md") || strings.HasSuffix(lp, ".json") || strings.HasSuffix(lp, ".txt") || strings.HasSuffix(lp, ".gitattributes") {
			continue
		}
		if path.Base(lp) == m {
			exact = append(exact, p)
		} else if strings.Contains(lp, m) {
			part = append(part, p)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(exact) > 1:
		return "", fmt.Errorf("%d files named %s", len(exact), match)
	case len(part) == 1:
		return part[0], nil
	case len(part) == 0:
		return "", fmt.Errorf("no file matching %q", match)
	}
	return "", fmt.Errorf("%d files match %q (%s)", len(part), match, strings.Join(part, ", "))
}

// sdVideoModeArgs are the extra sd-server arguments that start it in video mode.
// The stable-diffusion.cpp documentation does not say how the server picks video
// mode, so the default is the CLI's "-M vid_gen" and OUTPOST_SD_VIDEO_ARGS replaces
// it ("none" for no extra arguments) until it has been checked on a real node.
func sdVideoModeArgs() []string {
	v, ok := os.LookupEnv("OUTPOST_SD_VIDEO_ARGS")
	if !ok {
		return []string{"-M", "vid_gen"}
	}
	if strings.TrimSpace(v) == "none" {
		return nil
	}
	return strings.Fields(v)
}
