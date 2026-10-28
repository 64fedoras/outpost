package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

//go:embed web
var webFS embed.FS

const (
	maxJobOutputBytes = 1 << 20 // per job, kept on the controller for the UI
	maxJobsKept       = 200
	joinTokenTTL      = time.Hour
	heartbeatSeconds  = 10
)

type NodeRec struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	KeyHash     string     `json:"key_hash"`
	ExecEnabled bool       `json:"exec_enabled"`
	MaxJobs     int        `json:"max_jobs_override"` // 0 = use the node's own value
	Added       time.Time  `json:"added"`
	Hello       *Hello     `json:"hello,omitempty"`
	LoadPrefs   *ModelLoad `json:"load_prefs,omitempty"` // last model loaded from the UI
	Pool        []string   `json:"pool,omitempty"`       // helper nodes lending memory to this node's model
}

type nodeConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
	addr string
}

func (nc *nodeConn) send(typ string, body any) error {
	b, err := newEnvelope(typ, body)
	if err != nil {
		return err
	}
	nc.mu.Lock()
	defer nc.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return nc.conn.Write(ctx, websocket.MessageText, b)
}

type outChunk struct {
	Seq    int    `json:"seq"`
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type JobRec struct {
	ID         string           `json:"id"`
	NodeID     string           `json:"node_id"`
	NodeName   string           `json:"node_name"`
	Kind       string           `json:"kind"`
	Command    string           `json:"command"`
	Status     string           `json:"status"` // dispatched | running | succeeded | failed | cancelled | timed_out | lost
	Created    time.Time        `json:"created"`
	Ended      *time.Time       `json:"ended,omitempty"`
	Chunks     []outChunk       `json:"chunks,omitempty"`
	OutBytes   int              `json:"-"`
	Truncated  bool             `json:"truncated"`
	Result     *JobResult       `json:"result,omitempty"`
	ExecOutput *ExecOutput      `json:"exec_output,omitempty"`
	Inference  *InferenceOutput `json:"inference,omitempty"`
	Cleaned    *WSClosed        `json:"cleaned,omitempty"`
	workspace  string
}

type Controller struct {
	mu          sync.Mutex
	dataDir     string
	advertise   string
	fingerprint string
	cert        tls.Certificate
	repinAcks   map[string]string
	admin       adminRecord

	nodes      map[string]*NodeRec
	conns      map[string]*nodeConn
	lastHB     map[string]*Heartbeat
	lastSeen   map[string]time.Time
	tokens     map[string]time.Time
	usedTok    map[string]usedToken // token id -> who used it (so the Add node dialog can move on)
	sessions   map[string]time.Time
	sessMeta   map[string]sessionMeta
	apiTokens  []apiToken
	loginFails map[string][]time.Time
	jobs       map[string]*JobRec
	jobOrder   []string
	releases   map[string]*Release
	updates    map[string]*UpdateStatus

	settings  Settings
	waiters   map[string]chan *JobResult
	models    map[string]*ModelRec
	runtimes  map[string]*RuntimeRec
	rtCache   *runtimeCache
	nodeOps   map[string]*nodeOpView
	pipelines map[string]*pipeline // model setups in progress; cancel stops one
	peerBad   map[string]time.Time // nodes skipped as model sources until then (served data failing its hash)
	tasks     map[string]*Task
	reserved  map[string]bool
	cooldown  map[string]time.Time
	restarts  map[string]time.Time
	poolStale map[string]time.Time // main node -> when a helper reconnected
}

func runController(args []string) error {
	fsf := flag.NewFlagSet("controller", flag.ExitOnError)
	listen := fsf.String("listen", ":8443", "address to listen on")
	data := fsf.String("data", defaultDataDir("controller"), "data directory")
	advertise := fsf.String("advertise", "", "URL nodes use to reach this controller (default: https://<first LAN IP><listen port>)")
	resetPw := fsf.Bool("reset-password", false, "generate a new admin password and print it")
	_ = fsf.Parse(args)

	if err := os.MkdirAll(*data, 0o700); err != nil {
		return err
	}
	cert, fp, err := loadOrCreateCert(*data)
	if err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	c := &Controller{
		dataDir: *data, fingerprint: fp, cert: cert,
		nodes: map[string]*NodeRec{}, conns: map[string]*nodeConn{},
		lastHB: map[string]*Heartbeat{}, lastSeen: map[string]time.Time{},
		tokens: map[string]time.Time{}, usedTok: map[string]usedToken{}, sessions: map[string]time.Time{}, sessMeta: map[string]sessionMeta{}, loginFails: map[string][]time.Time{},
		jobs: map[string]*JobRec{}, updates: map[string]*UpdateStatus{},
		waiters: map[string]chan *JobResult{}, nodeOps: map[string]*nodeOpView{},
		pipelines: map[string]*pipeline{}, reserved: map[string]bool{}, cooldown: map[string]time.Time{}, restarts: map[string]time.Time{}, poolStale: map[string]time.Time{},
	}
	launchNewest(c.agentDir(), os.Args[1:]) // switch to an installed update, if newer
	c.loadReleases()
	c.loadSessions()
	c.loadAPITokens()
	c.loadSettings()
	c.loadModels()
	c.loadRuntimes()
	c.loadTasks()
	c.advertise = *advertise
	if c.advertise == "" {
		_, port, _ := net.SplitHostPort(*listen)
		host := "127.0.0.1"
		if ips := localIPs(); len(ips) > 0 {
			host = ips[0].String()
		}
		c.advertise = "https://" + net.JoinHostPort(host, port)
	}
	if err := c.loadNodes(); err != nil {
		return err
	}
	pw, err := c.loadOrCreateAdmin(*resetPw)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(static))
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The UI ships inside the binary; make browsers revalidate so an update shows at once.
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))
	mux.HandleFunc("POST /api/login", c.handleLogin)
	mux.HandleFunc("POST /api/logout", c.handleLogout)
	mux.HandleFunc("GET /api/sessions", c.authSession(c.handleListSessions))
	mux.HandleFunc("DELETE /api/sessions/{id}", c.authSession(c.handleRevokeSession))
	mux.HandleFunc("POST /api/sessions/logout-all", c.authSession(c.handleLogoutAll))
	mux.HandleFunc("GET /api/tls", c.authSession(c.handleTLSInfo))
	mux.HandleFunc("POST /api/tls", c.authSession(c.handleSetTLS))
	mux.HandleFunc("POST /api/backup", c.authSession(c.handleBackup))
	mux.HandleFunc("POST /api/backup/schedule", c.authSession(c.handleBackupSchedule))
	mux.HandleFunc("GET /api/apitokens", c.authSession(c.handleListAPITokens))
	mux.HandleFunc("POST /api/apitokens", c.authSession(c.handleNewAPIToken))
	mux.HandleFunc("DELETE /api/apitokens/{id}", c.authSession(c.handleRevokeAPIToken))
	mux.HandleFunc("GET /api/state", c.auth(c.handleState))
	mux.HandleFunc("POST /api/tokens", c.auth(c.handleNewToken))
	mux.HandleFunc("GET /api/tokens/{id}", c.auth(c.handleTokenStatus))
	mux.HandleFunc("POST /api/nodes/{id}/settings", c.auth(c.handleNodeSettings))
	mux.HandleFunc("DELETE /api/nodes/{id}", c.auth(c.handleNodeDelete))
	mux.HandleFunc("POST /api/nodes/{id}/jobs", c.auth(c.handleNewJob))
	mux.HandleFunc("GET /api/jobs/{id}", c.auth(c.handleGetJob))
	mux.HandleFunc("POST /api/jobs/{id}/cancel", c.auth(c.handleCancelJob))
	mux.HandleFunc("POST /api/releases", c.auth(c.handleUploadRelease))
	mux.HandleFunc("DELETE /api/releases/{platform}", c.auth(c.handleDeleteRelease))
	mux.HandleFunc("POST /api/nodes/{id}/update", c.auth(c.handleUpdateNode))
	mux.HandleFunc("POST /api/update-all", c.auth(c.handleUpdateAll))
	mux.HandleFunc("POST /api/controller/update", c.auth(c.handleUpdateController))
	mux.HandleFunc("POST /api/controller/rollback", c.auth(c.handleRollbackController))
	mux.HandleFunc("POST /api/nodes/{id}/rollback", c.auth(c.handleRollbackNode))
	mux.HandleFunc("POST /api/nodes/{id}/restart", c.auth(c.handleRestartNode))
	mux.HandleFunc("GET /v1/releases/{platform}", c.handleServeRelease)
	mux.HandleFunc("GET /v1/runtime/{variant}", c.handleServeRuntime)
	c.toolchainRoutes(mux)
	mux.HandleFunc("GET /v1/models/{name}/files/{index}", c.handleServeModelFile)
	mux.HandleFunc("GET /api/settings", c.auth(c.handleGetSettings))
	mux.HandleFunc("POST /api/settings", c.auth(c.handleSaveSettings))
	mux.HandleFunc("GET /api/openrouter/models", c.auth(c.handleOpenRouterModels))
	mux.HandleFunc("POST /api/password", c.auth(c.handleChangePassword))
	mux.HandleFunc("GET /api/models", c.auth(c.handleListModels))
	mux.HandleFunc("POST /api/models", c.auth(c.handleAddModel))
	mux.HandleFunc("DELETE /api/models/{name}", c.auth(c.handleDeleteModel))
	mux.HandleFunc("POST /api/models/{name}/retry", c.auth(c.handleRetryModel))
	mux.HandleFunc("POST /api/models/{name}/store", c.auth(c.handleStoreModel))
	mux.HandleFunc("POST /api/models/{name}/unstore", c.auth(c.handleUnstoreModel))
	mux.HandleFunc("GET /api/hf/files", c.auth(c.handleHFFiles))
	mux.HandleFunc("POST /api/runtime/check", c.auth(c.handleCheckRuntime))
	mux.HandleFunc("POST /api/nodes/{id}/load", c.auth(c.handleLoadModel))
	mux.HandleFunc("POST /api/nodes/{id}/unload", c.auth(c.handleUnloadModel))
	mux.HandleFunc("DELETE /api/nodes/{id}/models/{name}", c.auth(c.handleRemoveNodeModel))
	mux.HandleFunc("GET /api/tasks", c.auth(c.handleListTasks))
	mux.HandleFunc("POST /api/tasks", c.auth(c.handleCreateTask))
	mux.HandleFunc("GET /api/tasks/{id}", c.auth(c.handleGetTask))
	mux.HandleFunc("DELETE /api/tasks/{id}", c.auth(c.handleDeleteTask))
	mux.HandleFunc("POST /api/tasks/{id}/approve", c.auth(c.handleApproveTask))
	mux.HandleFunc("POST /api/tasks/{id}/replan", c.auth(c.handleReplanTask))
	mux.HandleFunc("POST /api/tasks/{id}/messages", c.auth(c.handleTaskMessage))
	mux.HandleFunc("POST /api/tasks/{id}/answers", c.auth(c.handleTaskAnswers))
	mux.HandleFunc("POST /api/tasks/{id}/cancel", c.auth(c.handleCancelTask))
	mux.HandleFunc("POST /api/tasks/{id}/resume", c.auth(c.handleResumeTask))
	mux.HandleFunc("GET /api/tasks/{id}/events", c.auth(c.handleTaskEvents))
	mux.HandleFunc("GET /api/tasks/{id}/file", c.auth(c.handleTaskFile))
	mux.HandleFunc("GET /api/tasks/{id}/rounds", c.auth(c.handleTaskRounds))
	mux.HandleFunc("GET /api/tasks/{id}/changes", c.auth(c.handleTaskChanges))
	mux.HandleFunc("POST /api/tasks/{id}/undo", c.auth(c.handleUndoRound))
	mux.HandleFunc("GET /api/tasks/{id}/download", c.auth(c.handleDownloadTask))
	mux.HandleFunc("POST /v1/node/join", c.handleJoin)
	mux.HandleFunc("GET /v1/node/ws", c.handleNodeWS)

	srv := &http.Server{
		Addr:              *listen,
		Handler:           logRequests(mux),
		TLSConfig:         &tls.Config{GetCertificate: c.getCertificate, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
	}
	go c.offlineSweeper()
	go c.poolKeeper()
	go c.backupLoop()

	log.Printf("outpost controller %s listening on %s", Version, *listen)
	log.Printf("web UI:           %s", c.advertise)
	log.Printf("cert fingerprint: %s", fp)
	log.Printf("data dir:         %s", *data)
	if pw != "" {
		log.Printf("ADMIN PASSWORD:   %s   (shown once; rerun with --reset-password to change)", pw)
	}
	return srv.ListenAndServeTLS("", "")
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/state") || strings.HasPrefix(r.URL.Path, "/api/jobs/") && r.Method == "GET" {
			h.ServeHTTP(w, r) // polled constantly; don't log
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
			log.Printf("%s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
		}
		h.ServeHTTP(w, r)
	})
}

