package main

// Idle unload and wake. Split out of node_llama.go.

import (
	"context"
	"errors"
	"log"
	"time"
)

// ---------- idle unload ----------

func (l *llamaMgr) touch() {
	l.mu.Lock()
	l.lastUsed = time.Now()
	l.mu.Unlock()
}

func (l *llamaMgr) setIdleAfter(d time.Duration) {
	l.mu.Lock()
	l.idleAfter = d
	l.lastUsed = time.Now() // a changed setting starts a fresh wait
	l.mu.Unlock()
}

// coldModel is the model unloaded for idleness ("" when none).
func (l *llamaMgr) coldModel() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cold != nil && l.cmd == nil {
		return l.cold.Name
	}
	return ""
}

// idleCheck unloads the model if it has been idle long enough. A pooled model is
// never unloaded: its helpers would have to be restarted by the controller.
func (l *llamaMgr) idleCheck(now time.Time) bool {
	l.mu.Lock()
	if l.idleAfter <= 0 || l.cmd == nil || !l.ready || l.busy > 0 || len(l.opts.RPC) > 0 ||
		now.Sub(l.lastUsed) < l.idleAfter {
		l.mu.Unlock()
		return false
	}
	idle := l.idleAfter
	name := l.goCold()
	log.Printf("model %s was idle for %s; unloading it (it loads again on the next request)", name, formatIdleDuration(idle))
	l.finishCold()
	return true
}

// goCold remembers the loaded model for reloading and returns its name. It must be
// called with l.mu held and releases it; finishCold then stops the process.
func (l *llamaMgr) goCold() string {
	ml := l.opts
	l.cold = &ml // set before stopping, so a request arriving now waits and reloads
	name := l.model
	l.mu.Unlock()
	return name
}

func (l *llamaMgr) finishCold() {
	l.stopProc()
	l.n.resendHello()
}

// errUnloadBusy and friends are the reasons unloadNow refuses; the tray shows them.
var (
	errUnloadNothing = errors.New("no model is loaded")
	errUnloadLoading = errors.New("the model is still loading; try again when it is ready")
	errUnloadBusy    = errors.New("the model is answering a request; try again when the node is idle")
	errUnloadPooled  = errors.New("this model is pooled across nodes; unload it from the controller's Nodes page")
)

// unloadNow unloads the model at once, as an idle unload does: memory is freed and
// the model loads again on the next request for it. It refuses while the model is
// loading, answering a request or pooled (the controller owns pooled helpers).
// Callers check for running jobs first.
func (l *llamaMgr) unloadNow(why string) (string, error) {
	l.mu.Lock()
	switch {
	case l.cmd == nil:
		l.mu.Unlock()
		return "", errUnloadNothing
	case len(l.opts.RPC) > 0:
		l.mu.Unlock()
		return "", errUnloadPooled
	case !l.ready:
		l.mu.Unlock()
		return "", errUnloadLoading
	case l.busy > 0:
		l.mu.Unlock()
		return "", errUnloadBusy
	}
	name := l.goCold()
	log.Printf("unloading model %s (%s); it loads again on the next request", name, why)
	l.finishCold()
	return name, nil
}

func (l *llamaMgr) idleLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			l.idleCheck(now)
		}
	}
}

// wake loads the idle-unloaded model again when a request for it arrives.
func (l *llamaMgr) wake(model string) *ErrInfo {
	l.mu.Lock()
	cold := l.cold
	l.mu.Unlock()
	if cold == nil || (model != "" && model != cold.Name) {
		return nil
	}
	l.wakeMu.Lock()
	defer l.wakeMu.Unlock()
	l.mu.Lock()
	cold = l.cold
	l.mu.Unlock()
	if cold == nil {
		return nil // another request already reloaded it
	}
	log.Printf("request for idle-unloaded model %s; loading it again", cold.Name)
	l.load(*cold)
	l.mu.Lock()
	ready, msg := l.ready, l.lastErr
	l.mu.Unlock()
	if !ready {
		if msg == "" {
			msg = "the model did not load"
		}
		// load() ends a failed start with stop(), which forgets the model. Put it back so the
		// node stays available and the next request tries again, instead of waiting for the
		// Nodes menu.
		l.mu.Lock()
		if l.cmd == nil && l.cold == nil {
			l.cold = cold
		}
		l.mu.Unlock()
		l.n.resendHello()
		return errf("inference_unavailable", true, "reloading %s after idle unload failed: %s", cold.Name, msg)
	}
	return nil
}
