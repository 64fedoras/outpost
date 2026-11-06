package gguf

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
	tb bytes.Buffer // tensor table
	nt int
}

// tensor adds a tensor table entry of the given ggml type with elems elements.
func (g *ggufBuilder) tensor(name string, typ uint32, elems uint64) {
	binary.Write(&g.tb, binary.LittleEndian, uint64(len(name)))
	g.tb.WriteString(name)
	binary.Write(&g.tb, binary.LittleEndian, uint32(1))
	binary.Write(&g.tb, binary.LittleEndian, elems)
	binary.Write(&g.tb, binary.LittleEndian, typ)
	binary.Write(&g.tb, binary.LittleEndian, uint64(0))
	g.nt++
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
	binary.Write(&g.b, binary.LittleEndian, uint64(g.nt))
	binary.Write(&g.b, binary.LittleEndian, uint64(g.n))
	g.b.Write(g.kv.Bytes())
	g.b.Write(g.tb.Bytes())
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
	s, err := ReadShape(bytes.NewReader(g.bytes()))
	if err != nil {
		t.Fatal(err)
	}
	// 28 layers * 4 heads * (128+128) * 2 bytes = 57344 bytes per token
	if s.FullBytesPerToken != 57344 || s.TrainCtx != 32768 || s.Layers != 28 {
		t.Fatalf("%+v", s)
	}
	if got := s.KVBytes(32768); got != 57344*32768 {
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
	s, err := ReadShape(bytes.NewReader(g.bytes()))
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
	s, err = ReadShape(bytes.NewReader(g.bytes()))
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
	s, err = ReadShape(bytes.NewReader(g.bytes()))
	if err != nil {
		t.Fatal(err)
	}
	per := int64(4 * 256 * 2)
	if s.FullBytesPerToken != per || s.SWABytesPerToken != 3*per || s.KVBytes(8192) != per*8192+3*per*1024 {
		t.Fatalf("swa %+v", s)
	}
}

func TestGGUFRejectsOtherFiles(t *testing.T) {
	if _, err := ReadShape(bytes.NewReader([]byte("PK\x03\x04 not a model"))); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := ReadShape(bytes.NewReader([]byte("GGUF\x03\x00"))); err == nil {
		t.Fatal("expected an error for a truncated header")
	}
}

func TestGGUFActiveFraction(t *testing.T) {
	g := &ggufBuilder{}
	g.str("general.architecture", "qwen3moe")
	g.u32("qwen3moe.block_count", 2)
	g.u32("qwen3moe.embedding_length", 256)
	g.u32("qwen3moe.attention.head_count", 4)
	g.u32("qwen3moe.attention.head_count_kv", 2)
	g.u32("qwen3moe.expert_count", 128)
	g.u32("qwen3moe.expert_used_count", 8)
	// 256-element Q4_K blocks are 144 bytes: 256*1000 elements = 144000 bytes.
	g.tensor("token_embd.weight", 12, 256*1000) // lookup only: not counted (output.weight exists)
	g.tensor("output.weight", 12, 256*1000)     // 144000 read
	g.tensor("blk.0.attn_q.weight", 12, 256*1000)
	g.tensor("blk.0.ffn_down_exps.weight", 12, 256*16000) // 2304000 bytes, 8/128 read = 144000
	s, err := ReadShape(bytes.NewReader(g.bytes()))
	if err != nil {
		t.Fatal(err)
	}
	// total = 4*144000 + 2304000-144000... compute: tensors 144000*3 + 2304000 = 2736000; active = 144000*2 + 144000 = 432000
	want := 432000.0 / 2736000.0
	if s.ActiveFrac < want-1e-9 || s.ActiveFrac > want+1e-9 {
		t.Errorf("ActiveFrac = %v, want %v", s.ActiveFrac, want)
	}
	if got := s.ActiveBytes(1000); got != 157 {
		t.Errorf("ActiveBytes(1000) = %d", got)
	}
	if s.ExpertBytes != 2304000 {
		t.Errorf("ExpertBytes = %d", s.ExpertBytes)
	}
	if s.NeedsHeaderRefresh() || !(&Shape{ActiveFrac: 0.2}).NeedsHeaderRefresh() {
		t.Error("NeedsHeaderRefresh")
	}
	// An unknown tensor type leaves the fraction unknown but keeps the KV shape.
	g2 := &ggufBuilder{}
	g2.str("general.architecture", "qwen3moe")
	g2.u32("qwen3moe.block_count", 2)
	g2.u32("qwen3moe.embedding_length", 256)
	g2.u32("qwen3moe.attention.head_count", 4)
	g2.tensor("blk.0.x.weight", 9999, 10)
	s2, err := ReadShape(bytes.NewReader(g2.bytes()))
	if err != nil || s2.ActiveFrac != 0 || s2.FullBytesPerToken == 0 {
		t.Errorf("unknown type: %+v %v", s2, err)
	}
}

func TestExpertBytesOf(t *testing.T) {
	s := &Shape{Layers: 40, ExpertBytes: 4000}
	for mode, want := range map[string]int64{"": 0, "all": 4000, "10": 1000, "40": 4000, "99": 4000, "0": 0, "x": 0} {
		if got := s.ExpertBytesOf(mode); got != want {
			t.Errorf("ExpertBytesOf(%q) = %d, want %d", mode, got, want)
		}
	}
	if (&Shape{Layers: 4}).ExpertBytesOf("all") != 0 || (*Shape)(nil).ExpertBytesOf("all") != 0 {
		t.Error("dense or unknown model moves nothing")
	}
}
