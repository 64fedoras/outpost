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

// A payload holds the cluster state and the lists the sidebar and pages
// show: tasks, projects, task groups and chats. Node heartbeats change the
// state every few seconds, but the lists (the task list is the biggest part)
// change far less often, so each tab is sent a list only when it differs
// from what that tab last received. The browser keeps its copy of a list
// that is left out.

type stateHub struct {
	mu   sync.Mutex
	subs map[chan []byte]stateSent
	last *stateParts
	on   bool
}

// stateLists are the parts sent only when they changed, in payload order.
var stateLists = []string{"tasks", "projects", "groups", "chats"}

// stateParts are the encoded parts of one payload.
type stateParts struct {
	state      []byte
	lists      map[string][]byte // by name in stateLists
	notifyLast int               // newest browser notification; the tab fetches them only when it moves
}

// stateSent is what one tab last received of each list.
type stateSent map[string][]byte

func (c *Controller) stateParts() *stateParts {
	p := &stateParts{lists: map[string][]byte{}}
	p.state, _ = json.Marshal(c.stateView())
	p.lists["tasks"], _ = json.Marshal(c.taskSummaries())
	p.lists["projects"], _ = json.Marshal(c.projectViews())
	p.lists["groups"], _ = json.Marshal(c.groupList())
	p.lists["chats"], _ = json.Marshal(c.chatSummaries())
	c.nt.mu.Lock()
	p.notifyLast = c.nt.seq
	c.nt.mu.Unlock()
	return p
}

func (p *stateParts) equal(o *stateParts) bool {
	if o == nil || p.notifyLast != o.notifyLast || !bytes.Equal(p.state, o.state) {
		return false
	}
	for _, k := range stateLists {
		if !bytes.Equal(p.lists[k], o.lists[k]) {
			return false
		}
	}
	return true
}

// payload encodes p for a tab that already has sent (nil: a new tab).
func (p *stateParts) payload(sent stateSent) []byte {
	var b bytes.Buffer
	n := len(p.state) + 64
	for _, v := range p.lists {
		n += len(v) + 16
	}
	b.Grow(n)
	b.WriteString(`{"state":`)
	b.Write(p.state)
	for _, k := range stateLists {
		if v, ok := sent[k]; !ok || !bytes.Equal(v, p.lists[k]) {
			fmt.Fprintf(&b, `,%q:`, k)
			b.Write(p.lists[k])
		}
	}
	fmt.Fprintf(&b, `,"notify_last":%d}`, p.notifyLast)
	return b.Bytes()
}

// sentAll records that a tab now has every list in p.
func (p *stateParts) sentAll(sent stateSent) {
	for _, k := range stateLists {
		sent[k] = p.lists[k]
	}
}

// subscribe returns a channel of payloads, starting with a full one.
func (h *stateHub) subscribe(c *Controller) (chan []byte, func()) {
	ch := make(chan []byte, 2)
	h.mu.Lock()
	start := !h.on
	h.on = true
	last := h.last
	h.mu.Unlock()
	if start || last == nil {
		last = c.stateParts()
	}
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[chan []byte]stateSent{}
	}
	if h.last == nil {
		h.last = last
	}
	// The tab starts from what it is sent here; later payloads are diffed against it.
	sent := stateSent{}
	last.sentAll(sent)
	h.subs[ch] = sent
	ch <- last.payload(nil)
	h.mu.Unlock()
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
		p := c.stateParts()
		h.mu.Lock()
		if !p.equal(h.last) {
			h.last = p
			for ch, sent := range h.subs {
				select {
				case ch <- p.payload(sent):
					p.sentAll(sent)
				default: // a slow tab gets the next one, with whatever it missed
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
