package main

// Node-to-node model copies. The controller stays the source of truth: it
// holds each file's SHA-256 (from Hugging Face or from hashing an upload), it
// picks the peers, and it sends the peer addresses, certificate fingerprints
// and a one-time token to the downloading node over the node's pinned
// WebSocket. Nodes never vouch for their own copies. A controller-stored
// model stays available as a seed and as the fallback route.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"sort"
	"strings"
	"time"
)

const (
	maxPeers     = 3
	peerBadFor   = time.Hour
	peerGrantTTL = 24 * time.Hour
)

// pickPeers returns up to maxPeers online nodes (other than self) that hold the
// model and share it, idle ones first.
func (c *Controller) pickPeers(self, model string) []PeerSource {
	type cand struct {
		src  PeerSource
		busy bool
	}
	var cs []cand
	c.mu.Lock()
	for id, n := range c.nodes {
		nc := c.conns[id]
		if id == self || nc == nil || n.Hello == nil || n.Hello.Peer == nil || n.Hello.Peer.Fingerprint == "" {
			continue
		}
		if until, ok := c.peerBad[id]; ok && time.Now().Before(until) {
			continue
		}
		holds := false
		for _, mi := range n.Hello.Models {
			if mi.Name == model {
				holds = true
			}
		}
		if !holds {
			continue
		}
		busy := c.pipelines[id] != nil || len(n.Hello.RunningJobs) > 0
		cs = append(cs, cand{PeerSource{Name: id, Addr: net.JoinHostPort(hostOf(nc.addr), fmt.Sprint(n.Hello.Peer.Port)), Fingerprint: n.Hello.Peer.Fingerprint}, busy})
	}
	c.mu.Unlock()
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].busy != cs[j].busy {
			return !cs[i].busy
		}
		return cs[i].src.Name < cs[j].src.Name
	})
	var out []PeerSource
	for _, x := range cs {
		if len(out) < maxPeers {
			out = append(out, x.src)
		}
	}
	return out
}

// copyFromPeers has node id copy model m from other nodes. It returns nil
// without doing anything when there are no usable peers or a file has no
// trusted hash; an error means it tried and failed (the caller falls back).
func (c *Controller) copyFromPeers(ctx context.Context, id string, m *ModelRec) error {
	for _, f := range m.Files {
		if len(f.SHA256) != 64 {
			return nil
		}
	}
	peers := c.pickPeers(id, m.Name)
	if len(peers) == 0 {
		return nil
	}
	c.mu.Lock()
	nc := c.conns[id]
	c.mu.Unlock()
	if nc == nil {
		return nil
	}
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return err
	}
	token := hex.EncodeToString(tok)
	var granted []string
	defer func() { // the token is good for this transfer only
		for _, pid := range granted {
			c.mu.Lock()
			pc := c.conns[pid]
			c.mu.Unlock()
			if pc != nil {
				_ = pc.send("peer.grant", PeerGrant{Token: token})
			}
		}
	}()
	for _, p := range peers {
		c.mu.Lock()
		pc := c.conns[p.Name]
		c.mu.Unlock()
		if pc == nil {
			continue
		}
		if err := pc.send("peer.grant", PeerGrant{Token: token, TTLSeconds: int(peerGrantTTL / time.Second)}); err != nil {
			continue
		}
		granted = append(granted, p.Name)
	}
	var names []string
	for _, p := range peers {
		names = append(names, p.Name)
	}
	log.Printf("node %s: copying %s from nodes %s", id, m.Name, strings.Join(names, ", "))
	c.setNodeOp(id, OpStatus{Op: "model.push", Target: m.Name, State: "downloading", BytesTotal: totalBytes(m.Files), Detail: "from " + strings.Join(names, ", ")})
	if err := nc.send("model.push", ModelPush{Name: m.Name, Files: m.Files, Peers: peers, PeerToken: token}); err != nil {
		return err
	}
	err := c.waitNodeOp(ctx, id, "model.push", m.Name, 24*time.Hour)
	if err != nil {
		c.mu.Lock()
		if st := c.nodeOps[id]; st != nil {
			for _, bad := range st.BadPeers {
				if c.peerBad == nil {
					c.peerBad = map[string]time.Time{}
				}
				c.peerBad[bad] = time.Now().Add(peerBadFor)
				log.Printf("node %s served %s with a failing hash; skipped as a source for %s", bad, m.Name, peerBadFor)
			}
		}
		c.mu.Unlock()
	}
	return err
}
