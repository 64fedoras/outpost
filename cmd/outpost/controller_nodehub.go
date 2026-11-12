package main

// Node API: join, WebSocket hub, message handling, offline sweeper. Split out of controller.go.

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// ---- node API ----

func (c *Controller) handleJoin(w http.ResponseWriter, r *http.Request) {
	var req JoinRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, 400, ErrInfo{Code: "bad_request", Message: "bad json"})
		return
	}
	c.mu.Lock()
	exp, ok := c.tokens[req.Secret]
	if ok {
		delete(c.tokens, req.Secret) // single use
		if !time.Now().After(exp) {
			c.usedTok[tokenID(req.Secret)] = usedToken{Node: req.Hostname, At: time.Now()}
		}
	}
	if !ok || time.Now().After(exp) {
		c.mu.Unlock()
		writeJSON(w, 403, ErrInfo{Code: "bad_token", Message: "join token is invalid, used or expired; create a new one in the UI"})
		return
	}
	key := randHex(32)
	n := &NodeRec{ID: newID("nd"), KeyHash: hashSecret("node", key), Added: time.Now(), Name: req.Hostname}
	c.nodes[n.ID] = n
	c.saveNodesLocked()
	c.mu.Unlock()
	log.Printf("node %s (%s) joined from %s", n.ID, req.Hostname, r.RemoteAddr)
	writeJSON(w, 200, JoinResponse{NodeID: n.ID, NodeKey: key})
}

func (c *Controller) nodeFromBearer(r *http.Request) *NodeRec {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if key == "" {
		return nil
	}
	h := hashSecret("node", key)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range c.nodes {
		if subtle.ConstantTimeCompare([]byte(n.KeyHash), []byte(h)) == 1 {
			return n
		}
	}
	return nil
}

func (c *Controller) handleNodeWS(w http.ResponseWriter, r *http.Request) {
	n := c.nodeFromBearer(r)
	if n == nil {
		writeJSON(w, 401, ErrInfo{Code: "bad_key", Message: "unknown node key; the node may have been removed"})
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(32 << 20)
	nc := &nodeConn{conn: conn, addr: r.RemoteAddr}
	ctx := r.Context()

	// First message must be node.hello.
	_, msg, err := conn.Read(ctx)
	if err != nil {
		return
	}
	var env Envelope
	var hello Hello
	if json.Unmarshal(msg, &env) != nil || env.Type != "node.hello" || json.Unmarshal(env.Body, &hello) != nil {
		conn.Close(websocket.StatusPolicyViolation, "expected node.hello")
		return
	}
	proto, protoOK := negotiateProtocol(hello.Protocol)
	if !protoOK {
		_ = nc.send("node.welcome", Welcome{Accepted: false, Error: errf("unsupported_protocol", false, "controller speaks v%d", ProtocolVersion)})
		conn.Close(websocket.StatusPolicyViolation, "protocol")
		return
	}

	c.mu.Lock()
	if old := c.conns[n.ID]; old != nil {
		// Not under c.mu: Close waits up to 10 s for a peer that is often gone
		// (that is why the node reconnected), and c.mu stops the whole controller.
		go old.conn.Close(websocket.StatusGoingAway, "replaced by new connection")
	}
	hello.NodeID = n.ID
	n.Hello = &hello
	delete(c.restarts, n.ID)
	if len(n.Pool) > 0 && n.LoadPrefs != nil && hello.LoadedModel == "" {
		c.poolStale[n.ID] = time.Now() // a pooled main node came back: re-form now
	}
	if main := c.lendingToLocked(n.ID); main != "" {
		// A helper came back: the main node's llama-server lost its connection
		// to it and must be started again (the pool keeper does that).
		c.poolStale[main] = time.Now()
	}
	if u := c.updates[n.ID]; u != nil && compareVersions(hello.AgentVersion, u.Version) >= 0 {
		c.updates[n.ID] = &UpdateStatus{Version: hello.AgentVersion, State: "done", Updated: time.Now()}
	}
	c.conns[n.ID] = nc
	c.lastSeen[n.ID] = time.Now()
	n.LastAddr = nc.addr
	c.saveNodesLocked()
	// Jobs the controller thinks are running but the node doesn't list are lost.
	running := map[string]bool{}
	for _, id := range hello.RunningJobs {
		running[id] = true
	}
	for _, j := range c.jobs {
		if j.NodeID == n.ID && (j.Status == "dispatched" || j.Status == "running") && !running[j.ID] {
			c.finishJobLocked(j, "lost", errf("lost", true, "node reconnected without this job"))
		}
	}
	// Jobs the node still runs but the controller no longer tracks (it
	// restarted, or gave up on them) have nobody waiting for their result.
	// Cancel them so a resumed task does not queue behind its own old call.
	orphans := orphanJobsLocked(c.jobs, hello.RunningJobs)
	settings := c.nodeSettingsLocked(n)
	c.mu.Unlock()
	log.Printf("node %s (%s, %s/%s) connected from %s", n.ID, hello.Hostname, hello.OS, hello.Arch, r.RemoteAddr)
	c.clearRepin(n.ID)

	if err := nc.send("node.welcome", Welcome{Accepted: true, Protocol: proto, Settings: settings}); err != nil {
		return
	}
	for _, id := range orphans {
		log.Printf("node %s: cancelling job %s left over from before a controller restart", n.ID, id)
		_ = nc.send("job.cancel", JobCancel{JobID: id, Reason: "controller restarted"})
	}

	defer func() {
		c.mu.Lock()
		if c.conns[n.ID] == nc {
			delete(c.conns, n.ID)
		}
		c.mu.Unlock()
		log.Printf("node %s disconnected", n.ID)
		if !controllerClosing.Load() {
			c.recordHealth(n.ID, "disconnect")
		}
	}()

	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if json.Unmarshal(msg, &env) != nil {
			continue
		}
		c.handleNodeMessage(n.ID, &env)
	}
}

