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
	g := &ggufReader{r: bufio.NewReaderSize(r, 1<<16)}
	if g.u32() != 0x46554747 { // "GGUF"
		if g.err != nil {
			return nil, g.err
		}
		return nil, errors.New("not a GGUF file")
	}
	if v := g.u32(); v < 2 && g.err == nil {
		return nil, fmt.Errorf("gguf version %d is not supported", v)
	}
	g.u64() // tensor count
	n := g.u64()
	if n > 1<<16 {
		return nil, fmt.Errorf("gguf: %d metadata keys", n)
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
		return nil, fmt.Errorf("gguf header: %w", g.err)
	}
	return out, nil
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
	meta, err := readGGUFMeta(io.LimitReader(r, 256<<20))
	if err != nil {
		return nil, err
	}
	return shapeFromMeta(meta)
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
	skip := m.Shape != nil || m.Status != "ready" || !strings.HasSuffix(strings.ToLower(mainModelFile(m.HFFiles)), ".gguf")
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
