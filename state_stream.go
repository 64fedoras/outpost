package main

// Pushed cluster state (idea 36). Instead of each browser tab asking for
// /api/state, the task list and the projects every 2 seconds, a tab opens
// one server-sent event stream. One goroutine builds the payload once a
// second while anyone is listening and sends it only when it changed, so
// several tabs cost one build, and an idle cluster sends almost nothing.
// The browser falls back to polling when the stream fails (for example
// behind a proxy that buffers it).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

var statePushEvery = time.Second

type stateHub struct {
	mu   sync.Mutex
	subs map[chan []byte]bool
	last []byte
	on   bool
}

func (c *Controller) statePayload() []byte {
	b, _ := json.Marshal(map[string]any{"state": c.stateView(), "tasks": c.taskSummaries(), "projects": c.projectViews()})
	return b
}

// subscribe returns a channel of payloads, starting with the latest one.
func (h *stateHub) subscribe(c *Controller) (chan []byte, func()) {
	ch := make(chan []byte, 2)
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[chan []byte]bool{}
	}
	h.subs[ch] = true
	start := !h.on
	h.on = true
	last := h.last
	h.mu.Unlock()
	if start || last == nil {
		last = c.statePayload()
		h.mu.Lock()
		h.last = last
		h.mu.Unlock()
	}
	ch <- last
	if start {
		go h.run(c, statePushEvery)
	}
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

func (h *stateHub) run(c *Controller, every time.Duration) {
	for {
		time.Sleep(every)
		h.mu.Lock()
		if len(h.subs) == 0 {
			h.on, h.last = false, nil
			h.mu.Unlock()
			return
		}
		h.mu.Unlock()
		b := c.statePayload()
		h.mu.Lock()
		if !bytes.Equal(b, h.last) {
			h.last = b
			for ch := range h.subs {
				select {
				case ch <- b:
				default: // a slow tab gets the next one
				}
			}
		}
		h.mu.Unlock()
	}
}

func (c *Controller) handleStateEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, 500, map[string]string{"error": "streaming not supported"})
		return
	}
	ch, cancel := c.stateHub.subscribe(c)
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 3000\n\n")
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-ch:
			fmt.Fprintf(w, "event: state\ndata: %s\n\n", b)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}