func (c *Controller) handleNodeMessage(nodeID string, env *Envelope) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastSeen[nodeID] = time.Now()
	switch env.Type {
	case "node.answer":
		c.deliverAnswer(env.Body)
	case "node.repin_ack":
		var a struct {
			Fingerprint string `json:"fingerprint"`
		}
		if json.Unmarshal(env.Body, &a) == nil && c.repinAcks != nil {
			c.repinAcks[nodeID] = a.Fingerprint
		}
	case "node.heartbeat":
		var hb Heartbeat
		if json.Unmarshal(env.Body, &hb) == nil {
			c.lastHB[nodeID] = &hb
			if !env.TS.IsZero() {
				if c.clockSkew == nil {
					c.clockSkew = map[string]time.Duration{}
				}
				c.clockSkew[nodeID] = env.TS.Sub(time.Now())
			}
			c.nudgeResume()
			if hb.RPCEndpoint != "" && !c.poolingOnLocked() {
				// A node is still lending (it resumed after a restart) while pooling is off.
				if nc := c.conns[nodeID]; nc != nil {
					log.Printf("pooling is off: telling %s to stop lending memory", c.nodeLabel(nodeID))
					go nc.send("rpc.stop", struct{}{})
				}
			}
		}
	case "job.output":
		var o JobOutput
		if json.Unmarshal(env.Body, &o) != nil {
			return
		}
		taskLive.jobOutput(o)
		c.genJobOutput(o)
		j := c.jobs[o.JobID]
		if j == nil {
			return
		}
		if j.Status == "dispatched" {
			j.Status = "running"
		}
		if j.OutBytes+len(o.Data) > maxJobOutputBytes {
			j.Truncated = true
			return
		}
		j.OutBytes += len(o.Data)
		j.Chunks = append(j.Chunks, outChunk{o.Seq, o.Stream, o.Data})
	case "job.result":
		var res JobResult
		if json.Unmarshal(env.Body, &res) != nil {
			return
		}
		j := c.jobs[res.JobID]
		if j == nil {
			return
		}
		j.Result = &res
		if j.Kind == "exec" {
			var eo ExecOutput
			if len(res.Output) > 0 && json.Unmarshal(res.Output, &eo) == nil {
				j.ExecOutput = &eo
			}
		} else if j.Kind == "inference" {
			var io InferenceOutput
			if len(res.Output) > 0 && json.Unmarshal(res.Output, &io) == nil {
				j.Inference = &io
			}
		}
		c.finishJobLocked(j, res.Status, nil)
	case "node.hello_update":
		var h Hello
		if json.Unmarshal(env.Body, &h) == nil {
			if n := c.nodes[nodeID]; n != nil {
				h.NodeID = nodeID
				n.Hello = &h
				c.saveNodesLocked()
			}
		}
	case "node.op_status":
		var st OpStatus
		if json.Unmarshal(env.Body, &st) == nil {
			c.recordNodeOpLocked(nodeID, st)
		}
	case "node.update_status":
		var us UpdateStatus
		if json.Unmarshal(env.Body, &us) == nil {
			us.Updated = time.Now()
			prev := c.updates[nodeID]
			c.updates[nodeID] = &us
			if prev == nil || prev.State != us.State {
				log.Printf("node %s update %s: %s %s", nodeID, us.Version, us.State, us.Error)
				if us.State == "failed" {
					name := nodeID
					if n := c.nodes[nodeID]; n != nil {
						name = displayName(n)
					}
					go c.deliverNotification("update_failed", "Update failed", fmt.Sprintf("%s could not switch to v%s: %s", name, us.Version, firstNonEmpty(us.Error, "no reason given")), "")
				}
			}
		}
	case "ws.closed":
		var wc WSClosed
		if json.Unmarshal(env.Body, &wc) != nil {
			return
		}
		for _, j := range c.jobs {
			if j.NodeID == nodeID && (j.workspace == wc.WorkspaceID || "ws_"+j.ID == wc.WorkspaceID) {
				wc := wc
				j.Cleaned = &wc
			}
		}
	}
}

