package main

// Memory pooling (llama.cpp RPC) orchestration. Split out of controller_models.go.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
)

// ---------- pooling (llama.cpp RPC) ----------

// installRuntimeOn makes sure node id runs llama.cpp (a given build, or the newest).
func (c *Controller) installRuntimeOn(ctx context.Context, id, build string) error {
	c.mu.Lock()
	var hello Hello
	if n := c.nodes[id]; n != nil && n.Hello != nil {
		hello = *n.Hello
	}
	nc := c.conns[id]
	c.mu.Unlock()
	if nc == nil {
		return fmt.Errorf("%s is offline", c.nodeLabel(id))
	}
	variant, _ := nodeVariant(&hello)
	if variant == "" {
		return fmt.Errorf("no llama.cpp build for %s/%s", hello.OS, hello.Arch)
	}
	c.setNodeOp(id, OpStatus{Op: "runtime", Target: variant, State: "preparing"})
	rt, err := c.ensureRuntimeBuild(ctx, variant, build)
	if err != nil {
		return fmt.Errorf("llama.cpp: %w", err)
	}
	if hello.Runtime == nil || hello.Runtime.Build != rt.Build || hello.Runtime.Variant != variant {
		if err := nc.send("runtime.install", RuntimeInstall{Build: rt.Build, Variant: variant, Archive: rt.Archive, SHA256: rt.SHA256, Bytes: rt.Bytes}); err != nil {
			return err
		}
		if err := c.waitNodeOp(ctx, id, "runtime", variant, 30*time.Minute); err != nil {
			return fmt.Errorf("installing llama.cpp on %s: %w", c.nodeLabel(id), err)
		}
	}
	return nil
}

// poolBuild picks one llama.cpp build that all pool members can run.
func (c *Controller) poolBuild(ctx context.Context, members []string) (string, error) {
	var variants []string
	seen := map[string]bool{}
	c.mu.Lock()
	for _, m := range members {
		n := c.nodes[m]
		if n == nil || n.Hello == nil {
			c.mu.Unlock()
			return "", fmt.Errorf("pool member %s is unknown or never connected", m)
		}
		v, _ := nodeVariant(n.Hello)
		if v != "" && !seen[v] {
			seen[v] = true
			variants = append(variants, v)
		}
	}
	c.mu.Unlock()
	rels, err := c.latestReleases(ctx, false)
	if err != nil {
		// Offline: use the build the controller already has, if all variants share it.
		c.mu.Lock()
		defer c.mu.Unlock()
		b := ""
		for _, v := range variants {
			rt := c.runtimes[v]
			if rt == nil || (b != "" && rt.Build != b) {
				return "", fmt.Errorf("cannot check llama.cpp releases (%v) and the pool has no common build on the controller", err)
			}
			b = rt.Build
		}
		return b, nil
	}
	b := commonBuild(rels, variants)
	if b == "" {
		return "", fmt.Errorf("no recent llama.cpp release has builds for all of: %s", strings.Join(variants, ", "))
	}
	return b, nil
}

func hostOf(addr string) string {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return h
}

// startHelpers asks each helper to lend its memory to main and returns their
// RPC endpoints. Each helper only accepts connections from main's address.
func (c *Controller) startHelpers(ctx context.Context, main string, helpers []string, build string) ([]string, error) {
	c.mu.Lock()
	mainConn := c.conns[main]
	c.mu.Unlock()
	if mainConn == nil {
		return nil, errors.New("main node is offline")
	}
	mainIP := hostOf(mainConn.addr)
	var endpoints []string
	for _, h := range helpers {
		if err := c.installRuntimeOn(ctx, h, build); err != nil {
			return nil, err
		}
		c.mu.Lock()
		hc := c.conns[h]
		threads := 0
		if n := c.nodes[h]; n != nil && n.Hello != nil {
			threads = n.Hello.CPU.Threads
		}
		c.mu.Unlock()
		if hc == nil {
			return nil, fmt.Errorf("%s is offline", c.nodeLabel(h))
		}
		c.setNodeOp(h, OpStatus{Op: "rpc", Target: "serve", State: "starting"})
		if err := hc.send("rpc.start", RPCStart{Host: hostOf(hc.addr), Allow: []string{mainIP}, Threads: threads}); err != nil {
			return nil, err
		}
		if err := c.waitNodeOp(ctx, h, "rpc", "serve", 2*time.Minute); err != nil {
			return nil, fmt.Errorf("%s could not lend its memory: %w", c.nodeLabel(h), err)
		}
		c.mu.Lock()
		ep := ""
		if hb := c.lastHB[h]; hb != nil {
			ep = hb.RPCEndpoint
		}
		c.mu.Unlock()
		if ep == "" {
			return nil, fmt.Errorf("%s started lending but reported no address", c.nodeLabel(h))
		}
		endpoints = append(endpoints, ep)
	}
	return endpoints, nil
}

