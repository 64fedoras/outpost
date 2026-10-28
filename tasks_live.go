package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Live output: while a model call runs for a task, its text is kept in memory
// (last liveBufBytes per call) and pushed to browsers over server-sent events.
// Nothing here is written to disk.

const liveBufBytes = 64 << 10

// liveEvent is one SSE message. T is "start", "delta", "reset", "end" or "changed".
type liveEvent struct {
	T     string  `json:"t"`
	Call  string  `json:"call,omitempty"`
	Sub   string  `json:"sub,omitempty"`
	Label string  `json:"label,omitempty"`
	Node  string  `json:"node,omitempty"`
	Text  string  `json:"text,omitempty"`
	Tok   int     `json:"tok,omitempty"`
	TPS   float64 `json:"tps,omitempty"`
	Est   bool    `json:"est,omitempty"` // token count is an estimate (cloud planners)
}

type liveCall struct {
	id, sub, label, node string
	buf                  []byte
	tok                  int
	est                  bool
	first                time.Time
}

func (lc *liveCall) tps() float64 {
	if lc.first.IsZero() || lc.tok < 2 {
		return 0
	}
	d := time.Since(lc.first).Seconds()
	if d < 0.2 {
		return 0
	}
	return float64(int(10*float64(lc.tok)/d)) / 10
}

type liveTask struct {
	calls map[string]*liveCall
	order []string
	subs  map[chan liveEvent]struct{}
}

type liveHub struct {
	mu    sync.Mutex
	tasks map[string]*liveTask
	jobs  map[string][2]string // job id -> task id, call id
}

func newLiveHub() *liveHub {
	return &liveHub{tasks: map[string]*liveTask{}, jobs: map[string][2]string{}}
}

// taskLive is the process-wide hub (one controller per process).
var taskLive = newLiveHub()

func (h *liveHub) taskLocked(id string) *liveTask {
	lt := h.tasks[id]
	if lt == nil {
		lt = &liveTask{calls: map[string]*liveCall{}, subs: map[chan liveEvent]struct{}{}}
		h.tasks[id] = lt
	}
	return lt
}

func (h *liveHub) gcLocked(id string) {
	if lt := h.tasks[id]; lt != nil && len(lt.calls) == 0 && len(lt.subs) == 0 {
		delete(h.tasks, id)
	}
}

// broadcastLocked sends to every subscriber. A subscriber that cannot keep up
// is dropped (its stream ends and the browser reconnects to a fresh snapshot).
// Droppable events (changed) are skipped instead.
func (h *liveHub) broadcastLocked(lt *liveTask, ev liveEvent) {
	for ch := range lt.subs {
		select {
		case ch <- ev:
		default:
			if ev.T != "changed" {
				delete(lt.subs, ch)
				close(ch)
			}
		}
	}
}

// liveHandle is what a model call writes its streamed text to. A nil handle ignores everything.
type liveHandle struct {
	h            *liveHub
	task, callID string
}

func (h *liveHub) begin(taskID, sub, label, node string, est bool) *liveHandle {
	h.mu.Lock()
	defer h.mu.Unlock()
	lt := h.taskLocked(taskID)
	lc := &liveCall{id: newID("call"), sub: sub, label: label, node: node, est: est}
	lt.calls[lc.id] = lc
	lt.order = append(lt.order, lc.id)
	h.broadcastLocked(lt, liveEvent{T: "start", Call: lc.id, Sub: sub, Label: label, Node: node, Est: est})
	return &liveHandle{h: h, task: taskID, callID: lc.id}
}

func (lh *liveHandle) with(fn func(lt *liveTask, lc *liveCall)) {
	if lh == nil {
		return
	}
	lh.h.mu.Lock()
	defer lh.h.mu.Unlock()
	if lt := lh.h.tasks[lh.task]; lt != nil {
		if lc := lt.calls[lh.callID]; lc != nil {
			fn(lt, lc)
		}
	}
}

