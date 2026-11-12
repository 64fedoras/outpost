package main

// Task build caches on nodes (node_pkgcache.go): every exec job of a task
// carries the task's ID as its cache scope, and when the task's run ends the
// controller tells the nodes to delete that cache.

import (
	"context"
	"net/http"
	"slices"
)

type cacheScopeKey struct{}

func withCacheScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, cacheScopeKey{}, scope)
}

func cacheScopeOf(ctx context.Context) string {
	s, _ := ctx.Value(cacheScopeKey{}).(string)
	return s
}

// closeCacheScope tells every connected node that knows task caches to
// delete this one. Nodes that were offline delete it on their own (idle
// timeout or restart).
func (c *Controller) closeCacheScope(scope string) {
	c.mu.Lock()
	var conns []*nodeConn
	for id, nc := range c.conns {
		if n := c.nodes[id]; n != nil && n.Hello != nil && slices.Contains(n.Hello.Features, "cache.scope") {
			conns = append(conns, nc)
		}
	}
	c.mu.Unlock()
	for _, nc := range conns {
		_ = nc.send("cache.close", CacheClose{Scope: scope})
	}
}

// handleClearPackageStore empties a node's clean package store.
func (c *Controller) handleClearPackageStore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c.mu.Lock()
	n, nc := c.nodes[id], c.conns[id]
	c.mu.Unlock()
	if n == nil || nc == nil {
		writeJSON(w, 409, map[string]string{"error": "the node is offline"})
		return
	}
	if n.Hello == nil || !slices.Contains(n.Hello.Features, "cache.scope") {
		writeJSON(w, 409, map[string]string{"error": "update this node first"})
		return
	}
	if err := nc.send("cache.clear", struct{}{}); err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
