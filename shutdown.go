package main

// Clean shutdown of the controller. A stop from systemd (SIGTERM), Ctrl+C, or
// an update or rollback that replaces this process first writes every task's
// pending save and stops taking new requests, so nothing is cut off mid-write.
// Running tasks are picked up by auto-resume (tasks_autoresume.go) when the
// controller starts again, as after any restart.

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

// shutdownGrace is how long open HTTP requests get to finish.
const shutdownGrace = 10 * time.Second

var controllerClosing atomic.Bool

// flushTasks writes every task's pending delayed save.
func (c *Controller) flushTasks() {
	c.mu.Lock()
	list := make([]*Task, 0, len(c.tasks))
	for _, t := range c.tasks {
		list = append(list, t)
	}
	c.mu.Unlock()
	for _, t := range list {
		t.flush()
	}
}

// prepareRestart is called right before this process is replaced (update,
// rollback) or exits.
func (c *Controller) prepareRestart(why string) {
	if controllerClosing.Swap(true) {
		return
	}
	log.Printf("%s: saving tasks before exit", why)
	c.flushTasks()
}

// handleSignals shuts srv down on SIGINT or SIGTERM. done is closed once
// everything is saved, so the caller can return.
func (c *Controller) handleSignals(srv *http.Server, done chan<- struct{}) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	sig := <-ch
	c.prepareRestart("received " + sig.String())
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	_ = srv.Shutdown(ctx)
	c.flushTasks() // anything saved again while requests drained
	log.Printf("controller stopped")
	close(done)
}
