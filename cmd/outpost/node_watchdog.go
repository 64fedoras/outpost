package main

// Hung-model watchdog. While a chat request runs, llama-server normally sends
// something at least every few seconds once it starts answering; a long prompt
// on a slow CPU can keep it quiet for minutes, but its /health endpoint still
// answers. If the stream is quiet for watchdogQuiet and /health then fails
// twice in a row, the server is taken as hung: the request is cancelled with a
// retryable "model_hung" error (the controller gives the work to another node)
// and llama-server is killed, which its supervisor (watch) restarts.

import (
	"context"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

var (
	watchdogQuiet = 3 * time.Minute
	watchdogTick  = 30 * time.Second
	watchdogProbe = 10 * time.Second
)

type watchdog struct {
	last    atomic.Int64 // unix nanos of the last line from llama-server
	tripped atomic.Bool
	quiet   time.Duration
}

func (d *watchdog) alive()     { d.last.Store(time.Now().UnixNano()) }
func (d *watchdog) hung() bool { return d.tripped.Load() }
func (d *watchdog) err() *ErrInfo {
	return errf("model_hung", true, "llama-server stopped answering (no output and no health check for %s); it is being restarted and the work goes to another node", d.quiet.Round(time.Second))
}

func (l *llamaMgr) startWatchdog(ctx context.Context, cancel context.CancelFunc) *watchdog {
	d := &watchdog{quiet: watchdogQuiet}
	d.alive()
	go func() {
		t := time.NewTicker(watchdogTick)
		defer t.Stop()
		strikes := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if time.Since(time.Unix(0, d.last.Load())) < watchdogQuiet {
				strikes = 0
				continue
			}
			if l.healthy() {
				strikes = 0
				continue
			}
			strikes++
			if strikes < 2 {
				continue
			}
			d.tripped.Store(true)
			log.Printf("watchdog: llama-server gave no output for %s and failed its health check twice; restarting it", time.Since(time.Unix(0, d.last.Load())).Round(time.Second))
			cancel()
			l.killHung()
			return
		}
	}()
	return d
}

// healthy asks llama-server's /health with a short timeout.
func (l *llamaMgr) healthy() bool {
	client := &http.Client{Timeout: watchdogProbe}
	resp, err := client.Get(l.baseURL() + "/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// killHung kills the current llama-server without bumping its generation, so
// watch() sees an unexpected exit and starts it again (up to 3 times).
func (l *llamaMgr) killHung() {
	l.mu.Lock()
	cmd := l.cmd
	l.ready = false
	l.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	l.n.pushHeartbeat()
}
