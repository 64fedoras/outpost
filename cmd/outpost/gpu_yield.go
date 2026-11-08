package main

// Yield the GPU to other programs (a game on the same desktop): when the node
// is not answering anything and the GPU stays busy for yieldHotFor, the model
// is unloaded to free VRAM, as an idle unload does. The node reports itself as
// not ready meanwhile, so the controller sends it no work. When the GPU has
// been quiet for yieldQuietFor the model loads again by itself. Off by default
// (a per-node switch on the Nodes page). Utilisation comes from the heartbeat
// sample, so it adds no polling. It cannot tell a game from any other program,
// and image generation on the same node counts as "another program".

import (
	"log"
	"time"
)

const (
	yieldHotPct   = 35
	yieldQuietPct = 10
	yieldHotFor   = 30 * time.Second
	yieldQuietFor = 5 * time.Minute
)

type yieldAction int

const (
	yieldNone yieldAction = iota
	yieldUnload
	yieldReload
)

type gpuYield struct {
	on       bool
	yielding bool
	since    time.Time // start of the current hot (before yielding) or quiet (while yielding) stretch
}

// step takes the busiest GPU's utilisation and says what to do. ours is true
// while the node's own work could be using the GPU; loaded says a model is loaded.
func (y *gpuYield) step(pct int, ours, loaded bool, now time.Time) yieldAction {
	if !y.on {
		was := y.yielding
		y.yielding, y.since = false, time.Time{}
		if was {
			return yieldReload
		}
		return yieldNone
	}
	if y.yielding {
		if pct >= yieldQuietPct {
			y.since = time.Time{}
			return yieldNone
		}
		if y.since.IsZero() {
			y.since = now
		}
		if now.Sub(y.since) >= yieldQuietFor {
			y.yielding, y.since = false, time.Time{}
			return yieldReload
		}
		return yieldNone
	}
	if ours || !loaded || pct < yieldHotPct {
		y.since = time.Time{}
		return yieldNone
	}
	if y.since.IsZero() {
		y.since = now
	}
	if now.Sub(y.since) >= yieldHotFor {
		y.yielding, y.since = true, time.Time{}
		return yieldUnload
	}
	return yieldNone
}

func (l *llamaMgr) setYield(on bool) {
	l.yieldMu.Lock()
	l.yield.on = on
	l.yieldMu.Unlock()
}

func (l *llamaMgr) isYielding() bool {
	l.yieldMu.Lock()
	defer l.yieldMu.Unlock()
	return l.yield.yielding
}

// yieldObserve is called with each heartbeat's GPU utilisation.
func (l *llamaMgr) yieldObserve(util []int, now time.Time) {
	pct := 0
	for _, u := range util {
		if u > pct {
			pct = u
		}
	}
	l.mu.Lock()
	ours := l.busy > 0 || now.Sub(l.lastUsed) < 20*time.Second
	loaded := l.cmd != nil && l.ready
	l.mu.Unlock()
	l.yieldMu.Lock()
	act := l.yield.step(pct, ours, loaded, now)
	l.yieldMu.Unlock()
	switch act {
	case yieldUnload:
		if _, err := l.unloadNow("another program is using the GPU"); err != nil {
			l.yieldMu.Lock()
			l.yield.yielding = false // could not unload now; look again
			l.yieldMu.Unlock()
			log.Printf("gpu yield: %v", err)
		}
	case yieldReload:
		go func() {
			log.Printf("gpu yield: the GPU is free again; loading the model")
			if e := l.wake(""); e != nil {
				log.Printf("gpu yield: reload failed: %v", e.Message)
			}
		}()
	}
}
