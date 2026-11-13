package main

// Persistence, the UI API (state, tokens, node settings) and one-off jobs. Split out of controller.go.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// ---- persistence ----

func (c *Controller) nodesPath() string { return filepath.Join(c.dataDir, "nodes.json") }

func (c *Controller) loadNodes() error {
	_, err := os.Stat(c.nodesPath())
	_, berr := os.Stat(c.nodesPath() + ".bak")
	if errors.Is(err, os.ErrNotExist) && berr != nil {
		return nil
	}
	var list []*NodeRec
	usedBak, err := readWithBackup(c.nodesPath(), func(b []byte) error { list = nil; return json.Unmarshal(b, &list) })
	if err != nil {
		return fmt.Errorf("nodes.json: %w", err)
	}
	if usedBak {
		c.noteRecovered("nodes.json was damaged; loaded the previous copy")
	}
	for _, n := range list {
		c.nodes[n.ID] = n
	}
	return nil
}

// saveNodesLocked must be called with c.mu held.
func (c *Controller) saveNodesLocked() {
	list := make([]*NodeRec, 0, len(c.nodes))
	for _, n := range c.nodes {
		list = append(list, n)
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err == nil {
		err = writeFileAtomicBackup(c.nodesPath(), b, 0o600)
	}
	if err != nil {
		// Losing this file loses every node's key: say so instead of failing silently.
		log.Printf("saving the node list failed (the last saved copy is kept): %v", err)
	}
}

func hashSecret(salt, secret string) string {
	h := sha256.Sum256([]byte(salt + ":" + secret))
	return hex.EncodeToString(h[:])
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ---- UI API ----

type nodeView struct {
	*NodeRec
	KeyHash     string        `json:"key_hash,omitempty"` // shadows NodeRec.KeyHash: never sent to the UI
	Online      bool          `json:"online"`
	Addr        string        `json:"addr,omitempty"`
	LastSeen    *time.Time    `json:"last_seen,omitempty"`
	Heartbeat   *Heartbeat    `json:"heartbeat,omitempty"`
	MaxJobs     int           `json:"max_jobs"`
	RunningJobs int           `json:"running_jobs"`
	UpdateTo    string        `json:"update_to,omitempty"`
	Restarting  bool          `json:"restarting,omitempty"`
	LendingTo   string        `json:"lending_to,omitempty"`  // name of the node this one lends memory to
	PoolNames   []string      `json:"pool_names,omitempty"`  // names of nodes lending memory to this one
	IdleNow     string        `json:"idle_unload_effective"` // the idle unload wait that applies, as text ("1h", "never")
	OffHours    bool          `json:"off_hours,omitempty"`   // outside its working hours now (hours.go)
	CanWake     bool          `json:"can_wake,omitempty"`    // offline with a known MAC (wol.go)
	Update      *UpdateStatus `json:"update,omitempty"`
	Health      string        `json:"health,omitempty"` // amber or red when unreliable this week (node_health.go)
	HealthNote  string        `json:"health_note,omitempty"`
}

type jobSummary struct {
	ID       string     `json:"id"`
	NodeID   string     `json:"node_id"`
	NodeName string     `json:"node_name"`
	Command  string     `json:"command"`
	Status   string     `json:"status"`
	Created  time.Time  `json:"created"`
	Ended    *time.Time `json:"ended,omitempty"`
}

func (c *Controller) effectiveMaxLocked(n *NodeRec) int {
	if n.MaxJobs > 0 {
		return n.MaxJobs
	}
	if n.Hello != nil && n.Hello.MaxConcurrentJobs > 0 {
		return n.Hello.MaxConcurrentJobs
	}
	return 1
}

func (c *Controller) runningLocked(nodeID string) int {
	k := 0
	for _, j := range c.jobs {
		if j.NodeID == nodeID && (j.Status == "dispatched" || j.Status == "running") {
			k++
		}
	}
	return k
}

func (c *Controller) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, c.stateView())
}