func (h *liveHub) addText(lt *liveTask, lc *liveCall, text string, tokens int) {
	if lc.first.IsZero() {
		lc.first = time.Now()
	}
	if tokens > 0 {
		lc.tok += tokens
	} else if lc.est {
		lc.tok += (len(text) + 3) / 4
	}
	lc.buf = append(lc.buf, text...)
	if len(lc.buf) > liveBufBytes {
		lc.buf = append([]byte(nil), lc.buf[len(lc.buf)-liveBufBytes:]...)
	}
	h.broadcastLocked(lt, liveEvent{T: "delta", Call: lc.id, Text: text, Tok: lc.tok, TPS: lc.tps()})
}

// Write appends streamed text (cloud planners; tokens are estimated).
func (lh *liveHandle) Write(text string) {
	lh.with(func(lt *liveTask, lc *liveCall) { lh.h.addText(lt, lc, text, 0) })
}

// Reset clears the text, e.g. when a failed request is retried.
func (lh *liveHandle) Reset() {
	lh.with(func(lt *liveTask, lc *liveCall) {
		lc.buf, lc.tok, lc.first = nil, 0, time.Time{}
		lh.h.broadcastLocked(lt, liveEvent{T: "reset", Call: lc.id})
	})
}

// End removes the call; its text is not kept.
func (lh *liveHandle) End() {
	if lh == nil {
		return
	}
	h := lh.h
	h.mu.Lock()
	defer h.mu.Unlock()
	lt := h.tasks[lh.task]
	if lt == nil {
		return
	}
	delete(lt.calls, lh.callID)
	for i, id := range lt.order {
		if id == lh.callID {
			lt.order = append(lt.order[:i], lt.order[i+1:]...)
			break
		}
	}
	for j, ref := range h.jobs {
		if ref[1] == lh.callID {
			delete(h.jobs, j)
		}
	}
	h.broadcastLocked(lt, liveEvent{T: "end", Call: lh.callID})
	h.gcLocked(lh.task)
}

// bindJob routes a node job's output to this call.
func (lh *liveHandle) bindJob(jobID string) {
	if lh == nil {
		return
	}
	lh.h.mu.Lock()
	lh.h.jobs[jobID] = [2]string{lh.task, lh.callID}
	lh.h.mu.Unlock()
}

// jobOutput feeds a node's job.output message to the call bound to that job.
func (h *liveHub) jobOutput(o JobOutput) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ref, ok := h.jobs[o.JobID]
	if !ok {
		return
	}
	lt := h.tasks[ref[0]]
	if lt == nil || lt.calls[ref[1]] == nil {
		return
	}
	h.addText(lt, lt.calls[ref[1]], o.Data, o.Tokens)
}

// changed tells browsers that the task's state changed and should be refetched.
func (h *liveHub) changed(taskID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if lt := h.tasks[taskID]; lt != nil {
		h.broadcastLocked(lt, liveEvent{T: "changed"})
	}
}

// subscribe returns the running calls as start events (with their text so far)
// and a channel of later events.
func (h *liveHub) subscribe(taskID string) ([]liveEvent, chan liveEvent, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	lt := h.taskLocked(taskID)
	var snap []liveEvent
	for _, id := range lt.order {
		lc := lt.calls[id]
		snap = append(snap, liveEvent{T: "start", Call: lc.id, Sub: lc.sub, Label: lc.label, Node: lc.node,
			Text: string(lc.buf), Tok: lc.tok, TPS: lc.tps(), Est: lc.est})
	}
	ch := make(chan liveEvent, 512)
	lt.subs[ch] = struct{}{}
	return snap, ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if lt := h.tasks[taskID]; lt != nil {
			if _, ok := lt.subs[ch]; ok {
				delete(lt.subs, ch)
				close(ch)
			}
			h.gcLocked(taskID)
		}
	}
}

// handleTaskEvents streams a task's live output and change notices (SSE).
func (c *Controller) handleTaskEvents(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, 500, map[string]string{"error": "streaming not supported"})
		return
	}
	snap, ch, cancel := taskLive.subscribe(t.ID)
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(ev liveEvent) {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	fmt.Fprint(w, "retry: 2000\n\n")
	for _, ev := range snap {
		send(ev)
	}
	send(liveEvent{T: "changed"})
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			send(ev)
			// batch whatever else is already queued into one flush
			for n := len(ch); n > 0; n-- {
				if ev, ok := <-ch; ok {
					send(ev)
				} else {
					fl.Flush()
					return
				}
			}
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}
