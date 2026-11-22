package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The start of the trimmed history must stay the same for several turns, or
// the node's cache of the processed prompt is useless once a chat is long.
func TestFitHistoryStableStart(t *testing.T) {
	h := []ChatMessage{{Role: "system", Content: "s"}}
	firsts := map[string]int{}
	for turn := 0; turn < 40; turn++ {
		h = append(h, ChatMessage{Role: "user", Content: strings.Repeat("u", 90) + string(rune('A'+turn%26)) + string(rune('a'+turn/26))})
		got := fitHistory(h, 1000)
		if got[0].Role != "system" || got[1].Role != "user" || got[len(got)-1].Content != h[len(h)-1].Content {
			t.Fatalf("turn %d: bad window", turn)
		}
		if size := len(got); size < 3 && turn > 5 {
			t.Fatalf("turn %d: window shrank to %d", turn, size)
		}
		firsts[got[1].Content]++
		h = append(h, ChatMessage{Role: "assistant", Content: strings.Repeat("a", 90)})
	}
	// With the cut on a grid, each start stays for several turns (one per message without it).
	for k, n := range firsts {
		if n == 1 && len(firsts) > 12 {
			t.Fatalf("start %q used once; %d different starts in 40 turns", k[len(k)-2:], len(firsts))
		}
	}
	if len(firsts) > 10 {
		t.Fatalf("%d different starts in 40 turns", len(firsts))
	}
}

func twoNodeChat(t *testing.T) *Controller {
	c := chatController(t)
	c.lastHB["n2"] = &Heartbeat{LoadedModel: "m", InferenceRdy: true}
	c.nodes["n2"] = &NodeRec{ID: "n2", Name: "second", LoadPrefs: &ModelLoad{Name: "m", Ctx: 4096}}
	return c
}

func TestChatAffinity(t *testing.T) {
	old := chatAffinityWait
	chatAffinityWait = 300 * time.Millisecond
	defer func() { chatAffinityWait = old }()
	c := twoNodeChat(t)
	c.conns["n"], c.conns["n2"] = &nodeConn{}, &nodeConn{}
	ch := &Chat{ID: "c1"}
	c.chats["c1"] = ch
	ctx := context.Background()

	// No history of the chat: the usual choice (first by ID when equal).
	id, err := c.acquireChatNode(ctx, "c1", "auto", nil, nil, time.Second)
	if err != nil || id != "n" {
		t.Fatalf("no affinity: %q %v", id, err)
	}
	c.releaseNode(id)

	// A big prompt answered on n2: the follow-up goes back there.
	c.noteChatNode("c1", "n2", "m", 5000)
	id, err = c.acquireChatNode(ctx, "c1", "auto", nil, nil, time.Second)
	if err != nil || id != "n2" {
		t.Fatalf("affinity: %q %v", id, err)
	}
	// It is busy and stays busy: after the short wait, any node will do.
	start := time.Now()
	id2, err := c.acquireChatNode(ctx, "c1", "auto", nil, nil, time.Second)
	if err != nil || id2 != "n" || time.Since(start) < 250*time.Millisecond {
		t.Fatalf("busy preferred node: %q %v after %s", id2, err, time.Since(start))
	}
	c.releaseNode(id2)
	c.releaseNode(id)

	// A short chat gains little: no waiting for a busy node.
	c.noteChatNode("c1", "n2", "m", 100)
	c.tryReserve("n2")
	start = time.Now()
	id, err = c.acquireChatNode(ctx, "c1", "auto", nil, nil, time.Second)
	if err != nil || id != "n" || time.Since(start) > 200*time.Millisecond {
		t.Fatalf("short chat waited: %q %v after %s", id, err, time.Since(start))
	}
	c.releaseNode(id)
	c.releaseNode("n2")

	// The model changed on that node, or the node is gone: any node.
	c.noteChatNode("c1", "n2", "m", 5000)
	c.lastHB["n2"].LoadedModel = "other"
	id, _ = c.acquireChatNode(ctx, "c1", "auto", nil, nil, time.Second)
	if id != "n" {
		t.Fatalf("model changed: %q", id)
	}
	c.releaseNode(id)
	c.lastHB["n2"].LoadedModel = "m"
	delete(c.conns, "n2")
	id, _ = c.acquireChatNode(ctx, "c1", "auto", nil, nil, time.Second)
	if id != "n" {
		t.Fatalf("node gone: %q", id)
	}
	c.releaseNode(id)

	// A pinned node is never overridden.
	c.conns["n2"] = &nodeConn{}
	id, _ = c.acquireChatNode(ctx, "c1", "n", nil, nil, time.Second)
	if id != "n" {
		t.Fatalf("pinned: %q", id)
	}
	c.releaseNode(id)
}

// silentNode accepts the controller's connection and never answers a job.
func silentNode(t *testing.T, c *Controller, id string) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.mu.Lock()
		c.conns[id] = &nodeConn{conn: conn}
		c.mu.Unlock()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			if _, _, err := ws.Read(ctx); err != nil {
				return
			}
		}
	}()
	for i := 0; i < 200; i++ {
		c.mu.Lock()
		_, ok := c.conns[id]
		c.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("node did not connect")
}

// A node that drops out mid-answer must not kill the chat: another node answers
// from the history the controller holds.
func TestChatNodeDropsMidReply(t *testing.T) {
	oldG, oldT := nodeGoneGrace, nodeGoneTick
	nodeGoneGrace, nodeGoneTick = 300*time.Millisecond, 50*time.Millisecond
	defer func() { nodeGoneGrace, nodeGoneTick = oldG, oldT }()
	c := twoNodeChat(t)
	var specs []InferenceSpec
	var mu sync.Mutex
	silentNode(t, c, "n")
	fakeInferNode(t, c, "n2", &specs, &mu)
	c.chats["c1"] = &Chat{ID: "c1"}
	go func() { // the first node's connection drops while it works
		time.Sleep(200 * time.Millisecond)
		c.mu.Lock()
		delete(c.conns, "n")
		c.mu.Unlock()
	}()
	start := time.Now()
	msg := c.answerChat(context.Background(), "c1", "auto", chatHistory([]ChatMsg{{Role: "user", Text: "hello"}}), func() {})
	if msg.Error != "" || msg.Text != "echo: hello" || msg.Node != "second" {
		t.Fatalf("answer %+v", msg)
	}
	if !strings.Contains(msg.Note, "dropped out") {
		t.Fatalf("note %q", msg.Note)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	if c.reserved["n"] != 0 || c.reserved["n2"] != 0 {
		t.Fatalf("slots not released: %v", c.reserved)
	}
	if ch := c.getChat("c1"); ch.aff.node != "n2" {
		t.Fatalf("affinity %+v", ch.aff)
	}
}