// checkPool validates a requested pool for main.
func (c *Controller) checkPool(main string, pool []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if lender := c.lendingToLocked(main); lender != "" {
		return fmt.Errorf("this node lends its memory to %s; unload that model first", displayName(c.nodes[lender]))
	}
	seen := map[string]bool{}
	for _, h := range pool {
		n := c.nodes[h]
		switch {
		case h == main || seen[h]:
			return errors.New("each helper can be listed once and cannot be the main node")
		case n == nil:
			return fmt.Errorf("unknown node %s", h)
		case c.conns[h] == nil:
			return fmt.Errorf("%s is offline", displayName(n))
		case len(n.Pool) > 0:
			return fmt.Errorf("%s runs its own pool; unload it first", displayName(n))
		}
		if other := c.lendingToLocked(h); other != "" && other != main {
			return fmt.Errorf("%s already lends its memory to %s", displayName(n), displayName(c.nodes[other]))
		}
		seen[h] = true
	}
	return nil
}

// lendingToLocked returns the main node that helper lends memory to, if any.
func (c *Controller) lendingToLocked(helper string) string {
	for id, n := range c.nodes {
		for _, h := range n.Pool {
			if h == helper {
				return id
			}
		}
	}
	return ""
}

// releaseHelpers tells helpers that left a pool to stop lending.
func (c *Controller) releaseHelpers(old, keep []string) {
	keepSet := map[string]bool{}
	for _, k := range keep {
		keepSet[k] = true
	}
	for _, h := range old {
		if keepSet[h] {
			continue
		}
		c.mu.Lock()
		hc := c.conns[h]
		c.mu.Unlock()
		if hc != nil {
			_ = hc.send("rpc.stop", struct{}{})
		}
	}
}

// poolKeeper re-forms pools after a main node or a helper restarts: when all
// members are online and the main node's model is not running, it loads it again.
func (c *Controller) poolKeeper() {
	last := map[string]time.Time{}
	for range time.Tick(15 * time.Second) {
		type job struct {
			id  string
			ml  ModelLoad
			ctx context.Context
			p   *pipeline
		}
		var todo []job
		c.mu.Lock()
		if !c.poolingOnLocked() {
			c.mu.Unlock()
			continue
		}
		for id, n := range c.nodes {
			if len(n.Pool) == 0 || n.LoadPrefs == nil || c.pipelines[id] != nil || c.conns[id] == nil {
				continue
			}
			hb := c.lastHB[id]
			stale := !c.poolStale[id].IsZero() && c.poolStale[id].After(last[id])
			if !stale && hb != nil && hb.LoadedModel == n.LoadPrefs.Name && (hb.InferenceRdy || hb.LlamaError == "") {
				continue // running or starting
			}
			all := true
			for _, h := range n.Pool {
				if c.conns[h] == nil {
					all = false
				}
			}
			if !all || (!stale && time.Since(last[id]) < 90*time.Second) {
				continue
			}
			delete(c.poolStale, id)
			last[id] = time.Now()
			ctx, cancel := context.WithCancel(context.Background())
			p := &pipeline{cancel}
			c.pipelines[id] = p
			todo = append(todo, job{id, *n.LoadPrefs, ctx, p})
		}
		c.mu.Unlock()
		for _, j := range todo {
			go func(j job) {
				defer c.endPipeline(j.id, j.p)
				log.Printf("pool keeper: starting the pooled model on %s again", c.nodeLabel(j.id))
				if err := c.loadPipeline(j.ctx, j.id, j.ml); err != nil {
					if j.ctx.Err() != nil {
						return
					}
					log.Printf("pool keeper: %s: %v", c.nodeLabel(j.id), err)
					c.setNodeOp(j.id, OpStatus{Op: "model.load", Target: j.ml.Name, State: "failed", Error: err.Error()})
				}
			}(j)
		}
	}
}

// endPipeline clears a finished model setup (if it is still the current one).
type pipeline struct{ cancel context.CancelFunc }

func (c *Controller) endPipeline(id string, p *pipeline) {
	p.cancel()
	c.mu.Lock()
	if c.pipelines[id] == p {
		delete(c.pipelines, id)
	}
	c.mu.Unlock()
}

// validCPUMoe accepts "" (off), "all" or a layer count from 1 to 999.
func validCPUMoe(v string) bool {
	if v == "" || v == "all" {
		return true
	}
	n, err := strconv.Atoi(v)
	return err == nil && n >= 1 && n <= 999
}