// ---- persistence ----

func (c *Controller) nodesPath() string { return filepath.Join(c.dataDir, "nodes.json") }

func (c *Controller) loadNodes() error {
	b, err := os.ReadFile(c.nodesPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []*NodeRec
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("nodes.json: %w", err)
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
	b, _ := json.MarshalIndent(list, "", "  ")
	tmp := c.nodesPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		_ = os.Rename(tmp, c.nodesPath())
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
	LendingTo   string        `json:"lending_to,omitempty"` // name of the node this one lends memory to
	PoolNames   []string      `json:"pool_names,omitempty"` // names of nodes lending memory to this one
	Update      *UpdateStatus `json:"update,omitempty"`
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
	c.mu.Lock()
	defer c.mu.Unlock()
	nodes := make([]nodeView, 0, len(c.nodes))
	for id, n := range c.nodes {
		v := nodeView{NodeRec: n, MaxJobs: c.effectiveMaxLocked(n), RunningJobs: c.runningLocked(id)}
		if nc, ok := c.conns[id]; ok {
			v.Online, v.Addr = true, nc.addr
		}
		if t, ok := c.lastSeen[id]; ok {
			t := t
			v.LastSeen = &t
		}
		v.Heartbeat = c.lastHB[id]
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
	writeJSON(w, 200, map[string]any{
		"version": Version, "platform": thisPlatform(), "controller_update_to": ctlUpdate,
		"controller_rollback_to": previousVersion(c.agentDir()),
		"fingerprint":            c.fingerprint, "advertise": c.advertise,
		"nodes": nodes, "jobs": jobs, "releases": releases, "ops": c.nodeOps,
		"has_claude_key": c.settings.AnthropicKeyEnc != "", "default_planner": c.settings.DefaultPlanner,
		"default_cap_usd": c.settings.DefaultCapUSD, "claude_model": c.settings.ClaudeModel,
		"has_openrouter_key": c.settings.OpenRouterKeyEnc != "", "openrouter_model": c.settings.OpenRouterModel,
		"openrouter_label": openRouterLabel(c.settings.OpenRouterModel),
	})
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
	b, _ := json.Marshal(JoinToken{URL: c.advertise, Fingerprint: c.fingerprintNow(), Secret: secret})
	tok := base64.RawURLEncoding.EncodeToString(b)
	writeJSON(w, 200, map[string]any{
		"id":      tokenID(secret),
		"token":   tok,
		"expires": exp,
		"linux":   "outpost install node --join " + tok,
		"windows": "outpost.exe install node --join " + tok,
		"macos":   "outpost node --join " + tok,
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

func (c *Controller) handleNodeSettings(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name        *string `json:"name"`
		ExecEnabled *bool   `json:"exec_enabled"`
		MaxJobs     *int    `json:"max_jobs_override"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad json"})
		return
	}
	c.mu.Lock()
	n, ok := c.nodes[id]
	if !ok {
		c.mu.Unlock()
		writeJSON(w, 404, map[string]string{"error": "no such node"})
		return
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
	c.saveNodesLocked()
	nc := c.conns[id]
	settings := NodeSettings{HeartbeatS: heartbeatSeconds, ExecEnabled: n.ExecEnabled}
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
	job.Spec, _ = json.Marshal(ExecSpec{Argv: argv, Stream: true})
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
	if !containsInt(hello.Protocol, ProtocolVersion) {
		_ = nc.send("node.welcome", Welcome{Accepted: false, Error: errf("unsupported_protocol", false, "controller speaks v%d", ProtocolVersion)})
		conn.Close(websocket.StatusPolicyViolation, "protocol")
		return
	}

	c.mu.Lock()
	if old := c.conns[n.ID]; old != nil {
		old.conn.Close(websocket.StatusGoingAway, "replaced by new connection")
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
	settings := NodeSettings{HeartbeatS: heartbeatSeconds, ExecEnabled: n.ExecEnabled}
	c.mu.Unlock()
	log.Printf("node %s (%s, %s/%s) connected from %s", n.ID, hello.Hostname, hello.OS, hello.Arch, r.RemoteAddr)
	c.clearRepin(n.ID)

	if err := nc.send("node.welcome", Welcome{Accepted: true, Protocol: ProtocolVersion, Settings: settings}); err != nil {
		return
	}

	defer func() {
		c.mu.Lock()
		if c.conns[n.ID] == nc {
			delete(c.conns, n.ID)
		}
		c.mu.Unlock()
		log.Printf("node %s disconnected", n.ID)
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

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func (c *Controller) handleNodeMessage(nodeID string, env *Envelope) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastSeen[nodeID] = time.Now()
	switch env.Type {
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
		}
	case "job.output":
		var o JobOutput
		if json.Unmarshal(env.Body, &o) != nil {
			return
		}
		taskLive.jobOutput(o)
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
			c.nodeOps[nodeID] = &nodeOpView{OpStatus: st, Updated: time.Now()}
		}
	case "node.update_status":
		var us UpdateStatus
		if json.Unmarshal(env.Body, &us) == nil {
			us.Updated = time.Now()
			prev := c.updates[nodeID]
			c.updates[nodeID] = &us
			if prev == nil || prev.State != us.State {
				log.Printf("node %s update %s: %s %s", nodeID, us.Version, us.State, us.Error)
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
		c.mu.Lock()
		now := time.Now()
		for id, nc := range c.conns {
			if now.Sub(c.lastSeen[id]) > 3*heartbeatSeconds*time.Second {
				log.Printf("node %s missed 3 heartbeats; closing", id)
				nc.conn.Close(websocket.StatusGoingAway, "heartbeat timeout")
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
}
