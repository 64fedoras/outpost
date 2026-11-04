package main

// Local control for the node, used by the tray icon (`outpost tray`).
//
// The node listens on 127.0.0.1 only, on a random port, and writes the port
// and a random token to <data>/control.json (readable only by the node's
// user). Every request must carry that token, so other local users and web
// pages cannot drive it.
//
//   GET  /status        what the tray shows
//   POST /pause|resume  stop or allow new work being sent to this node
//   POST /unload        unload the model now (it loads again on the next request)
//   POST /quit          stop the node (a service stays stopped until started)
//   GET  /join          small page for joining a different controller
//   POST /join          {token, name}: join it, then restart on the new config

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// exitQuit is the exit code for a deliberate quit; the systemd unit does not
// restart on it (RestartPreventExitStatus).
const exitQuit = 75

// exitQuitService is what a node run by the Windows service exits with on Quit
// (75 means "relaunch after an update" there).
const exitQuitService = 76

type controlFile struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

func controlPath(dataDir string) string { return filepath.Join(dataDir, "control.json") }
func pausedPath(dataDir string) string  { return filepath.Join(dataDir, "paused") }

func (n *Node) isPaused() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.paused
}

// setPaused stops or allows new work. It is remembered across restarts.
func (n *Node) setPaused(p bool) {
	n.mu.Lock()
	changed := n.paused != p
	n.paused = p
	n.mu.Unlock()
	if p {
		_ = os.WriteFile(pausedPath(n.dataDir), []byte("paused from the tray icon\n"), 0o600)
	} else {
		_ = os.Remove(pausedPath(n.dataDir))
	}
	if changed {
		log.Printf("new work %s from the tray icon", map[bool]string{true: "paused", false: "resumed"}[p])
		n.pushHeartbeat()
		n.resendHello()
	}
}

func (n *Node) isOnline() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.online
}

func (n *Node) setOnline(v bool) {
	n.mu.Lock()
	n.online = v
	n.mu.Unlock()
}

type controlStatus struct {
	Version     string `json:"version"`
	NodeID      string `json:"node_id"`
	Name        string `json:"name"`
	Controller  string `json:"controller"`
	Online      bool   `json:"online"`
	Paused      bool   `json:"paused"`
	Model       string `json:"model"`
	ModelReady  bool   `json:"model_ready"`
	ModelCold   string `json:"model_cold,omitempty"` // unloaded (idle or from the tray); loads on the next request
	Pooled      bool   `json:"pooled,omitempty"`     // the loaded model uses other nodes' memory
	RunningJobs int    `json:"running_jobs"`
	Lending     string `json:"lending,omitempty"`
	Service     bool   `json:"service"` // started by systemd
}

func (n *Node) startControl() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Printf("tray control unavailable: %v", err)
		return
	}
	cf := controlFile{Port: ln.Addr().(*net.TCPAddr).Port, Token: randHex(16), PID: os.Getpid()}
	b, _ := json.Marshal(cf)
	if err := os.WriteFile(controlPath(n.dataDir), b, 0o600); err != nil {
		log.Printf("tray control unavailable: %v", err)
		ln.Close()
		return
	}
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			t := r.Header.Get("X-Outpost-Token")
			if t == "" {
				t = r.URL.Query().Get("t")
			}
			if subtle.ConstantTimeCompare([]byte(t), []byte(cf.Token)) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			// Only this machine's browser or tray, never another site.
			if o := r.Header.Get("Origin"); o != "" && !strings.HasPrefix(o, "http://127.0.0.1:") {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			h(w, r)
		}
	}
	mux.HandleFunc("GET /status", auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, n.controlStatus())
	}))
	mux.HandleFunc("POST /pause", auth(func(w http.ResponseWriter, r *http.Request) {
		n.setPaused(true)
		writeJSON(w, 200, n.controlStatus())
	}))
	mux.HandleFunc("POST /resume", auth(func(w http.ResponseWriter, r *http.Request) {
		n.setPaused(false)
		writeJSON(w, 200, n.controlStatus())
	}))
	mux.HandleFunc("POST /unload", auth(func(w http.ResponseWriter, r *http.Request) {
		if err := n.unloadFromTray(); err != nil {
			writeJSON(w, 409, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, n.controlStatus())
	}))
	mux.HandleFunc("POST /quit", auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"ok": true})
		go n.quit()
	}))
	mux.HandleFunc("GET /join", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, joinPage(n.controlStatus(), cf.Token))
	}))
	mux.HandleFunc("POST /join", auth(n.handleControlJoin))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
}

