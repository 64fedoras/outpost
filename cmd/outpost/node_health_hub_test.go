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

// hubController is a controller with just enough state for handleNodeWS.
func hubController(t *testing.T) (*Controller, string) {
	t.Helper()
	key := "k-test"
	c := &Controller{
		dataDir: t.TempDir(),
		nodes:   map[string]*NodeRec{"n1": {ID: "n1", KeyHash: hashSecret("node", key), Name: "n1"}},
		conns:   map[string]*nodeConn{}, lastHB: map[string]*Heartbeat{}, lastSeen: map[string]time.Time{},
		jobs: map[string]*JobRec{}, updates: map[string]*UpdateStatus{}, waiters: map[string]chan *JobResult{},
		nodeOps: map[string]*nodeOpView{}, pipelines: map[string]*pipeline{}, reserved: map[string]int{},
		cooldown: map[string]time.Time{}, restarts: map[string]time.Time{}, poolStale: map[string]time.Time{},
	}
	return c, key
}

// connectAndDrop connects as node n1, runs before (which may send messages),
// then drops the connection and waits until the controller has noticed.
func connectAndDrop(t *testing.T, c *Controller, key string, before func(conn *websocket.Conn), code websocket.StatusCode) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(c.handleNodeWS))
	defer srv.Close()
	ctx := context.Background()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + key}}})
	if err != nil {
		t.Fatal(err)
	}
	send := func(typ string, body any) {
		b, err := newEnvelope(typ, body)
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}
	send("node.hello", Hello{Protocol: []int{ProtocolVersion}, Hostname: "n1", AgentVersion: Version})
	if _, _, err := conn.Read(ctx); err != nil { // node.welcome
		t.Fatal(err)
	}
	if before != nil {
		before(conn)
	}
	if code != 0 {
		conn.Close(code, "bye")
	} else {
		conn.CloseNow()
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		gone := c.conns["n1"] == nil
		c.mu.Unlock()
		if gone {
			time.Sleep(50 * time.Millisecond) // the deferred health record runs right after
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("controller did not notice the disconnect")
}

func healthScore(c *Controller) int {
	s, _, _ := c.healthOf("n1")
	return s
}

// An Outpost update restarts the node on purpose; that must not make the node
// look unreliable. A plain drop still counts.
func TestUpdateRestartIsNotADisconnect(t *testing.T) {
	c, key := hubController(t)
	connectAndDrop(t, c, key, func(conn *websocket.Conn) {
		b, _ := newEnvelope("node.update_status", UpdateStatus{Version: "9.9.9", State: "restarting"})
		_ = conn.Write(context.Background(), websocket.MessageText, b)
		time.Sleep(100 * time.Millisecond)
	}, 0)
	if s := healthScore(c); s != 0 {
		t.Fatalf("update restart counted: score %d", s)
	}

	c, key = hubController(t)
	connectAndDrop(t, c, key, nil, websocket.StatusServiceRestart)
	if s := healthScore(c); s != 0 {
		t.Fatalf("service restart counted: score %d", s)
	}

	c, key = hubController(t)
	connectAndDrop(t, c, key, func(*websocket.Conn) {
		c.mu.Lock()
		c.restarts["n1"] = time.Now() // what handleRestartNode does
		c.mu.Unlock()
	}, 0)
	if s := healthScore(c); s != 0 {
		t.Fatalf("restart from the UI counted: score %d", s)
	}

	c, key = hubController(t)
	connectAndDrop(t, c, key, nil, 0)
	if s := healthScore(c); s != 1 {
		t.Fatalf("plain drop: score %d, want 1", s)
	}
}

// A node that reconnects long after its update keeps the update's own time,
// so later drops are counted again.
func TestReconnectKeepsUpdateDoneTime(t *testing.T) {
	c, key := hubController(t)
	old := time.Now().Add(-time.Hour)
	c.updates["n1"] = &UpdateStatus{Version: Version, State: "done", Updated: old}
	connectAndDrop(t, c, key, nil, 0)
	if got := c.updates["n1"].Updated; !got.Equal(old) {
		t.Fatalf("reconnect moved the update time to %v", got)
	}
	if s := healthScore(c); s != 1 {
		t.Fatalf("drop an hour after an update: score %d, want 1", s)
	}
}
