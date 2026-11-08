package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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
	// Reason marks a delta that is a thinking model's reasoning, not its answer.
	Reason bool `json:"reason,omitempty"`
	// Prompt reading progress before the first token (local nodes with a
	// llama.cpp that reports it): tokens read, prompt size, seconds left.
	Read  int `json:"read,omitempty"`
	Total int `json:"total,omitempty"`
	Left  int `json:"left,omitempty"`
}

type liveCall struct {
	id, sub, label, node string
	buf                  []byte
	tok                  int
	est                  bool
	first                time.Time
	lastOut              time.Time // last text or start, for stall detection
	read, total, left    int       // prompt progress (see liveEvent)
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
	// last is the most recent finished call per slot (subtask id, or "" for
	// planner and other calls), so a new browser still sees the output of the
	// call that just ended until the next one starts. Bounded: one buffer
	// (liveBufBytes) per slot, in memory only, dropped with the task's hub entry.
	last map[string]*liveCall
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
		lt = &liveTask{calls: map[string]*liveCall{}, subs: map[chan liveEvent]struct{}{}, last: map[string]*liveCall{}}
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
	lc := &liveCall{id: newID("call"), sub: sub, label: label, node: node, est: est, lastOut: time.Now()}
	delete(lt.last, sub)
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
	h.addTextKind(lt, lc, text, tokens, false)
}

func (h *liveHub) addTextKind(lt *liveTask, lc *liveCall, text string, tokens int, reasoning bool) {
	lc.lastOut = time.Now()
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
	h.broadcastLocked(lt, liveEvent{T: "delta", Call: lc.id, Text: text, Tok: lc.tok, TPS: lc.tps(), Reason: reasoning})
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

// End removes the call. Its text is kept as the slot's last output until the
// next call in that slot starts.
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
	if lc := lt.calls[lh.callID]; lc != nil && len(lc.buf) > 0 {
		lt.last[lc.sub] = lc
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
	lc := lt.calls[ref[1]]
	if o.Stream == "progress" {
		if read, total, left, ok := parseProgress(o.Data); ok {
			lc.read, lc.total, lc.left, lc.lastOut = read, total, left, time.Now()
			h.broadcastLocked(lt, liveEvent{T: "progress", Call: lc.id, Read: read, Total: total, Left: left})
		}
		return
	}
	h.addTextKind(lt, lc, o.Data, o.Tokens, o.Stream == "reasoning")
}

// parseProgress reads the last "processed total cached ms" line a node sent
// and estimates the seconds left from the speed of the tokens read so far.
func parseProgress(data string) (read, total, left int, ok bool) {
	lines := strings.Fields(data)
	if len(lines) < 4 {
		return 0, 0, 0, false
	}
	f := lines[len(lines)-4:]
	var v [4]int
	for i, s := range f {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return 0, 0, 0, false
		}
		v[i] = n
	}
	read, total, cached, ms := v[0], v[1], v[2], v[3]
	if total <= 0 {
		return 0, 0, 0, false
	}
	if read > total {
		read = total
	}
	if done := read - cached; done > 0 && ms > 0 {
		left = int(float64(total-read) * float64(ms) / float64(done) / 1000)
	}
	return read, total, left, true
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
			Text: string(lc.buf), Tok: lc.tok, TPS: lc.tps(), Est: lc.est, Read: lc.read, Total: lc.total, Left: lc.left})
	}
	// finished calls whose slot has no running call: start then end, so the
	// browser files them as the slot's last output
	for _, lc := range lt.last {
		snap = append(snap, liveEvent{T: "start", Call: lc.id, Sub: lc.sub, Label: lc.label, Node: lc.node,
			Text: string(lc.buf), Tok: lc.tok, Est: lc.est}, liveEvent{T: "end", Call: lc.id})
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
	serveLive(w, r, fl, t.ID)
}

// serveLive streams one live key (a task id, or "chat_" + a chat id) as SSE.
func serveLive(w http.ResponseWriter, r *http.Request, fl http.Flusher, key string) {
	snap, ch, cancel := taskLive.subscribe(key)
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

// idleFor is how long the task's quietest running model call has gone without
// output; 0 when no call is running.
func (h *liveHub) idleFor(taskID string) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	lt := h.tasks[taskID]
	if lt == nil {
		return 0
	}
	var idle time.Duration
	for _, lc := range lt.calls {
		if d := time.Since(lc.lastOut); d > idle {
			idle = d
		}
	}
	return idle
}

// hasCalls says whether any model call of the task is running.
func (h *liveHub) hasCalls(taskID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	lt := h.tasks[taskID]
	return lt != nil && len(lt.calls) > 0
}
