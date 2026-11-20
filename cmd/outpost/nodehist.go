package main

// Node history for the sparklines on the Nodes page: the last hour of each
// node's CPU, RAM, GPU, VRAM, GPU temperature and measured speed, kept in
// controller memory only (nothing on disk, so a controller restart starts the
// charts again). Heartbeats arrive every 10 s; they are averaged into 30 s
// points, 120 per node.

import (
	"math"
	"net/http"
	"time"
)

const (
	histStep   = 30 * time.Second
	histPoints = 120 // one hour
)

// histPoint is one 30 s average. A value of -1 means "not reported" (no GPU, no
// temperature sensor, no model loaded), so the chart leaves a gap.
type histPoint struct {
	T    int64   `json:"t"` // unix seconds at the start of the 30 s step
	CPU  float32 `json:"cpu"`
	RAM  float32 `json:"ram"`
	GPU  float32 `json:"gpu"`
	VRAM float32 `json:"vram"`
	Temp float32 `json:"temp"` // hottest GPU, °C
	TPS  float32 `json:"tps"`  // measured speed of the loaded model
	n    int
}

type nodeHist struct {
	pts []histPoint
}

// histSample turns one heartbeat into the values of a point.
func histSample(h *Hello, hb *Heartbeat, bench *NodeBench) histPoint {
	p := histPoint{CPU: float32(hb.CPUPct), GPU: -1, VRAM: -1, Temp: -1, TPS: -1}
	if hb.RAMTotalBytes > 0 {
		p.RAM = float32(100 * float64(hb.RAMUsedBytes) / float64(hb.RAMTotalBytes))
	}
	if len(hb.GPUUtilPct) > 0 {
		for _, g := range hb.GPUUtilPct {
			p.GPU = max(p.GPU, float32(g))
		}
	}
	if h != nil && len(h.GPUs) > 0 && len(hb.VRAMUsedBytes) > 0 {
		var used, total uint64
		for i, g := range h.GPUs {
			if i < len(hb.VRAMUsedBytes) && g.VRAMBytes > 0 {
				used += hb.VRAMUsedBytes[i]
				total += g.VRAMBytes
			}
		}
		if total > 0 {
			p.VRAM = float32(min(100, 100*float64(used)/float64(total)))
		}
	}
	for _, t := range hb.GPUTempC {
		if t > 0 {
			p.Temp = max(p.Temp, float32(t))
		}
	}
	if hb.LoadedModel != "" && bench != nil && bench.Model == hb.LoadedModel && bench.TokPerSec > 0 {
		p.TPS = float32(bench.TokPerSec)
	}
	return p
}

// add merges a sample into the current 30 s point (a running average; a
// "not reported" value stays -1 only if every sample in the step lacked it).
func (h *nodeHist) add(now time.Time, s histPoint) {
	step := now.Truncate(histStep).Unix()
	if n := len(h.pts); n > 0 && h.pts[n-1].T == step {
		p := &h.pts[n-1]
		avg := func(old, v float32) float32 {
			switch {
			case v < 0:
				return old
			case old < 0:
				return v
			}
			return (old*float32(p.n) + v) / float32(p.n+1)
		}
		p.CPU, p.RAM = avg(p.CPU, s.CPU), avg(p.RAM, s.RAM)
		p.GPU, p.VRAM, p.Temp, p.TPS = avg(p.GPU, s.GPU), avg(p.VRAM, s.VRAM), max(p.Temp, s.Temp), avg(p.TPS, s.TPS)
		p.n++
		return
	}
	s.T, s.n = step, 1
	h.pts = append(h.pts, s)
	cut := now.Add(-histPoints * histStep).Unix()
	i := 0
	for i < len(h.pts) && h.pts[i].T <= cut {
		i++
	}
	if i > 0 {
		h.pts = append(h.pts[:0], h.pts[i:]...)
	}
}

// recordHistLocked is called with c.mu held for every heartbeat.
func (c *Controller) recordHistLocked(id string, hb *Heartbeat, now time.Time) {
	if c.hist == nil {
		c.hist = map[string]*nodeHist{}
	}
	h := c.hist[id]
	if h == nil {
		h = &nodeHist{}
		c.hist[id] = h
	}
	var hello *Hello
	var bench *NodeBench
	if n := c.nodes[id]; n != nil {
		hello, bench = n.Hello, n.Bench
	}
	h.add(now, histSample(hello, hb, bench))
}

// handleNodeHistory returns every node's last hour, oldest point first.
func (c *Controller) handleNodeHistory(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	out := map[string][]histPoint{}
	r1 := func(v float32) float32 { return float32(math.Round(float64(v)*10) / 10) } // one decimal is plenty for a chart
	for id, h := range c.hist {
		if _, known := c.nodes[id]; known {
			pts := make([]histPoint, len(h.pts))
			for i, p := range h.pts {
				pts[i] = histPoint{T: p.T, CPU: r1(p.CPU), RAM: r1(p.RAM), GPU: r1(p.GPU), VRAM: r1(p.VRAM), Temp: r1(p.Temp), TPS: r1(p.TPS)}
			}
			out[id] = pts
		}
	}
	c.mu.Unlock()
	writeJSON(w, 200, map[string]any{"step": int(histStep / time.Second), "nodes": out})
}