func (n *Node) controlStatus() controlStatus {
	host, _ := os.Hostname()
	model, ready, _, _ := n.llama.status()
	st := controlStatus{Version: Version, NodeID: n.cfg.NodeID, Name: host, Controller: n.cfg.Controller,
		Online: n.isOnline(), Paused: n.isPaused(), Model: model, ModelReady: ready, RunningJobs: n.runningCount(),
		Service: os.Getenv("INVOCATION_ID") != "" || os.Getenv("OUTPOST_SERVICE") == "1"}
	if ep := n.rpc.current(); ep != "" {
		st.Lending = ep
	}
	st.ModelCold = n.llama.coldModel()
	n.llama.mu.Lock()
	st.Pooled = n.llama.cmd != nil && len(n.llama.opts.RPC) > 0
	n.llama.mu.Unlock()
	return st
}

var errUnloadJobs = errors.New("a job is running on this node; try again when it is idle")

// unloadFromTray frees the model's memory now, like an idle unload: the node keeps
// taking work and the model loads again when the next request needs it. Running
// work is never interrupted; the tray is told why instead.
func (n *Node) unloadFromTray() error {
	if n.runningCount() > 0 {
		return errUnloadJobs
	}
	_, err := n.llama.unloadNow("unloaded from the tray icon")
	return err
}

// quit stops work cleanly and exits. A systemd service stays stopped until
// the tray, `systemctl start outpost-node` or the next boot starts it.
func (n *Node) quit() {
	log.Printf("quit requested from the tray icon")
	n.cancelAll()
	if n.runningCount() > 0 {
		time.Sleep(2 * time.Second)
	}
	n.llama.stop()
	n.rpc.stop(false)
	n.connMu.Lock()
	if n.conn != nil {
		n.conn.CloseNow()
	}
	n.connMu.Unlock()
	n.sweepWorkRoot()
	os.Remove(controlPath(n.dataDir))
	if os.Getenv("OUTPOST_SERVICE") == "1" {
		os.Exit(exitQuitService)
	}
	os.Exit(exitQuit)
}

// handleControlJoin joins a different controller with a new join code. The
// old config is kept as node.json.prev; models stay on disk.
func (n *Node) handleControlJoin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || strings.TrimSpace(req.Token) == "" {
		writeJSON(w, 400, map[string]string{"error": "paste a join code from the controller's Add node dialog"})
		return
	}
	cfg, err := doJoin(req.Token, strings.TrimSpace(req.Name))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	cfgPath := filepath.Join(n.dataDir, "node.json")
	if old, err := os.ReadFile(cfgPath); err == nil {
		_ = os.WriteFile(cfgPath+".prev", old, 0o600)
	}
	if err := writeNodeConfig(cfgPath, cfg); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("joined %s as %s from the tray icon; restarting on the new config", cfg.Controller, cfg.NodeID)
	// The new controller decides what to load and lend.
	n.saveAutoload(nil)
	n.rpc.stop(true)
	writeJSON(w, 200, map[string]string{"controller": cfg.Controller, "node_id": cfg.NodeID})
	go func() {
		time.Sleep(500 * time.Millisecond)
		n.restartSelfFor("the tray icon (new controller)")
	}()
}

