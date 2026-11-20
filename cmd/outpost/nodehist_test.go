package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNodeHistAveragesAndKeepsAnHour(t *testing.T) {
	var h nodeHist
	t0 := time.Unix(1_800_000_000, 0).Truncate(histStep)
	h.add(t0, histPoint{CPU: 10, RAM: 50, GPU: -1, VRAM: -1, Temp: -1, TPS: -1})
	h.add(t0.Add(10*time.Second), histPoint{CPU: 30, RAM: 50, GPU: 80, VRAM: 40, Temp: 61, TPS: 12})
	if len(h.pts) != 1 {
		t.Fatalf("one 30 s step, got %d points", len(h.pts))
	}
	p := h.pts[0]
	if p.CPU != 20 || p.GPU != 80 || p.Temp != 61 || p.TPS != 12 {
		t.Fatalf("merged %+v", p)
	}
	for i := 1; i <= 150; i++ {
		h.add(t0.Add(time.Duration(i)*histStep), histPoint{CPU: float32(i), GPU: -1, VRAM: -1, Temp: -1, TPS: -1})
	}
	if len(h.pts) != histPoints || h.pts[0].T != t0.Add(31*histStep).Unix() {
		t.Fatalf("kept %d points from %d", len(h.pts), h.pts[0].T-t0.Unix())
	}
}

func TestHistSample(t *testing.T) {
	hello := &Hello{GPUs: []GPUInfo{{VRAMBytes: 8 << 30}, {VRAMBytes: 8 << 30}}}
	hb := &Heartbeat{CPUPct: 42, RAMUsedBytes: 4, RAMTotalBytes: 16, GPUUtilPct: []int{10, 90}, VRAMUsedBytes: []uint64{4 << 30, 4 << 30}, GPUTempC: []int{0, 70}, LoadedModel: "m"}
	p := histSample(hello, hb, &NodeBench{Model: "m", TokPerSec: 9.5})
	if p.CPU != 42 || p.RAM != 25 || p.GPU != 90 || p.VRAM != 50 || p.Temp != 70 || p.TPS != 9.5 {
		t.Fatalf("got %+v", p)
	}
	p = histSample(&Hello{}, &Heartbeat{CPUPct: 5}, &NodeBench{Model: "other", TokPerSec: 3})
	if p.GPU != -1 || p.VRAM != -1 || p.Temp != -1 || p.TPS != -1 {
		t.Fatalf("no GPU, no model: %+v", p)
	}
}

func TestNodeHistoryHandler(t *testing.T) {
	c := &Controller{nodes: map[string]*NodeRec{"a": {ID: "a"}}, lastHB: map[string]*Heartbeat{}}
	c.mu.Lock()
	c.recordHistLocked("a", &Heartbeat{CPUPct: 12}, time.Now())
	c.recordHistLocked("gone", &Heartbeat{CPUPct: 1}, time.Now())
	c.mu.Unlock()
	rr := httptest.NewRecorder()
	c.handleNodeHistory(rr, httptest.NewRequest("GET", "/api/node-history", nil))
	var v struct {
		Step  int                    `json:"step"`
		Nodes map[string][]histPoint `json:"nodes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Step != 30 || len(v.Nodes["a"]) != 1 || v.Nodes["a"][0].CPU != 12 || v.Nodes["gone"] != nil {
		t.Fatalf("got %+v", v)
	}
}

func TestGPUTempFromHwmon(t *testing.T) {
	dev := t.TempDir()
	if gpuTempC(dev) != 0 {
		t.Fatal("no sensor should read 0")
	}
	p := filepath.Join(dev, "hwmon", "hwmon3")
	os.MkdirAll(p, 0o755)
	os.WriteFile(filepath.Join(p, "temp1_input"), []byte("64000\n"), 0o644)
	if got := gpuTempC(dev); got != 64 {
		t.Fatalf("got %d", got)
	}
}
