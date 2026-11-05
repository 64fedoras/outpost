package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ModelShape is what the KV cache size depends on, read from a GGUF file's
// header. KV cache bytes for n tokens (f16 cache, llama.cpp's default) are
// about FullBytesPerToken*n + SWABytesPerToken*min(n, SWAWindow).
type ModelShape struct {
	Arch              string `json:"arch"`
	Layers            int    `json:"layers"`
	TrainCtx          int    `json:"train_ctx,omitempty"`  // context length the model was trained for
	FullBytesPerToken int64  `json:"full_bytes_per_token"` // layers that cache every token
	SWABytesPerToken  int64  `json:"swa_bytes_per_token"`  // sliding-window layers
	SWAWindow         int    `json:"swa_window,omitempty"` // tokens a sliding-window layer keeps
	// ActiveFrac is the share of the weights one generated token reads: below 1
	// for mixture-of-experts models, which only use some experts per token.
	// 0 means unknown (treated as 1).
	ActiveFrac float64 `json:"active_frac,omitempty"`
}

// activeBytes is how many bytes of a model file of the given size one token reads.
func (s *ModelShape) activeBytes(fileBytes int64) int64 {
	if s == nil || s.ActiveFrac <= 0 || s.ActiveFrac >= 1 {
		return fileBytes
	}
	return int64(float64(fileBytes) * s.ActiveFrac)
}

// kvBytes estimates the KV cache for ctx tokens.
func (s *ModelShape) kvBytes(ctx int) int64 {
	swa := ctx
	if s.SWAWindow > 0 && swa > s.SWAWindow {
		swa = s.SWAWindow
	}
	return s.FullBytesPerToken*int64(ctx) + s.SWABytesPerToken*int64(swa)
}

// modelShapeTimeout bounds reading a header from Hugging Face.
const modelShapeTimeout = 2 * time.Minute

const (
	ggufU8 = iota
	ggufI8
	ggufU16
	ggufI16
	ggufU32
	ggufI32
	ggufF32
	ggufBool
	ggufString
	ggufArray
	ggufU64
	ggufI64
	ggufF64
)

// ggufValue is a parsed metadata value: a number, a string, or a slice of
// numbers (string arrays such as the tokenizer vocabulary are skipped).
type ggufValue struct {
	num  int64
	nums []int64
	str  string
	isN  bool
}

type ggufReader struct {
	r   *bufio.Reader
	err error
}

func (g *ggufReader) u32() uint32 {
	var b [4]byte
	if g.err == nil {
		_, g.err = io.ReadFull(g.r, b[:])
	}
	return binary.LittleEndian.Uint32(b[:])
}

func (g *ggufReader) u64() uint64 {
	var b [8]byte
	if g.err == nil {
		_, g.err = io.ReadFull(g.r, b[:])
	}
	return binary.LittleEndian.Uint64(b[:])
}

func (g *ggufReader) skip(n uint64) {
	if g.err == nil {
		_, g.err = g.r.Discard(int(n))
	}
}

func (g *ggufReader) str(keep bool) string {
	n := g.u64()
	if g.err != nil {
		return ""
	}
	if n > 1<<20 {
		g.err = fmt.Errorf("gguf: string of %d bytes", n)
		return ""
	}
	if !keep {
		g.skip(n)
		return ""
	}
	b := make([]byte, n)
	if g.err == nil {
		_, g.err = io.ReadFull(g.r, b)
	}
	return string(b)
}

// scalar reads one value of type t; numbers come back as int64 (floats are
// truncated, which is fine for the keys used here).
func (g *ggufReader) scalar(t uint32, keepStr bool) (int64, string) {
	var b [8]byte
	size := map[uint32]int{ggufU8: 1, ggufI8: 1, ggufBool: 1, ggufU16: 2, ggufI16: 2, ggufU32: 4, ggufI32: 4, ggufF32: 4, ggufU64: 8, ggufI64: 8, ggufF64: 8}[t]
	if t == ggufString {
		return 0, g.str(keepStr)
	}
	if size == 0 {
		g.err = fmt.Errorf("gguf: unknown value type %d", t)
		return 0, ""
	}
	if g.err == nil {
		_, g.err = io.ReadFull(g.r, b[:size])
	}
	switch t {
	case ggufI8:
		return int64(int8(b[0])), ""
	case ggufI16:
		return int64(int16(binary.LittleEndian.Uint16(b[:]))), ""
	case ggufU16:
		return int64(binary.LittleEndian.Uint16(b[:])), ""
	case ggufI32:
		return int64(int32(binary.LittleEndian.Uint32(b[:]))), ""
	case ggufU32:
		return int64(binary.LittleEndian.Uint32(b[:])), ""
	case ggufF32:
		return int64(math.Float32frombits(binary.LittleEndian.Uint32(b[:]))), ""
	case ggufF64:
		return int64(math.Float64frombits(binary.LittleEndian.Uint64(b[:]))), ""
	case ggufU64, ggufI64:
		return int64(binary.LittleEndian.Uint64(b[:])), ""
	}
	return int64(b[0]), ""
}

