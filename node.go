package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	inlineMax       = 1 << 20  // files above this move as blobs (see node_artifacts.go)
	collectCap      = 64 << 20 // total inline-collected bytes per job
	tailMax         = 64 << 10
	workspaceTTL    = 30 * time.Minute
	disconnectGrace = 60 * time.Second
)

type NodeConfig struct {
	Controller  string `json:"controller"`
	Fingerprint string `json:"fingerprint"`
	NodeID      string `json:"node_id"`
	NodeKey     string `json:"node_key"`
}

type Node struct {
	dataDir  string
	workRoot string
	cfg      NodeConfig
	fpMu     sync.RWMutex // guards cfg.Fingerprint (changes on a certificate swap)

	mu          sync.Mutex
	execEnabled bool
	hbSeconds   int
	jobs        map[string]context.CancelFunc
	wsLastUsed  map[string]time.Time
	conn        *websocket.Conn
	connMu      sync.Mutex
	stats       *statSampler
	updating    bool
	args        []string
	llama       *llamaMgr
	rpc         *rpcMgr
	paused      bool // no new work (tray icon); kept in <data>/paused
	online      bool // connected and welcomed by the controller
	tools       toolCache
	peer        peerState
	artifacts   map[string]string // "<job>/<path>" -> file on disk, served to the controller
	busyModels  map[string]int    // models being written right now; cleanup skips them
}

func (n *Node) agentDir() string { return filepath.Join(n.dataDir, "agent") }

func writeNodeConfig(path string, cfg NodeConfig) error {
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(path, b, 0o600)
}

func runNode(args []string) error {
	fsf := flag.NewFlagSet("node", flag.ExitOnError)
	join := fsf.String("join", "", "join token from the controller UI (first run only)")
	data := fsf.String("data", defaultDataDir("node"), "data directory (config, models, work)")
	name := fsf.String("name", "", "display name to register with (default: hostname)")
	_ = fsf.Parse(args)

	n := &Node{dataDir: *data, workRoot: filepath.Join(*data, "work"), hbSeconds: heartbeatSeconds,
		jobs: map[string]context.CancelFunc{}, wsLastUsed: map[string]time.Time{}, stats: newStatSampler(),
		args: stripJoin(os.Args[1:])}
	n.llama = newLlamaMgr(n)
	n.rpc = newRPCMgr(n)
	for _, d := range []string{*data, n.workRoot, filepath.Join(*data, "models"), n.agentDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	cfgPath := filepath.Join(*data, "node.json")

	if *join != "" {
		cfg, err := doJoin(*join, *name)
		if err != nil {
			return err
		}
		if err := writeNodeConfig(cfgPath, cfg); err != nil {
			return err
		}
		log.Printf("joined controller %s as %s; config saved to %s", cfg.Controller, cfg.NodeID, cfgPath)
	}
	launchNewest(n.agentDir(), n.args) // switch to an installed update, if newer
	// One node per data folder. During an update or restart the old process
	// may take a moment to exit, so wait for it rather than run alongside it.
	unlock, err := acquireNodeLock(*data, 30*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("no node config at %s; run once with --join TOKEN (create one in the controller UI)", cfgPath)
	}
	if err := json.Unmarshal(b, &n.cfg); err != nil {
		return fmt.Errorf("%s: %w", cfgPath, err)
	}

	if _, err := os.Stat(pausedPath(n.dataDir)); err == nil {
		n.paused = true
	}
	n.startControl()
	freed := n.sweepWorkRoot()
	log.Printf("outpost node %s starting; node id %s; work root %s (startup sweep freed %d bytes)", Version, n.cfg.NodeID, n.workRoot, freed)
	go n.workspaceJanitor()
	go n.autoload()
	go n.llama.idleLoop(context.Background())
	go n.autostartRPC()
	n.startPeerServer()

	backoff := time.Second
	lostSince := time.Now()
	swept := false
	for {
		welcomed, err := n.session()
		if errors.Is(err, errRemoved) {
			log.Printf("this node was removed in the controller UI; wiping work root and config")
			n.cancelAll()
			n.llama.stop()
			n.rpc.stop(true)
			time.Sleep(time.Second)
			n.sweepWorkRoot()
			_ = os.Remove(cfgPath)
			return nil
		}
		if welcomed {
			lostSince, swept, backoff = time.Now(), false, time.Second
		}
		log.Printf("not connected: %v; retrying in %s", err, backoff)
		time.Sleep(backoff)
		if !swept && time.Since(lostSince) > disconnectGrace {
			if n.runningCount() > 0 || n.workspaceCount() > 0 {
				log.Printf("offline for %s; cancelling jobs and closing all workspaces", disconnectGrace)
				n.cancelAll()
				time.Sleep(2 * time.Second)
				n.sweepWorkRoot()
			}
			swept = true
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (n *Node) workspaceCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.wsLastUsed)
}

var errRemoved = errors.New("node removed")

func doJoin(token, name string) (NodeConfig, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return NodeConfig{}, fmt.Errorf("join token is not valid base64: %w", err)
	}
	var jt JoinToken
	if err := json.Unmarshal(raw, &jt); err != nil || jt.URL == "" || jt.Fingerprint == "" {
		return NodeConfig{}, errors.New("join token is malformed")
	}
	if name == "" {
		name, _ = os.Hostname()
	}
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: pinnedTLS(jt.Fingerprint)}}
	body, _ := json.Marshal(JoinRequest{Secret: jt.Secret, Hostname: name})
	resp, err := client.Post(strings.TrimRight(jt.URL, "/")+"/v1/node/join", "application/json", bytes.NewReader(body))
	if err != nil {
		return NodeConfig{}, fmt.Errorf("cannot reach controller at %s: %w", jt.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var e ErrInfo
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return NodeConfig{}, fmt.Errorf("join refused (%d): %s", resp.StatusCode, e.Message)
	}
	var jr JoinResponse
	if err := json.NewDecoder(resp.Body).Decode(&jr); err != nil {
		return NodeConfig{}, err
	}
	return NodeConfig{Controller: jt.URL, Fingerprint: jt.Fingerprint, NodeID: jr.NodeID, NodeKey: jr.NodeKey}, nil
}

