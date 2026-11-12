package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Dropping a node that stopped answering must not hold the controller's lock
// while the WebSocket close handshake waits for it: that froze the web UI and
// every other node for seconds each time a machine was switched off.
func TestSweepDoesNotHoldLockWhileClosingDeadNode(t *testing.T) {
	accepted := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
		<-r.Context().Done()
	}))
	defer srv.Close()
	// The "node" connects and then never reads, so it never answers a close frame.
	client, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	server := <-accepted
	defer server.CloseNow()

	c := &Controller{dataDir: t.TempDir(), nodes: map[string]*NodeRec{"n1": {ID: "n1"}},
		conns:    map[string]*nodeConn{"n1": {conn: server}},
		lastSeen: map[string]time.Time{"n1": time.Now().Add(-time.Hour)},
		jobs:     map[string]*JobRec{}, tokens: map[string]time.Time{}, usedTok: map[string]usedToken{}}

	start := time.Now()
	c.sweepOnce(time.Now())
	c.mu.Lock() // what every web request and node message needs
	_, still := c.conns["n1"]
	c.mu.Unlock()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("the controller lock was held for %s while closing a dead node", d.Round(time.Millisecond))
	}
	if still {
		t.Fatal("the silent node was not dropped")
	}
}