// stateView is what /api/state returns (and what the state stream pushes).
func (c *Controller) stateView() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	nodes := make([]nodeView, 0, len(c.nodes))
	for id, n := range c.nodes {
		v := nodeView{NodeRec: n, MaxJobs: c.effectiveMaxLocked(n), RunningJobs: c.runningLocked(id),
			IdleNow: formatIdleDuration(idleUnloadFor(c.settings.IdleUnload, n.IdleUnload))}
		if nc, ok := c.conns[id]; ok {
			v.Online, v.Addr = true, nc.addr
		}
		if t, ok := c.lastSeen[id]; ok {
			t := t
			v.LastSeen = &t
		}
		v.Heartbeat = c.lastHB[id]
		v.OffHours = !n.Hours.open(time.Now())
		if _, col, note := c.healthOf(id); col != "green" {
			v.Health, v.HealthNote = col, note
		}
		v.CanWake = !v.Online && n.Hello != nil && len(n.Hello.MACs) > 0
		if rel := c.updateCandidateLocked(n); rel != nil {
			v.UpdateTo = rel.Version
		}
		v.Update = c.updates[id]
		if l := c.lendingToLocked(id); l != "" {
			v.LendingTo = displayName(c.nodes[l])
		}
		for _, h := range n.Pool {
			if hn := c.nodes[h]; hn != nil {
				v.PoolNames = append(v.PoolNames, displayName(hn))
			}
		}
		if t, ok := c.restarts[id]; ok && time.Since(t) < 3*time.Minute {
			v.Restarting = true
		}
		nodes = append(nodes, v)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Added.Before(nodes[j].Added) })
	jobs := []jobSummary{}
	for i := len(c.jobOrder) - 1; i >= 0 && len(jobs) < 50; i-- {
		j := c.jobs[c.jobOrder[i]]
		jobs = append(jobs, jobSummary{j.ID, j.NodeID, j.NodeName, j.Command, j.Status, j.Created, j.Ended})
	}
	releases := []*Release{}
	for _, r := range c.releases {
		releases = append(releases, r)
	}
	sort.Slice(releases, func(i, j int) bool { return releases[i].Platform < releases[j].Platform })
	ctlUpdate := ""
	if r := c.releases[thisPlatform()]; r != nil && compareVersions(r.Version, Version) > 0 {
		ctlUpdate = r.Version
	}
	return map[string]any{
		"version": Version, "platform": thisPlatform(), "controller_update_to": ctlUpdate,
		"controller_rollback_to": previousVersion(c.agentDir()),
		"fingerprint":            c.fingerprint, "advertise": c.advertise,
		"nodes": nodes, "jobs": jobs, "releases": releases, "ops": c.nodeOps,
		"has_claude_key": c.settings.AnthropicKeyEnc != "", "default_planner": c.settings.DefaultPlanner,
		"default_cap_usd": c.settings.DefaultCapUSD, "claude_model": c.settings.ClaudeModel,
		"default_fix_rounds": maxFixRounds, "default_test_minutes": int(testTimeout / time.Minute),
		"has_openrouter_key": c.settings.OpenRouterKeyEnc != "", "openrouter_model": c.settings.OpenRouterModel,
		"openrouter_label": openRouterLabel(c.settings.OpenRouterModel),
		"pooling_enabled":  c.poolingOnLocked(),
		"github_updates":   c.ghViewLocked(),
		"recovered":        c.recoveredList(),
		"disk_warning":     c.diskWarning(),
		"rollout":          c.rollout,
		"labels":           c.allLabelsLocked(),
		"templates":        c.templateViewsLocked(),
	}
}

func (c *Controller) handleNewToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Replace string `json:"replace"` // id of an unused code to cancel
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req)
	secret := randHex(24)
	exp := time.Now().Add(joinTokenTTL)
	c.mu.Lock()
	if req.Replace != "" {
		for s := range c.tokens {
			if tokenID(s) == req.Replace {
				delete(c.tokens, s)
			}
		}
	}
	c.tokens[secret] = exp
	c.mu.Unlock()
	tok := encodeJoinToken(c.advertise, c.fingerprintNow(), secret)
	cmds := c.installCommands(tok)
	writeJSON(w, 200, map[string]any{
		"id":        tokenID(secret),
		"token":     tok,
		"expires":   exp,
		"linux":     "outpost install node --join " + tok,
		"windows":   "outpost.exe install node --join " + tok,
		"macos":     "outpost install node --join " + tok,
		"curl_unix": cmds["curl_unix"],
		"curl_win":  cmds["curl_win"],
		"platforms": c.releasePlatforms(),
	})
}

type usedToken struct {
	Node string
	At   time.Time
}

// tokenID names a join token without revealing its secret.
func tokenID(secret string) string { return hashSecret("token-id", secret)[:16] }

// handleTokenStatus tells the Add node dialog whether its code is still
// usable, so it can show a fresh one once a node has joined with it.
func (c *Controller) handleTokenStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c.mu.Lock()
	defer c.mu.Unlock()
	if u, ok := c.usedTok[id]; ok {
		writeJSON(w, 200, map[string]any{"status": "used", "node": u.Node, "at": u.At})
		return
	}
	for s, exp := range c.tokens {
		if tokenID(s) == id && time.Now().Before(exp) {
			writeJSON(w, 200, map[string]any{"status": "valid", "expires": exp})
			return
		}
	}
	writeJSON(w, 200, map[string]any{"status": "expired"})
}