// pin is the controller certificate fingerprint to trust. It is re-read from
// node.json each time, so `outpost repin` takes effect without a restart.
func (n *Node) pin() string {
	n.fpMu.Lock()
	defer n.fpMu.Unlock()
	if b, err := os.ReadFile(filepath.Join(n.dataDir, "node.json")); err == nil {
		var c NodeConfig
		if json.Unmarshal(b, &c) == nil && c.Fingerprint != "" {
			n.cfg.Fingerprint = c.Fingerprint
		}
	}
	return n.cfg.Fingerprint
}

func (n *Node) setPin(fp string) error {
	n.fpMu.Lock()
	defer n.fpMu.Unlock()
	n.cfg.Fingerprint = fp
	return writeNodeConfig(filepath.Join(n.dataDir, "node.json"), n.cfg)
}

func (n *Node) send(typ string, body any) error {
	b, err := newEnvelope(typ, body)
	if err != nil {
		return err
	}
	n.connMu.Lock()
	defer n.connMu.Unlock()
	if n.conn == nil {
		return errors.New("not connected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return n.conn.Write(ctx, websocket.MessageText, b)
}

func (n *Node) session() (bool, error) {
	welcomed := false
	err := n.sessionInner(&welcomed)
	return welcomed, err
}

func (n *Node) sessionInner(welcomed *bool) error {
	wsURL := strings.Replace(strings.TrimRight(n.cfg.Controller, "/"), "https://", "wss://", 1) + "/v1/node/ws"
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: pinnedTLS(n.pin())}}
	dctx, dcancel := context.WithTimeout(context.Background(), 20*time.Second)
	conn, resp, err := websocket.Dial(dctx, wsURL, &websocket.DialOptions{
		HTTPClient: hc,
		HTTPHeader: http.Header{"Authorization": {"Bearer " + n.cfg.NodeKey}},
	})
	dcancel()
	if err != nil {
		if resp != nil && resp.StatusCode == 401 {
			return fmt.Errorf("controller rejected this node's key (removed in UI?): %w", err)
		}
		return err
	}
	conn.SetReadLimit(256 << 20)
	n.connMu.Lock()
	n.conn = conn
	n.connMu.Unlock()
	defer func() {
		n.connMu.Lock()
		n.conn = nil
		n.connMu.Unlock()
		conn.CloseNow()
	}()

	if err := n.send("node.hello", n.hello()); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, msg, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	var env Envelope
	var wel Welcome
	if json.Unmarshal(msg, &env) != nil || env.Type != "node.welcome" || json.Unmarshal(env.Body, &wel) != nil {
		return errors.New("expected node.welcome")
	}
	if !wel.Accepted {
		return fmt.Errorf("controller refused: %v", wel.Error)
	}
	*welcomed = true
	n.setOnline(true)
	defer n.setOnline(false)
	n.applySettings(wel.Settings)
	log.Printf("connected to %s (exec %s)", n.cfg.Controller, onOff(wel.Settings.ExecEnabled))

	go n.heartbeatLoop(ctx)
	go n.toolsLoop(ctx)

	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		if json.Unmarshal(msg, &env) != nil {
			continue
		}
		switch env.Type {
		case "job.dispatch":
			var jd JobDispatch
			if err := json.Unmarshal(env.Body, &jd); err != nil {
				continue
			}
			go n.runJob(jd)
		case "job.cancel":
			var jc JobCancel
			if json.Unmarshal(env.Body, &jc) == nil {
				n.mu.Lock()
				if c := n.jobs[jc.JobID]; c != nil {
					c()
				}
				n.mu.Unlock()
			}
		case "node.settings":
			var s NodeSettings
			if json.Unmarshal(env.Body, &s) == nil {
				n.applySettings(s)
				log.Printf("settings updated: exec %s", onOff(s.ExecEnabled))
			}
		case "ws.close":
			var wc WSClose
			if json.Unmarshal(env.Body, &wc) == nil {
				n.closeWorkspace(wc.WorkspaceID, "closed")
			}
		case "runtime.install":
			var ri RuntimeInstall
			if json.Unmarshal(env.Body, &ri) == nil {
				go n.installRuntime(ri)
			}
		case "toolchain.install":
			var ti ToolInstall
			if json.Unmarshal(env.Body, &ti) == nil {
				go n.installToolchain(ti)
			}
		case "model.push":
			var mp ModelPush
			if json.Unmarshal(env.Body, &mp) == nil {
				go n.pushModel(mp)
			}
		case "peer.grant":
			var pg PeerGrant
			if json.Unmarshal(env.Body, &pg) == nil {
				n.peerGrant(pg)
			}
		case "model.fetch":
			var mf ModelFetch
			if json.Unmarshal(env.Body, &mf) == nil {
				go n.fetchModel(mf)
			}
		case "model.load":
			var ml ModelLoad
			if json.Unmarshal(env.Body, &ml) == nil {
				go func() {
					n.llama.load(ml)
					if m, _, _, _ := n.llama.status(); m == ml.Name {
						n.saveAutoload(&ml)
					}
				}()
			}
		case "rpc.start":
			var rs RPCStart
			if json.Unmarshal(env.Body, &rs) == nil {
				go n.rpc.start(rs)
			}
		case "rpc.stop":
			go func() {
				n.rpc.stop(true)
				n.pushHeartbeat()
				n.opStatus("rpc", "serve", "done", 0, 0, "")
			}()
		case "model.unload":
			go func() {
				n.llama.stop()
				n.saveAutoload(nil)
				n.opStatus("model.unload", "", "done", 0, 0, "")
				n.resendHello()
			}()
		case "model.remove":
			var mr ModelRemove
			if json.Unmarshal(env.Body, &mr) == nil {
				go n.removeModel(mr.Name)
			}
		case "model.cleanup":
			var mc ModelCleanup
			if json.Unmarshal(env.Body, &mc) == nil {
				go n.cleanupModels(mc)
			}
		case "node.restart":
			go n.restartSelf()
		case "node.rollback":
			var rb NodeRollback
			if json.Unmarshal(env.Body, &rb) == nil {
				go n.applyRollback(rb.Version)
			}
		case "node.update":
			var u NodeUpdate
			if json.Unmarshal(env.Body, &u) == nil {
				go n.applyUpdate(u)
			}
		case "node.repin":
			var rp struct {
				Fingerprint string `json:"fingerprint"`
			}
			if json.Unmarshal(env.Body, &rp) == nil && len(rp.Fingerprint) == 64 {
				if err := n.setPin(strings.ToLower(rp.Fingerprint)); err != nil {
					log.Printf("repin: saving the new fingerprint: %v", err)
				} else {
					log.Printf("controller certificate changed; now pinning %s", rp.Fingerprint)
					_ = n.send("node.repin_ack", rp)
				}
			}
		case "node.remove":
			return errRemoved
		}
	}
}