func joinPage(st controlStatus, token string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Outpost: join a controller</title>
<style>
:root{--bg:#f6f5f1;--card:#fff;--ink:#1d1d1f;--muted:#6b6b70;--line:#e3e1db;--accent:#2f6fdf;--bad:#c0392b;--ok:#1e7d4f}
@media (prefers-color-scheme:dark){:root{--bg:#121315;--card:#1b1c1f;--ink:#ececee;--muted:#9a9aa2;--line:#2c2d31;--accent:#5b8ef0;--bad:#e06b5f;--ok:#4cc38a}}
body{margin:0;background:var(--bg);color:var(--ink);font:15px/1.5 system-ui,sans-serif}
main{max-width:560px;margin:48px auto;padding:0 16px}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:20px 22px}
h1{font-size:19px;margin:0 0 6px}p{margin:6px 0 14px;color:var(--muted)}
label{display:block;font-size:13px;color:var(--muted);margin:12px 0 4px}
textarea,input{width:100%;box-sizing:border-box;border:1px solid var(--line);border-radius:8px;padding:9px 10px;background:var(--bg);color:var(--ink);font:13px ui-monospace,monospace}
textarea{height:96px;resize:vertical}
button{margin-top:16px;background:var(--accent);color:#fff;border:0;border-radius:8px;padding:9px 16px;font:inherit;cursor:pointer}
button:disabled{opacity:.6;cursor:default}
.msg{margin-top:14px;font-size:14px}.err{color:var(--bad)}.ok{color:var(--ok)}
code{font-size:13px}
</style></head><body><main><div class="card">
<h1>Join a different controller</h1>
<p>This node (<b>` + html.EscapeString(st.Name) + `</b>) is joined to <code>` + html.EscapeString(st.Controller) + `</code>.
Paste a join code from the other controller's <b>Nodes → Add node</b> dialog. Running work is cancelled, models stay on this machine,
and the old settings are kept as <code>node.json.prev</code>.</p>
<label for="tok">Join code (the long text after <code>--join</code>)</label>
<textarea id="tok" spellcheck="false" autofocus></textarea>
<label for="name">Name on the new controller (optional)</label>
<input id="name" value="` + html.EscapeString(st.Name) + `">
<button id="go">Join</button>
<div id="msg" class="msg"></div>
</div></main>
<script>
const go = document.getElementById('go'), msg = document.getElementById('msg');
go.onclick = async () => {
  let tok = document.getElementById('tok').value.trim();
  const m = tok.match(/--join\s+(\S+)/); if (m) tok = m[1];
  if (!tok) { msg.className = 'msg err'; msg.textContent = 'Paste the join code first.'; return; }
  go.disabled = true; msg.className = 'msg'; msg.textContent = 'Joining…';
  try {
    const r = await fetch('/join?t=` + token + `', {method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({token: tok, name: document.getElementById('name').value})});
    const b = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(b.error || r.statusText);
    msg.className = 'msg ok';
    msg.textContent = 'Joined ' + b.controller + '. The node is restarting and connects in a few seconds; you can close this page.';
  } catch (e) { msg.className = 'msg err'; msg.textContent = e.message; go.disabled = false; }
};
</script></body></html>`
}

// runRepin trusts a new controller certificate fingerprint on a node that was
// offline when the certificate was replaced. Check the fingerprint in the
// controller's Settings page first.
func runRepin(args []string) error {
	fsf := flag.NewFlagSet("repin", flag.ExitOnError)
	fp := fsf.String("fingerprint", "", "SHA-256 fingerprint shown in the controller's Settings")
	data := fsf.String("data", defaultDataDir("node"), "the node's data directory")
	_ = fsf.Parse(args)
	f := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(*fp), ":", ""))
	if len(f) != 64 {
		return errors.New("--fingerprint must be the 64-character SHA-256 fingerprint")
	}
	p := filepath.Join(*data, "node.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("no node config in %s (use --data DIR)", *data)
	}
	var cfg NodeConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return err
	}
	cfg.Fingerprint = f
	if err := writeNodeConfig(p, cfg); err != nil {
		return err
	}
	fmt.Println("Now trusting", f, "- the node picks it up on its next connection attempt.")
	return nil
}
