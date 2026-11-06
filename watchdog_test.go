package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func fastWatchdog(t *testing.T) {
	q, tick, probe := watchdogQuiet, watchdogTick, watchdogProbe
	watchdogQuiet, watchdogTick, watchdogProbe = 150*time.Millisecond, 50*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { watchdogQuiet, watchdogTick, watchdogProbe = q, tick, probe })
}

func hungLlama(t *testing.T, healthy *atomic.Bool) int {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			if healthy.Load() {
				w.WriteHeader(200)
			} else {
				w.WriteHeader(503)
			}
			return
		}
		// A chat request that never sends anything (until the test ends).
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	_, p, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(p)
	return port
}

func TestWatchdogRestartsHungServer(t *testing.T) {
	fastWatchdog(t)
	var healthy atomic.Bool // false: /health fails
	l := idleTestMgr("m", nil)
	l.port = hungLlama(t, &healthy)
	start := time.Now()
	out, e := l.chat(context.Background(), InferenceSpec{Messages: []ChatMessage{{Role: "user", Content: "hi"}}}, func(string) {})
	if out != nil || e == nil || e.Code != "model_hung" || !e.Retryable {
		t.Fatalf("out=%v err=%+v", out, e)
	}
	if !isNodeProblem(e) {
		t.Fatal("a hung model must send the work to another node")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("watchdog too slow")
	}
	l.mu.Lock()
	ready := l.ready
	l.mu.Unlock()
	if ready {
		t.Fatal("hung server still marked ready")
	}
}

func TestWatchdogLeavesSlowButHealthyServer(t *testing.T) {
	fastWatchdog(t)
	var healthy atomic.Bool
	healthy.Store(true) // a long prompt: quiet, but /health answers
	l := idleTestMgr("m", nil)
	l.port = hungLlama(t, &healthy)
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	out, e := l.chat(ctx, InferenceSpec{Messages: []ChatMessage{{Role: "user", Content: "hi"}}}, func(string) {})
	if out != nil || e != nil {
		t.Fatalf("a healthy slow server was treated as hung: %+v", e)
	}
}