func onOff(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

func (n *Node) applySettings(s NodeSettings) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.execEnabled = s.ExecEnabled
	if s.HeartbeatS > 0 {
		n.hbSeconds = s.HeartbeatS
	}
	n.llama.setIdleAfter(time.Duration(s.IdleUnloadS) * time.Second)
}

func (n *Node) runningIDs() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	ids := []string{}
	for id := range n.jobs {
		ids = append(ids, id)
	}
	return ids
}

func (n *Node) runningCount() int { return len(n.runningIDs()) }

func (n *Node) cancelAll() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, c := range n.jobs {
		c()
	}
}

func (n *Node) hello() Hello {
	host, _ := os.Hostname()
	hw := detectHardware()
	n.mu.Lock()
	execOn := n.execEnabled
	n.mu.Unlock()
	h := Hello{
		AgentVersion: Version, Protocol: supportedProtocols(), NodeID: n.cfg.NodeID,
		Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH,
		CPU: hw.CPU, RAMBytes: hw.RAMBytes, GPUs: hw.GPUs, Backends: hw.Backends,
		Models: n.listModels(), WorkFreeBytes: diskFree(n.workRoot), ModelsFreeBytes: diskFree(filepath.Join(n.dataDir, "models")),
		MaxConcurrentJobs: autoMaxJobs(hw), ExecEnabled: execOn, RunningJobs: n.runningIDs(),
		VulkanAvailable: vulkanAvailable(),
		RollbackTo:      previousVersion(n.agentDir()),
		Paused:          n.isPaused(),
		Tools:           n.toolInventory(false), Shell: nodeShell(),
	}
	if rt := n.currentRuntime(); rt != nil {
		h.Runtime = &rt.RuntimeInfo
	}
	h.LoadedModel, _, _, _ = n.llama.status()
	h.Peer = n.peerInfo()
	return h
}

