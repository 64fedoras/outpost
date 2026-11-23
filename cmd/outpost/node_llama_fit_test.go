package main

import (
	"testing"

	"outpost/internal/gguf"
)

func TestFitKV(t *testing.T) {
	shape := &gguf.Shape{FullBytesPerToken: 98 << 10} // about 98 KB per token, as qwen3-coder-30b
	const gib = 1 << 30
	ml := ModelLoad{Ctx: 262144}
	if no, c := fitKV(shape, ml, 8*gib, 121*gib); !no || c != 0 {
		t.Errorf("256k on an 8 GiB card with 121 GiB RAM: want KV in RAM, got %v %d", no, c)
	}
	ml.Ctx = 16384
	if no, c := fitKV(shape, ml, 8*gib, 121*gib); no || c != 0 {
		t.Errorf("16k fits the card: want no change, got %v %d", no, c)
	}
	ml.Ctx = 262144
	if no, c := fitKV(shape, ml, 8*gib, 16*gib); !no || c == 0 || c >= 262144 {
		t.Errorf("256k with 16 GiB RAM: want a lower context in RAM, got %v %d", no, c)
	}
	if no, c := fitKV(shape, ml, 0, 121*gib); no || c != 0 {
		t.Error("no GPU: nothing to fit")
	}
	ml.GPULayers = "0"
	if no, c := fitKV(shape, ml, 8*gib, 121*gib); no || c != 0 {
		t.Error("gpu layers 0: nothing to fit")
	}
	ml.GPULayers = ""
	ml.ExtraArgs = []string{"--no-kv-offload"}
	if no, _ := fitKV(shape, ml, 8*gib, 121*gib); no {
		t.Error("already chosen by the user")
	}
}