// readGGUFMeta reads the metadata of a GGUF file (versions 2 and 3) and stops
// before the tensor table, so only the header is read.
func readGGUFMeta(r io.Reader) (map[string]ggufValue, error) {
	meta, _, err := readGGUFHeader(r, false)
	return meta, err
}

// ggufTensor is one entry of the tensor table.
type ggufTensor struct {
	name  string
	bytes int64
}

// ggufBlock is (elements per block, bytes per block) for each ggml tensor type.
var ggufBlock = map[uint32][2]int64{
	0: {1, 4}, 1: {1, 2}, 2: {32, 18}, 3: {32, 20}, 6: {32, 22}, 7: {32, 24}, 8: {32, 34}, 9: {32, 36},
	10: {256, 84}, 11: {256, 110}, 12: {256, 144}, 13: {256, 176}, 14: {256, 210}, 15: {256, 292},
	16: {256, 66}, 17: {256, 74}, 18: {256, 98}, 19: {256, 50}, 20: {32, 18}, 21: {256, 110}, 22: {256, 82},
	23: {256, 136}, 24: {1, 1}, 25: {1, 2}, 26: {1, 4}, 27: {1, 8}, 28: {1, 8}, 29: {256, 56}, 30: {1, 2},
}

// readGGUFHeader reads the metadata and, when wantTensors is set, the tensor
// table that follows it (names and sizes only). The tensor table is nil when a
// tensor has a type this code does not know.
func readGGUFHeader(r io.Reader, wantTensors bool) (map[string]ggufValue, []ggufTensor, error) {
	g := &ggufReader{r: bufio.NewReaderSize(r, 1<<16)}
	if g.u32() != 0x46554747 { // "GGUF"
		if g.err != nil {
			return nil, nil, g.err
		}
		return nil, nil, errors.New("not a GGUF file")
	}
	if v := g.u32(); v < 2 && g.err == nil {
		return nil, nil, fmt.Errorf("gguf version %d is not supported", v)
	}
	ntensors := g.u64()
	n := g.u64()
	if n > 1<<16 {
		return nil, nil, fmt.Errorf("gguf: %d metadata keys", n)
	}
	out := map[string]ggufValue{}
	for i := uint64(0); i < n && g.err == nil; i++ {
		key := g.str(true)
		t := g.u32()
		if t == ggufArray {
			et, cnt := g.u32(), g.u64()
			if et == ggufString || cnt > 4096 {
				for j := uint64(0); j < cnt && g.err == nil; j++ {
					g.scalar(et, false)
				}
				continue
			}
			v := ggufValue{nums: make([]int64, 0, cnt)}
			for j := uint64(0); j < cnt && g.err == nil; j++ {
				x, _ := g.scalar(et, false)
				v.nums = append(v.nums, x)
			}
			out[key] = v
			continue
		}
		x, s := g.scalar(t, true)
		out[key] = ggufValue{num: x, str: s, isN: t != ggufString}
	}
	if g.err != nil {
		return nil, nil, fmt.Errorf("gguf header: %w", g.err)
	}
	if !wantTensors || ntensors > 1<<20 {
		return out, nil, nil
	}
	var tensors []ggufTensor
	known := true
	for i := uint64(0); i < ntensors && g.err == nil; i++ {
		name := g.str(true)
		nd := g.u32()
		if nd > 8 {
			return out, nil, nil
		}
		elems := int64(1)
		for d := uint32(0); d < nd; d++ {
			elems *= int64(g.u64())
		}
		typ := g.u32()
		g.u64() // data offset
		blk, ok := ggufBlock[typ]
		if !ok {
			known = false
			continue
		}
		tensors = append(tensors, ggufTensor{name: name, bytes: elems / blk[0] * blk[1]})
	}
	if g.err != nil || !known {
		return out, nil, nil // the metadata alone is still useful
	}
	return out, tensors, nil
}

// activeFracFromTensors is the share of the weights that one token reads. Expert
// tensors (ffn_*_exps) count only for the experts used per token; the token
// embedding is a table lookup, so it only counts when it doubles as the output
// layer. Returns 0 when it cannot be worked out.
func activeFracFromTensors(meta map[string]ggufValue, tensors []ggufTensor) float64 {
	arch := meta["general.architecture"].str
	nExp, nUsed := meta[arch+".expert_count"].num, meta[arch+".expert_used_count"].num
	var total, active float64
	hasOutput := false
	for _, t := range tensors {
		if t.name == "output.weight" {
			hasOutput = true
		}
	}
	for _, t := range tensors {
		b := float64(t.bytes)
		total += b
		switch {
		case strings.Contains(t.name, "_exps."):
			if nExp > 0 && nUsed > 0 && nUsed < nExp {
				active += b * float64(nUsed) / float64(nExp)
			} else {
				active += b
			}
		case t.name == "token_embd.weight" && hasOutput:
		default:
			active += b
		}
	}
	if total <= 0 {
		return 0
	}
	return active / total
}

