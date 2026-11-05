package main

// Request and answer between the controller and one node, over the node's
// existing WebSocket: the controller sends "node.ask" and waits for the
// matching "node.answer". Used for things the web UI reads from a node on
// demand (its log tail). Nothing is stored on either side.
//
// A node older than this feature ignores "node.ask", so the controller's
// wait times out and the UI says the node needs updating.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type AskRequest struct {
	Req  string          `json:"req"`
	Kind string          `json:"kind"` // "log.tail"
	Args json.RawMessage `json:"args,omitempty"`
}

type AskAnswer struct {
	Req   string          `json:"req"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// LogTailArgs asks for the end of one of the node's logs.
type LogTailArgs struct {
	Source string `json:"source"` // node | tray | llama
	Lines  int    `json:"lines"`
}

type LogTail struct {
	Source string `json:"source"`
	Text   string `json:"text"`
	Note   string `json:"note,omitempty"` // e.g. "no tray on this node"
}

var errOldNode = errors.New("the node did not answer; it may be running an older Outpost (update it to use this)")

// ---- controller side ----

type askWaiters struct {
	mu sync.Mutex
	m  map[string]chan AskAnswer
}

func (c *Controller) ask(nodeID, kind string, args any, timeout time.Duration, out any) error {
	c.mu.Lock()
	nc := c.conns[nodeID]
	c.mu.Unlock()
	if nc == nil {
		return errors.New("node is offline")
	}
	a, _ := json.Marshal(args)
	req := AskRequest{Req: newID("ask"), Kind: kind, Args: a}
	ch := make(chan AskAnswer, 1)
	c.asks.mu.Lock()
	if c.asks.m == nil {
		c.asks.m = map[string]chan AskAnswer{}
	}
	c.asks.m[req.Req] = ch
	c.asks.mu.Unlock()
	defer func() {
		c.asks.mu.Lock()
		delete(c.asks.m, req.Req)
		c.asks.mu.Unlock()
	}()
	if err := nc.send("node.ask", req); err != nil {
		return err
	}
	select {
	case ans := <-ch:
		if ans.Error != "" {
			return errors.New(ans.Error)
		}
		if out != nil {
			return json.Unmarshal(ans.Data, out)
		}
		return nil
	case <-time.After(timeout):
		return errOldNode
	}
}

// deliverAnswer is called from handleNodeMessage.
func (c *Controller) deliverAnswer(body json.RawMessage) {
	var a AskAnswer
	if json.Unmarshal(body, &a) != nil {
		return
	}
	c.asks.mu.Lock()
	ch := c.asks.m[a.Req]
	c.asks.mu.Unlock()
	if ch != nil {
		select {
		case ch <- a:
		default:
		}
	}
}

// ---- node side ----

// nodeLog keeps the end of this process's own log in memory, so the web UI
// can show it on every platform (on Linux the log otherwise only goes to
// journald). Installed by runNode.
var nodeLog = &ringLog{max: 512 << 10}

type ringLog struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (r *ringLog) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		cut := len(r.buf) - r.max
		if i := strings.IndexByte(string(r.buf[cut:]), '\n'); i >= 0 {
			cut += i + 1
		}
		r.buf = append([]byte(nil), r.buf[cut:]...)
	}
	return len(p), nil
}

func (r *ringLog) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

func (n *Node) answerAsk(req AskRequest) {
	ans := AskAnswer{Req: req.Req}
	var data any
	var err error
	switch req.Kind {
	case "log.tail":
		var a LogTailArgs
		_ = json.Unmarshal(req.Args, &a)
		data, err = n.logTail(a)
	default:
		err = fmt.Errorf("this node does not know %q", req.Kind)
	}
	if err != nil {
		ans.Error = err.Error()
	} else {
		ans.Data, _ = json.Marshal(data)
	}
	if e := n.send("node.answer", ans); e != nil {
		log.Printf("answering %s: %v", req.Kind, e)
	}
}

func (n *Node) logTail(a LogTailArgs) (*LogTail, error) {
	lines := a.Lines
	if lines <= 0 || lines > 5000 {
		lines = 500
	}
	out := &LogTail{Source: a.Source}
	switch a.Source {
	case "", "node":
		out.Source = "node"
		out.Text = tailLines(nodeLog.String(), lines)
	case "tray":
		b, err := readTail(filepath.Join(n.dataDir, "tray.log"), 256<<10)
		if err != nil {
			out.Note = "No tray log on this node (no tray icon has run here)."
		}
		out.Text = tailLines(string(b), lines)
	case "llama":
		n.llama.mu.Lock()
		t := n.llama.logTail
		n.llama.mu.Unlock()
		if t == nil {
			out.Note = "llama-server has not run since this node started."
		} else {
			out.Text = tailLines(t.String(), lines)
		}
	default:
		return nil, fmt.Errorf("unknown log %q", a.Source)
	}
	if out.Text == "" && out.Note == "" {
		out.Note = "Nothing logged yet."
	}
	return out, nil
}

// readTail reads at most max bytes from the end of a file.
func readTail(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off := int64(0)
	if st.Size() > max {
		off = st.Size() - max
	}
	b := make([]byte, st.Size()-off)
	_, err = f.ReadAt(b, off)
	return b, err
}

// tailLines returns the last k lines of s, kept as lines.
func tailLines(s string, k int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > k {
		lines = lines[len(lines)-k:]
	}
	return strings.Join(lines, "\n")
}