// nodeSettingsLocked is what a node is told: the exec switch, the heartbeat and
// the effective idle unload wait. c.mu must be held.
func (c *Controller) nodeSettingsLocked(n *NodeRec) NodeSettings {
	idle := idleUnloadFor(c.settings.IdleUnload, n.IdleUnload)
	if !n.Hours.open(time.Now()) {
		idle = minIdleUnload // outside working hours: free the memory soon after the last call
	}
	return NodeSettings{HeartbeatS: heartbeatSeconds, ExecEnabled: n.ExecEnabled,
		IdleUnloadS: int(idle / time.Second), YieldGPU: n.YieldGPU,
		Sandbox:      effectiveSandbox(c.settings.Sandbox, n.Sandbox),
		StorageLimit: int64(n.StorageLimitGB * (1 << 30)), StorageFull: n.StorageFull}
}

// pushNodeSettings sends every connected node its settings again.
func (c *Controller) pushNodeSettings() {
	type send struct {
		nc *nodeConn
		s  NodeSettings
	}
	var out []send
	c.mu.Lock()
	for id, n := range c.nodes {
		if nc := c.conns[id]; nc != nil {
			out = append(out, send{nc, c.nodeSettingsLocked(n)})
		}
	}
	c.mu.Unlock()
	for _, x := range out {
		_ = x.nc.send("node.settings", x.s)
	}
}