// shapeFromMeta works out the per-token KV cache size. Per-layer head counts
// (arrays) are used when present; a layer with 0 KV heads has no cache
// (recurrent layers in hybrid models). Hybrid models that only give
// full_attention_interval cache every Nth layer. Sliding-window layers come
// from sliding_window_pattern when the file has it; otherwise every layer is
// counted as full attention, which over-estimates.
func shapeFromMeta(meta map[string]ggufValue) (*ModelShape, error) {
	arch := meta["general.architecture"].str
	if arch == "" {
		return nil, errors.New("gguf: no general.architecture")
	}
	k := func(s string) ggufValue { return meta[arch+"."+s] }
	layers := int(k("block_count").num)
	heads := k("attention.head_count")
	embd := k("embedding_length").num
	if layers <= 0 || embd <= 0 {
		return nil, fmt.Errorf("gguf: %s has no layer count or embedding length", arch)
	}
	at := func(v ggufValue, i int, def int64) int64 {
		if len(v.nums) > i {
			return v.nums[i]
		}
		if v.isN {
			return v.num
		}
		return def
	}
	kvHeads := k("attention.head_count_kv")
	keyLen, valLen := k("attention.key_length").num, k("attention.value_length").num
	interval := int(k("full_attention_interval").num)
	pattern := k("attention.sliding_window_pattern").nums
	s := &ModelShape{Arch: arch, Layers: layers, TrainCtx: int(k("context_length").num), SWAWindow: int(k("attention.sliding_window").num)}
	for i := 0; i < layers; i++ {
		h := at(heads, i, 0)
		kv := at(kvHeads, i, h)
		if kv <= 0 || h <= 0 {
			continue
		}
		if interval > 1 && (i+1)%interval != 0 {
			continue
		}
		kl, vl := keyLen, valLen
		if kl <= 0 {
			kl = embd / h
		}
		if vl <= 0 {
			vl = kl
		}
		per := kv * (kl + vl) * 2 // f16
		if s.SWAWindow > 0 && i < len(pattern) && pattern[i] != 0 {
			s.SWABytesPerToken += per
		} else {
			s.FullBytesPerToken += per
		}
	}
	if s.SWABytesPerToken == 0 {
		s.SWAWindow = 0
	}
	if s.FullBytesPerToken+s.SWABytesPerToken == 0 {
		return nil, fmt.Errorf("gguf: %s has no attention heads", arch)
	}
	return s, nil
}

func readGGUFShape(r io.Reader) (*ModelShape, error) {
	meta, tensors, err := readGGUFHeader(io.LimitReader(r, 256<<20), true)
	if err != nil {
		return nil, err
	}
	s, err := shapeFromMeta(meta)
	if err != nil {
		return nil, err
	}
	if tensors != nil {
		s.ActiveFrac = activeFracFromTensors(meta, tensors)
	}
	return s, nil
}

// mainModelFile is the first file of a model that is not a vision projector
// (for split models, part 1 holds the metadata).
func mainModelFile(files []string) string {
	for _, f := range files {
		if !isMMProj(f) {
			return f
		}
	}
	return ""
}

// readModelShape reads the model's GGUF header from the controller's copy, or
// streams just the header from Hugging Face for a direct model.
func (c *Controller) readModelShape(ctx context.Context, m *ModelRec) (*ModelShape, error) {
	c.mu.Lock()
	storage, repo, hf := m.Storage, m.Repo, mainModelFile(m.HFFiles)
	var local string
	for _, f := range m.Files {
		if !isMMProj(f.Path) {
			local = filepath.Join(c.modelsStoreDir(), m.Name, f.Path)
			break
		}
	}
	c.mu.Unlock()
	if storage == "controller" && local != "" {
		f, err := os.Open(local)
		if err == nil {
			defer f.Close()
			return readGGUFShape(f)
		}
	}
	if repo == "" || hf == "" {
		return nil, errors.New("no model file to read")
	}
	return c.readShapeFromHF(ctx, repo, hf)
}

// readShapeFromHF streams just the header of a file on Hugging Face.
func (c *Controller) readShapeFromHF(ctx context.Context, repo, hf string) (*ModelShape, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", hfURL(repo, hf), nil)
	if t := c.hfToken(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() // closing early stops the download after the header
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		return nil, fmt.Errorf("Hugging Face: %s", resp.Status)
	}
	return readGGUFShape(resp.Body)
}

// fillModelShape reads and saves a ready model's shape if it is not known yet.
func (c *Controller) fillModelShape(m *ModelRec) {
	c.mu.Lock()
	skip := (m.Shape != nil && m.Shape.ActiveFrac > 0) || m.Status != "ready" || !strings.HasSuffix(strings.ToLower(mainModelFile(m.HFFiles)), ".gguf")
	c.mu.Unlock()
	if skip {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), modelShapeTimeout)
	defer cancel()
	s, err := c.readModelShape(ctx, m)
	if err != nil {
		log.Printf("model %s: could not read the GGUF header for the context estimate: %v", m.Name, err)
		return
	}
	c.mu.Lock()
	if c.models[m.Name] == m {
		m.Shape = s
		c.saveModelLocked(m)
	}
	c.mu.Unlock()
}