func (c *Controller) finishJobLocked(j *JobRec, status string, e *ErrInfo) {
	j.Status = status
	now := time.Now()
	j.Ended = &now
	if e != nil && j.Result == nil {
		j.Result = &JobResult{JobID: j.ID, Status: status, Error: e, EndedAt: now}
	}
	c.notifyWaiterLocked(j)
}

// offlineSweeper drops connections that stop heartbeating and marks their jobs lost after 60 s.
func (c *Controller) offlineSweeper() {
	for range time.Tick(5 * time.Second) {
		c.sweepOnce(time.Now())
	}
}

// sweepOnce is one pass of the offline sweeper.
func (c *Controller) sweepOnce(now time.Time) {
	c.mu.Lock()
	for id, nc := range c.conns {
		if now.Sub(c.lastSeen[id]) > 3*heartbeatSeconds*time.Second {
			log.Printf("node %s missed 3 heartbeats; closing", id)
			// Close in the background: its close handshake waits up to 10 s
			// for a node that has stopped answering, and holding c.mu that
			// long froze the web UI and every other node whenever one died.
			go nc.conn.Close(websocket.StatusGoingAway, "heartbeat timeout")
			delete(c.conns, id)
		}
	}
	for _, j := range c.jobs {
		if j.Status != "dispatched" && j.Status != "running" {
			continue
		}
		if _, online := c.conns[j.NodeID]; !online && now.Sub(c.lastSeen[j.NodeID]) > 60*time.Second {
			c.finishJobLocked(j, "lost", errf("lost", true, "node offline for more than 60 s"))
		}
	}
	c.sweepOfflineNodes(now)
	for s, exp := range c.tokens {
		if now.After(exp) {
			delete(c.tokens, s)
		}
	}
	for id, u := range c.usedTok {
		if now.Sub(u.At) > 2*joinTokenTTL {
			delete(c.usedTok, id)
		}
	}
	c.mu.Unlock()
}