func autoMaxJobs(hw Hardware) int {
	switch {
	case len(hw.GPUs) > 0:
		return 2
	case hw.CPU.Threads >= 32 && hw.RAMBytes >= 64<<30:
		return 4
	case hw.CPU.Threads >= 16:
		return 2
	default:
		return 1
	}
}

// pushHeartbeat sends a heartbeat now, so the controller sees model state
// changes (loading, ready, stopped) without waiting for the next interval.
func (n *Node) pushHeartbeat() { _ = n.sendHeartbeat() }

func (n *Node) sendHeartbeat() error { return n.send("node.heartbeat", n.buildHeartbeat()) }

func (n *Node) buildHeartbeat() Heartbeat {
	n.mu.Lock()
	wsCount := len(n.wsLastUsed)
	n.mu.Unlock()
	hb := n.stats.sample()
	hb.RunningJobs = n.runningIDs()
	hb.Workspaces = wsCount
	hb.LoadedModel, hb.InferenceRdy, hb.InferenceBusy, hb.LlamaError = n.llama.status()
	if cold := n.llama.coldModel(); cold != "" {
		// Unloaded for idleness: still this node's model, available, but cold.
		hb.LoadedModel, hb.InferenceRdy, hb.IdleUnloaded = cold, true, true
	}
	hb.RPCEndpoint = n.rpc.current()
	hb.Paused = n.isPaused()
	return hb
}

