package main

import (
	"log"
)

// Memory pooling (llama.cpp RPC) is off by default. llama.cpp's RPC protocol
// has no authentication or encryption; Outpost only keeps it on loopback
// behind a source-IP allow-list (see node_rpc.go), which is fine on a trusted
// LAN and nothing more. Until pooling is switched on in Settings the
// controller never asks a node to lend memory and the pool option is hidden.

// poolingOnLocked reports whether pooling is enabled. Call with c.mu held.
func (c *Controller) poolingOnLocked() bool {
	return c.settings.PoolingEnabled != nil && *c.settings.PoolingEnabled
}

func (c *Controller) poolingOn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.poolingOnLocked()
}

// migratePooling settles the setting once nodes are loaded. An install that
// already has a pool configured keeps it working (pooling on, with a notice in
// Settings); every other install starts with pooling off.
func (c *Controller) migratePooling() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settings.PoolingEnabled != nil {
		return
	}
	on := false
	for _, n := range c.nodes {
		if len(n.Pool) > 0 {
			on = true
		}
	}
	c.settings.PoolingEnabled = &on
	c.settings.PoolingNotice = on
	if on {
		log.Printf("pooling: a pool is configured, so memory pooling stays enabled; review it under Settings")
	}
	_ = c.saveSettingsLocked()
}

// disablePooling tears down every pool: configured pools are dropped, the main
// nodes unload their pooled models and every node is told to stop lending.
func (c *Controller) disablePooling() {
	type target struct {
		conn   *nodeConn
		unload bool
	}
	var todo []target
	c.mu.Lock()
	for id, n := range c.nodes {
		pooled := len(n.Pool) > 0
		if pooled {
			if p := c.pipelines[id]; p != nil {
				p.cancel()
			}
			n.Pool, n.LoadPrefs = nil, nil
		}
		if nc := c.conns[id]; nc != nil {
			todo = append(todo, target{nc, pooled})
		}
	}
	c.saveNodesLocked()
	c.mu.Unlock()
	for _, t := range todo {
		if t.unload {
			_ = t.conn.send("model.unload", struct{}{})
		}
		_ = t.conn.send("rpc.stop", struct{}{})
	}
	log.Printf("pooling disabled: all pools dissolved")
}
