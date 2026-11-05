package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// ggufBuilder writes a minimal GGUF v3 header for tests.
type ggufBuilder struct {
	b  bytes.Buffer
	n  int
	kv bytes.Buffer
}

func (g *ggufBuilder) key(k string, t uint32) {
	binary.Write(&g.kv, binary.LittleEndian, uint64(len(k)))
	g.kv.WriteString(k)
	binary.Write(&g.kv, binary.LittleEndian, t)
	g.n++
}
func (g *ggufBuilder) u32(k string, v uint32) {
	g.key(k, ggufU32)
	binary.Write(&g.kv, binary.LittleEndian, v)
}
func (g *ggufBuilder) str(k, v string) {
	g.key(k, ggufString)
	binary.Write(&g.kv, binary.LittleEndian, uint64(len(v)))
	g.kv.WriteString(v)
}
func (g *ggufBuilder) arrU32(k string, v []uint32) {
	g.key(k, ggufArray)
	binary.Write(&g.kv, binary.LittleEndian, uint32(ggufU32))
	binary.Write(&g.kv, binary.LittleEndian, uint64(len(v)))
	for _, x := range v {
		binary.Write(&g.kv, binary.LittleEndian, x)
	}
}
func (g *ggufBuilder) arrBool(k string, v []bool) {
	g.key(k, ggufArray)
	binary.Write(&g.kv, binary.LittleEndian, uint32(ggufBool))
	binary.Write(&g.kv, binary.LittleEndian, uint64(len(v)))
	for _, x := range v {
		binary.Write(&g.kv, binary.LittleEndian, x)
	}
}
func (g *ggufBuilder) vocab(k string, words []string) {
	g.key(k, ggufArray)
	binary.Write(&g.kv, binary.LittleEndian, uint32(ggufString))
	binary.Write(&g.kv, binary.LittleEndian, uint64(len(words)))
	for _, w := range words {
		binary.Write(&g.kv, binary.LittleEndian, uint64(len(w)))
		g.kv.WriteString(w)
	}
}
func (g *ggufBuilder) bytes() []byte {
	g.b.WriteString("GGUF")
	binary.Write(&g.b, binary.LittleEndian, uint32(3))
	binary.Write(&g.b, binary.LittleEndian, uint64(0))
	binary.Write(&g.b, binary.LittleEndian, uint64(g.n))
	g.b.Write(g.kv.Bytes())
	g.b.WriteString("tensor data that must not be read")
	return g.b.Bytes()
}

func TestGGUFShapeDense(t *testing.T) {
	// Qwen2.5-Coder-7B-like: 28 layers, 28 heads, 4 KV heads, 3584 embedding (head dim 128).
	g := &ggufBuilder{}
	g.str("general.architecture", "qwen2")
	g.vocab("tokenizer.ggml.tokens", []string{"a", "b", "hello"})
	g.u32("qwen2.block_count", 28)
	g.u32("qwen2.context_length", 32768)
	g.u32("qwen2.embedding_length", 3584)
	g.u32("qwen2.attention.head_count", 28)
	g.u32("qwen2.attention.head_count_kv", 4)
	s, err := readGGUFShape(bytes.NewReader(g.bytes()))
	if err != nil {
		t.Fatal(err)
	}
	// 28 layers * 4 heads * (128+128) * 2 bytes = 57344 bytes per token
	if s.FullBytesPerToken != 57344 || s.TrainCtx != 32768 || s.Layers != 28 {
		t.Fatalf("%+v", s)
	}
	if got := s.kvBytes(32768); got != 57344*32768 {
		t.Fatalf("kv %d", got)
	}
}

func TestGGUFShapeHybridAndSWA(t *testing.T) {
	// hybrid: only every 4th layer has attention
	g := &ggufBuilder{}
	g.str("general.architecture", "qwen3next")
	g.u32("qwen3next.block_count", 8)
	g.u32("qwen3next.embedding_length", 2048)
	g.u32("qwen3next.attention.head_count", 16)
	g.u32("qwen3next.attention.head_count_kv", 2)
	g.u32("qwen3next.attention.key_length", 256)
	g.u32("qwen3next.attention.value_length", 256)
	g.u32("qwen3next.full_attention_interval", 4)
	s, err := readGGUFShape(bytes.NewReader(g.bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if s.FullBytesPerToken != 2*2*512*2 { // 2 attention layers * 2 heads * 512 * 2 bytes
		t.Fatalf("hybrid %+v", s)
	}

	// per-layer KV head counts with zeros for recurrent layers
	g = &ggufBuilder{}
	g.str("general.architecture", "lfm2")
	g.u32("lfm2.block_count", 4)
	g.u32("lfm2.embedding_length", 1024)
	g.u32("lfm2.attention.head_count", 16)
	g.arrU32("lfm2.attention.head_count_kv", []uint32{0, 8, 0, 8})
	s, err = readGGUFShape(bytes.NewReader(g.bytes()))
	if err != nil || s.FullBytesPerToken != 2*8*128*2 {
		t.Fatalf("per-layer %+v %v", s, err)
	}

	// sliding window: 3 of 4 layers keep only 1024 tokens
	g = &ggufBuilder{}
	g.str("general.architecture", "gemma3")
	g.u32("gemma3.block_count", 4)
	g.u32("gemma3.embedding_length", 1024)
	g.u32("gemma3.attention.head_count", 8)
	g.u32("gemma3.attention.head_count_kv", 4)
	g.u32("gemma3.attention.key_length", 128)
	g.u32("gemma3.attention.sliding_window", 1024)
	g.arrBool("gemma3.attention.sliding_window_pattern", []bool{true, true, true, false})
	s, err = readGGUFShape(bytes.NewReader(g.bytes()))
	if err != nil {
		t.Fatal(err)
	}
	per := int64(4 * 256 * 2)
	if s.FullBytesPerToken != per || s.SWABytesPerToken != 3*per || s.kvBytes(8192) != per*8192+3*per*1024 {
		t.Fatalf("swa %+v", s)
	}
}

func TestGGUFRejectsOtherFiles(t *testing.T) {
	if _, err := readGGUFShape(bytes.NewReader([]byte("PK\x03\x04 not a model"))); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := readGGUFShape(bytes.NewReader([]byte("GGUF\x03\x00"))); err == nil {
		t.Fatal("expected an error for a truncated header")
	}
}