func (n *Node) heartbeatLoop(ctx context.Context) {
	for {
		n.mu.Lock()
		every := time.Duration(n.hbSeconds) * time.Second
		n.mu.Unlock()
		if err := n.sendHeartbeat(); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// ---- workspaces and cleanup ----

func (n *Node) wsDir(id string) string { return filepath.Join(n.workRoot, id) }

func validWSID(id string) bool {
	if id == "" || len(id) > 80 {
		return false
	}
	for _, r := range id {
		if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func dirSize(p string) int64 {
	var total int64
	_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, e := d.Info(); e == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func (n *Node) closeWorkspace(id, reason string) {
	if !validWSID(id) {
		return
	}
	dir := n.wsDir(id)
	size := dirSize(dir)
	err := os.RemoveAll(dir)
	n.dropArtifacts(strings.TrimPrefix(id, "ws_"))
	n.mu.Lock()
	delete(n.wsLastUsed, id)
	n.mu.Unlock()
	if err != nil {
		log.Printf("cleanup of %s failed: %v", id, err)
		return
	}
	_ = n.send("ws.closed", WSClosed{WorkspaceID: id, Reason: reason, BytesFreed: size})
}

// sweepWorkRoot deletes everything under the work root (startup, removal, long disconnect).
func (n *Node) sweepWorkRoot() int64 {
	var freed int64
	entries, _ := os.ReadDir(n.workRoot)
	for _, e := range entries {
		p := filepath.Join(n.workRoot, e.Name())
		freed += dirSize(p)
		_ = os.RemoveAll(p)
	}
	n.mu.Lock()
	n.wsLastUsed = map[string]time.Time{}
	n.artifacts = nil
	n.mu.Unlock()
	return freed
}

func (n *Node) workspaceJanitor() {
	for range time.Tick(time.Minute) {
		n.mu.Lock()
		var idle []string
		for id, t := range n.wsLastUsed {
			if time.Since(t) > workspaceTTL {
				idle = append(idle, id)
			}
		}
		n.mu.Unlock()
		for _, id := range idle {
			n.closeWorkspace(id, "ttl")
		}
	}
}

// safeJoin resolves rel inside root and refuses anything that escapes it.
func safeJoin(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "\x00") {
		return "", errf("path_denied", false, "bad path %q", rel)
	}
	p := filepath.Join(root, filepath.FromSlash(rel))
	r, err := filepath.Rel(root, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", errf("path_denied", false, "path %q leaves the workspace", rel)
	}
	return p, nil
}

// ---- jobs ----

func (n *Node) runJob(jd JobDispatch) {
	started := time.Now().UTC()
	res := JobResult{JobID: jd.JobID, StartedAt: started, Files: []FileEntry{}}
	private := jd.WorkspaceID == ""
	wsID := jd.WorkspaceID
	if private {
		wsID = "ws_" + jd.JobID
	}

	timeout := time.Duration(jd.TimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	n.mu.Lock()
	n.jobs[jd.JobID] = cancel
	n.mu.Unlock()

	finish := func() {
		res.EndedAt = time.Now().UTC()
		if err := n.send("job.result", res); err != nil {
			log.Printf("job %s: could not send result: %v", jd.JobID, err)
		}
		n.mu.Lock()
		delete(n.jobs, jd.JobID)
		n.mu.Unlock()
		cancel()
		if private {
			n.closeWorkspace(wsID, "job_ended")
		}
		log.Printf("job %s %s", jd.JobID, res.Status)
	}
	fail := func(e *ErrInfo) {
		res.Status, res.Error = "failed", e
		finish()
	}

	log.Printf("job %s (%s) received", jd.JobID, jd.Kind)
	if n.isPaused() {
		fail(errf("busy", true, "node is paused (tick Accept new work in its tray icon)"))
		return
	}
	if jd.Kind == "inference" {
		private = false // inference uses no workspace
		n.mu.Lock()
		updating := n.updating
		n.mu.Unlock()
		if updating {
			fail(errf("busy", true, "node is installing an update"))
			return
		}
		n.runInference(ctx, jd, &res)
		finish()
		return
	}
	if !validWSID(wsID) {
		fail(errf("bad_request", false, "invalid workspace id"))
		return
	}
	if jd.Kind != "exec" {
		fail(errf("bad_request", false, "job kind %q is not supported by this agent version yet", jd.Kind))
		return
	}
	n.mu.Lock()
	execOn, updating := n.execEnabled, n.updating
	n.mu.Unlock()
	if updating {
		fail(errf("busy", true, "node is installing an update"))
		return
	}
	if !execOn {
		fail(errf("exec_disabled", false, "exec is disabled on this node"))
		return
	}

	dir := n.wsDir(wsID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fail(errf("disk_full", true, "%v", err))
		return
	}
	n.mu.Lock()
	n.wsLastUsed[wsID] = time.Now()
	n.mu.Unlock()

	if e := n.writeInputs(dir, jd.JobID, jd.Inputs); e != nil {
		fail(e)
		return
	}

	var spec ExecSpec
	if err := json.Unmarshal(jd.Spec, &spec); err != nil || len(spec.Argv) == 0 {
		fail(errf("bad_request", false, "exec spec needs argv"))
		return
	}
	if _, err := exec.LookPath(spec.Argv[0]); err != nil {
		fail(errf("exec_not_found", false, "%s not found on this node", spec.Argv[0]))
		return
	}

	cmd := exec.CommandContext(ctx, spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = dir
	cmd.Env = n.execEnv()
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	setProcGroup(cmd)
	cmd.WaitDelay = 5 * time.Second

	sw := newStreamer(n, jd.JobID, spec.Stream)
	stdoutTail, stderrTail := &tailBuf{}, &tailBuf{}
	cmd.Stdout = io.MultiWriter(stdoutTail, sw.writer("stdout"))
	cmd.Stderr = io.MultiWriter(stderrTail, sw.writer("stderr"))

	runErr := cmd.Run()
	sw.close()

	out := ExecOutput{ExitCode: -1, StdoutTail: stdoutTail.String(), StderrTail: stderrTail.String()}
	if cmd.ProcessState != nil {
		out.ExitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.Status = "timed_out"
	case errors.Is(ctx.Err(), context.Canceled):
		res.Status = "cancelled"
	case runErr != nil && cmd.ProcessState == nil:
		res.Status, res.Error = "failed", errf("exec_failed", false, "%v", runErr)
	default:
		res.Status = "succeeded" // a non-zero exit code is still a successful job
	}
	res.Output, _ = json.Marshal(out)

	files, e := n.collectFiles(dir, jd.JobID, jd.Collect)
	res.Files = files
	for _, f := range files {
		if f.Blob { // the controller fetches these, then closes the workspace
			private = false
			break
		}
	}
	if e != nil && res.Status == "succeeded" {
		res.Status, res.Error = "failed", e
	}
	finish()
}

// tailBuf keeps the last tailMax bytes written.
type tailBuf struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > tailMax {
		t.buf = t.buf[len(t.buf)-tailMax:]
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// streamer batches output into job.output messages every 100 ms or 16 KiB.
type streamer struct {
	n      *Node
	jobID  string
	on     bool
	mu     sync.Mutex
	seq    int
	cur    string
	buf    []byte
	tok    int
	done   chan struct{}
	closed sync.Once
}

func newStreamer(n *Node, jobID string, on bool) *streamer {
	s := &streamer{n: n, jobID: jobID, on: on, done: make(chan struct{})}
	if on {
		go func() {
			t := time.NewTicker(100 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					s.flush()
				case <-s.done:
					return
				}
			}
		}()
	}
	return s
}

type streamWriter struct {
	s      *streamer
	stream string
}

func (s *streamer) writer(stream string) io.Writer { return streamWriter{s, stream} }

func (w streamWriter) Write(p []byte) (int, error) {
	s := w.s
	if !s.on {
		return len(p), nil
	}
	s.mu.Lock()
	if s.cur != w.stream && len(s.buf) > 0 {
		s.flushLocked()
	}
	s.cur = w.stream
	s.buf = append(s.buf, p...)
	if len(s.buf) >= 16<<10 {
		s.flushLocked()
	}
	s.mu.Unlock()
	return len(p), nil
}

// token counts one model token toward the next flush.
func (s *streamer) token() {
	s.mu.Lock()
	s.tok++
	s.mu.Unlock()
}

func (s *streamer) flush() {
	s.mu.Lock()
	s.flushLocked()
	s.mu.Unlock()
}

func (s *streamer) flushLocked() {
	if len(s.buf) == 0 {
		return
	}
	_ = s.n.send("job.output", JobOutput{JobID: s.jobID, Seq: s.seq, Stream: s.cur, Data: string(s.buf), Tokens: s.tok})
	s.seq++
	s.tok = 0
	s.buf = s.buf[:0]
}

func (s *streamer) close() {
	s.closed.Do(func() {
		close(s.done)
		s.flush()
	})
}
