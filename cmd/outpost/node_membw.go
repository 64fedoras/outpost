package main

import (
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// The node measures how fast it can read memory, so the controller can
// estimate tokens per second for CPU-only models from the machine itself
// rather than from a table of CPU types. Generating a token on a CPU is bound
// by this number: every token reads the active weights once.

var (
	memBWBits atomic.Uint64 // math.Float64bits of the best measured GB/s
	memBWOnce sync.Once
)

var memBWFixed float64 // test builds only: reported instead of measuring

func memBandwidthGBs() float64 {
	if memBWFixed > 0 {
		return memBWFixed
	}
	return math.Float64frombits(memBWBits.Load())
}

// startMemBandwidth measures once in the background (about a second) and then
// calls done so the node can tell the controller.
func startMemBandwidth(ramBytes uint64, done func()) {
	memBWOnce.Do(func() {
		go func() {
			if bw := measureMemBandwidth(ramBytes); bw > 0 {
				memBWBits.Store(math.Float64bits(bw))
				if done != nil {
					done()
				}
			}
		}()
	})
}

// measureMemBandwidth reads a buffer much bigger than any cache from all CPU
// threads at once and returns the best rate in GB/s, or 0 if it could not run.
func measureMemBandwidth(ramBytes uint64) float64 {
	size := int64(512 << 20)
	if ramBytes > 0 && int64(ramBytes)/6 < size {
		size = int64(ramBytes) / 6
	}
	if size < 64<<20 {
		return 0
	}
	workers := runtime.NumCPU()
	if workers > 64 {
		workers = 64
	}
	words := int(size / 8)
	buf := make([]uint64, words)
	per := words / workers
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ { // touch every page so it is really in RAM
		wg.Add(1)
		go func(part []uint64) {
			defer wg.Done()
			for i := range part {
				part[i] = uint64(i) | 1
			}
		}(buf[w*per : (w+1)*per])
	}
	wg.Wait()
	var sink atomic.Uint64
	best := 0.0
	deadline := time.Now().Add(1500 * time.Millisecond)
	for pass := 0; pass < 8 && time.Now().Before(deadline); pass++ {
		start := time.Now()
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(part []uint64) {
				defer wg.Done()
				var a, b, c, d uint64
				i := 0
				for ; i+4 <= len(part); i += 4 {
					a += part[i]
					b += part[i+1]
					c += part[i+2]
					d += part[i+3]
				}
				sink.Add(a + b + c + d)
			}(buf[w*per : (w+1)*per])
		}
		wg.Wait()
		if gbs := float64(per*workers*8) / time.Since(start).Seconds() / 1e9; gbs > best {
			best = gbs
		}
	}
	runtime.KeepAlive(buf)
	return best
}