func (c *Controller) handleNodeSettings(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name        *string    `json:"name"`
		ExecEnabled *bool      `json:"exec_enabled"`
		MaxJobs     *int       `json:"max_jobs_override"`
		IdleUnload  *string    `json:"idle_unload"` // "" = use the global setting
		Sandbox     *string    `json:"sandbox"`     // "" = use the global setting; off | basic | strict
		Labels      *[]string  `json:"labels"`
		YieldGPU    *bool      `json:"yield_gpu"`
		Hours       *WorkHours `json:"hours"` // {} or from == to... see hours.go; "clear_hours" removes them
		ClearHours  bool       `json:"clear_hours"`
		StorageGB   *float64   `json:"storage_limit_gb"` // 0 = no limit
		StorageFull *string    `json:"storage_full"`     // "" (remove old models) or "refuse"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad json"})
		return
	}
	if req.Hours != nil {
		if err := req.Hours.check(); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
	}
	if req.IdleUnload != nil && strings.TrimSpace(*req.IdleUnload) != "" {
		if _, err := parseIdleDuration(*req.IdleUnload); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
	}
	if req.Sandbox != nil && strings.TrimSpace(*req.Sandbox) != "" && normSandbox(*req.Sandbox) == "" {
		writeJSON(w, 400, map[string]string{"error": "sandbox must be off, basic, strict or empty"})
		return
	}
	if req.StorageGB != nil && (*req.StorageGB < 0 || *req.StorageGB > 1<<20 || (*req.StorageGB > 0 && *req.StorageGB < 1)) {
		writeJSON(w, 400, map[string]string{"error": "storage limit must be at least 1 GB, or empty for no limit"})
		return
	}
	if req.StorageFull != nil && *req.StorageFull != "" && *req.StorageFull != "refuse" {
		writeJSON(w, 400, map[string]string{"error": "storage_full must be empty or refuse"})
		return
	}
	c.mu.Lock()
	n, ok := c.nodes[id]
	if !ok {
		c.mu.Unlock()
		writeJSON(w, 404, map[string]string{"error": "no such node"})
		return
	}
	if req.StorageGB != nil {
		n.StorageLimitGB = *req.StorageGB
	}
	if req.StorageFull != nil {
		n.StorageFull = *req.StorageFull
	}
	if req.Sandbox != nil {
		n.Sandbox = normSandbox(*req.Sandbox)
	}
	if req.IdleUnload != nil {
		n.IdleUnload = strings.TrimSpace(*req.IdleUnload)
	}
	if req.Name != nil {
		n.Name = strings.TrimSpace(*req.Name)
	}
	if req.ExecEnabled != nil {
		n.ExecEnabled = *req.ExecEnabled
	}
	if req.MaxJobs != nil && *req.MaxJobs >= 0 {
		n.MaxJobs = *req.MaxJobs
	}
	if req.Labels != nil {
		n.Labels = normLabels(*req.Labels)
	}
	if req.YieldGPU != nil {
		n.YieldGPU = *req.YieldGPU
	}
	if req.ClearHours {
		n.Hours = nil
	} else if req.Hours != nil {
		n.Hours = req.Hours
	}
	c.saveNodesLocked()
	nc := c.conns[id]
	settings := c.nodeSettingsLocked(n)
	c.mu.Unlock()
	if nc != nil {
		_ = nc.send("node.settings", settings)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleNodeDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c.mu.Lock()
	_, ok := c.nodes[id]
	nc := c.conns[id]
	delete(c.nodes, id)
	delete(c.conns, id)
	for _, other := range c.nodes {
		kept := other.Pool[:0]
		for _, h := range other.Pool {
			if h != id {
				kept = append(kept, h)
			}
		}
		other.Pool = kept
	}
	delete(c.lastHB, id)
	c.saveNodesLocked()
	c.mu.Unlock()
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "no such node"})
		return
	}
	if nc != nil {
		_ = nc.send("node.remove", map[string]string{"reason": "removed in UI"})
		time.AfterFunc(2*time.Second, func() { nc.conn.Close(websocket.StatusNormalClosure, "removed") })
	}
	log.Printf("node %s removed", id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleNewJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Command   string `json:"command"`
		InputName string `json:"input_name"`
		InputText string `json:"input_text"`
		Collect   string `json:"collect"`
		TimeoutS  int    `json:"timeout_s"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Command) == "" {
		writeJSON(w, 400, map[string]string{"error": "command required"})
		return
	}
	c.mu.Lock()
	n, ok := c.nodes[id]
	nc := c.conns[id]
	if !ok || nc == nil {
		c.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "node is offline"})
		return
	}
	if !n.ExecEnabled {
		c.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "exec is disabled on this node; enable it first"})
		return
	}
	if c.runningLocked(id) >= c.effectiveMaxLocked(n) {
		c.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "node is busy (at its job limit)"})
		return
	}
	argv := shellArgv(n.Hello, req.Command)
	timeout := req.TimeoutS
	if timeout <= 0 || timeout > 24*3600 {
		timeout = 600
	}
	job := &JobDispatch{JobID: newID("job"), Kind: "exec", TimeoutS: timeout}
	if req.InputText != "" {
		name := strings.TrimSpace(req.InputName)
		if name == "" {
			name = "input.txt"
		}
		job.Inputs = []FileEntry{inlineFile(name, []byte(req.InputText))}
	}
	for _, p := range strings.Split(req.Collect, ",") {
		if p = strings.TrimSpace(p); p != "" {
			job.Collect = append(job.Collect, p)
		}
	}
	job.Spec, _ = json.Marshal(ExecSpec{Argv: argv, Stream: true, Network: "allow"}) // typed by the owner: trusted
	rec := &JobRec{ID: job.JobID, NodeID: id, NodeName: displayName(n), Kind: "exec", Command: req.Command, Status: "dispatched", Created: time.Now()}
	c.addJobLocked(rec)
	c.mu.Unlock()

	if err := nc.send("job.dispatch", job); err != nil {
		c.mu.Lock()
		rec.Status = "failed"
		now := time.Now()
		rec.Ended = &now
		rec.Result = &JobResult{JobID: rec.ID, Status: "failed", Error: errf("send_failed", true, "%v", err)}
		c.mu.Unlock()
		writeJSON(w, 502, map[string]string{"error": "could not reach node: " + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"job_id": job.JobID})
}

func displayName(n *NodeRec) string {
	if n.Name != "" {
		return n.Name
	}
	if n.Hello != nil && n.Hello.Hostname != "" {
		return n.Hello.Hostname
	}
	return n.ID
}

func (c *Controller) addJobLocked(j *JobRec) {
	c.jobs[j.ID] = j
	c.jobOrder = append(c.jobOrder, j.ID)
	for len(c.jobOrder) > maxJobsKept {
		old := c.jobs[c.jobOrder[0]]
		if old != nil && (old.Status == "dispatched" || old.Status == "running") {
			break
		}
		delete(c.jobs, c.jobOrder[0])
		c.jobOrder = c.jobOrder[1:]
	}
}

func (c *Controller) handleGetJob(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	j, ok := c.jobs[r.PathValue("id")]
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "no such job"})
		return
	}
	writeJSON(w, 200, j)
}

func (c *Controller) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	j, ok := c.jobs[r.PathValue("id")]
	var nc *nodeConn
	if ok {
		nc = c.conns[j.NodeID]
	}
	c.mu.Unlock()
	if !ok || nc == nil {
		writeJSON(w, 409, map[string]string{"error": "job or node not available"})
		return
	}
	_ = nc.send("job.cancel", JobCancel{JobID: j.ID, Reason: "user"})
	writeJSON(w, 200, map[string]bool{"ok": true})
}
