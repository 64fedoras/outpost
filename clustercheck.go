package main

// Check cluster: one button on the Nodes page that runs a short model call,
// a command and a connection to the file-sharing port on every online node,
// and checks disk space, clock offset and version. It says what is broken and
// how to fix it. Results live in memory only (the last run).

import (
	"context"
	"crypto/tls"
	"fmt"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type checkStep struct {
	State string `json:"state"` // pending | running | ok | warn | fail | skip
	Text  string `json:"text"`
}

type checkRow struct {
	ID      string               `json:"id"`
	Name    string               `json:"name"`
	Online  bool                 `json:"online"`
	Offline string               `json:"offline,omitempty"` // "offline since 02:14"
	Steps   map[string]checkStep `json:"steps"`             // model, command, port, disk, clock, version
	Notes   []checkNote          `json:"notes,omitempty"`
}

type checkNote struct {
	Level string `json:"level"` // warn | bad
	Text  string `json:"text"`
	Fix   string `json:"fix,omitempty"` // a command to run, shown as code
}

type clusterCheck struct {
	mu      sync.Mutex
	Running bool        `json:"running"`
	Started time.Time   `json:"started"`
	Ended   *time.Time  `json:"ended,omitempty"`
	Rows    []*checkRow `json:"rows"`
}

var checkSteps = []string{"model", "command", "port", "disk", "clock", "version"}

const (
	diskWarnBytes = 10 << 30 // under 10 GB free on the models disk
	clockWarn     = 30 * time.Second
)

func (c *Controller) handleGetClusterCheck(w http.ResponseWriter, r *http.Request) {
	c.check.mu.Lock()
	defer c.check.mu.Unlock()
	writeJSON(w, 200, &c.check)
}

func (c *Controller) handleStartClusterCheck(w http.ResponseWriter, r *http.Request) {
	c.check.mu.Lock()
	if c.check.Running {
		c.check.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "a check is already running"})
		return
	}
	c.check.Running, c.check.Started, c.check.Ended = true, time.Now(), nil
	c.check.Rows = c.checkRowsInit()
	c.check.mu.Unlock()
	go c.runClusterCheck()
	c.handleGetClusterCheck(w, r)
}

// checkRowsInit lists every node, online ones with all steps pending.
func (c *Controller) checkRowsInit() []*checkRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	var rows []*checkRow
	for id, n := range c.nodes {
		row := &checkRow{ID: id, Name: displayName(n), Steps: map[string]checkStep{}}
		if _, ok := c.conns[id]; ok {
			row.Online = true
			for _, s := range checkSteps {
				row.Steps[s] = checkStep{State: "pending"}
			}
		} else if t, ok := c.lastSeen[id]; ok {
			row.Offline = "offline since " + t.Format("Jan 2 15:04")
		} else {
			row.Offline = "offline"
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Online != rows[j].Online {
			return rows[i].Online
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}

func (c *Controller) setStep(row *checkRow, step, state, text string) {
	c.check.mu.Lock()
	row.Steps[step] = checkStep{State: state, Text: text}
	c.check.mu.Unlock()
}

func (c *Controller) addNote(row *checkRow, level, text, fix string) {
	c.check.mu.Lock()
	row.Notes = append(row.Notes, checkNote{Level: level, Text: row.Name + ": " + text, Fix: fix})
	c.check.mu.Unlock()
}

func (c *Controller) runClusterCheck() {
	c.check.mu.Lock()
	rows := append([]*checkRow(nil), c.check.Rows...)
	c.check.mu.Unlock()
	var wg sync.WaitGroup
	for _, row := range rows {
		if !row.Online {
			continue
		}
		wg.Add(1)
		go func(row *checkRow) {
			defer wg.Done()
			c.checkNode(row)
		}(row)
	}
	wg.Wait()
	c.check.mu.Lock()
	now := time.Now()
	c.check.Running, c.check.Ended = false, &now
	c.check.mu.Unlock()
}

func (c *Controller) checkNode(row *checkRow) {
	c.mu.Lock()
	n := c.nodes[row.ID]
	hb := c.lastHB[row.ID]
	nc := c.conns[row.ID]
	skew, haveSkew := c.clockSkew[row.ID]
	var hello Hello
	if n != nil && n.Hello != nil {
		hello = *n.Hello
	}
	exec := n != nil && n.ExecEnabled
	c.mu.Unlock()

	// Instant checks first, so the table fills in at once.
	switch {
	case hello.AgentVersion == "":
		c.setStep(row, "version", "warn", "unknown")
	case compareVersions(hello.AgentVersion, Version) < 0:
		c.setStep(row, "version", "warn", hello.AgentVersion+" · update")
		c.addNote(row, "warn", fmt.Sprintf("runs %s, older than the controller (%s). Update it on the Updates page.", hello.AgentVersion, Version), "")
	default:
		c.setStep(row, "version", "ok", hello.AgentVersion)
	}
	if hello.ModelsFreeBytes == 0 {
		c.setStep(row, "disk", "skip", "not reported")
	} else if hello.ModelsFreeBytes < diskWarnBytes {
		c.setStep(row, "disk", "warn", humanBytes(int64(hello.ModelsFreeBytes))+" free")
		c.addNote(row, "warn", fmt.Sprintf("only %s free on the models disk. New models may not fit; delete unused ones on the Models page.", humanBytes(int64(hello.ModelsFreeBytes))), "")
	} else {
		c.setStep(row, "disk", "ok", humanBytes(int64(hello.ModelsFreeBytes))+" free")
	}
	switch {
	case !haveSkew:
		c.setStep(row, "clock", "skip", "no heartbeat yet")
	case time.Duration(math.Abs(float64(skew))) > clockWarn:
		c.setStep(row, "clock", "warn", fmt.Sprintf("%+.0f s", skew.Seconds()))
		fix := "sudo timedatectl set-ntp true"
		if hello.OS == "windows" {
			fix = "w32tm /resync"
		} else if hello.OS == "darwin" {
			fix = "sudo sntp -sS time.apple.com"
		}
		c.addNote(row, "warn", fmt.Sprintf("its clock is %+.0f s off the controller's. Timings and stall warnings may be wrong; turn on time sync.", skew.Seconds()), fix)
	default:
		c.setStep(row, "clock", "ok", fmt.Sprintf("%+.1f s", skew.Seconds()))
	}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); c.checkModel(row, hb) }()
	go func() { defer wg.Done(); c.checkCommand(row, exec, hello) }()
	go func() { defer wg.Done(); c.checkPort(row, nc, hello) }()
	wg.Wait()
}

func (c *Controller) checkModel(row *checkRow, hb *Heartbeat) {
	if hb == nil || hb.LoadedModel == "" {
		c.setStep(row, "model", "skip", "no model loaded")
		return
	}
	c.setStep(row, "model", "running", "")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	spec := InferenceSpec{Messages: []ChatMessage{{Role: "user", Content: "Reply with the single word OK."}}, Params: InferenceParams{MaxTokens: 16}}
	start := time.Now()
	out, err := c.runInferenceJob(ctx, row.ID, spec, "Cluster check", 3*time.Minute)
	if err != nil {
		c.setStep(row, "model", "fail", shortErr(err))
		c.addNote(row, "bad", "the model call failed: "+err.Error()+". Check the node's logs (Logs button on its card).", "")
		return
	}
	txt := fmt.Sprintf("%.1f s", time.Since(start).Seconds())
	if out.TokensPerSec > 0 {
		txt = fmt.Sprintf("%.0f tok/s", out.TokensPerSec)
	}
	c.setStep(row, "model", "ok", txt)
}

func (c *Controller) checkCommand(row *checkRow, exec bool, hello Hello) {
	if !exec {
		c.setStep(row, "command", "skip", "commands off")
		return
	}
	c.setStep(row, "command", "running", "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	start := time.Now()
	out, status, err := c.runExecJob(ctx, row.ID, c.execArgv(row.ID, "echo outpost-check"), nil, "Cluster check", 2*time.Minute)
	if err != nil || out == nil || out.ExitCode != 0 {
		msg := status
		if err != nil {
			msg = err.Error()
		}
		c.setStep(row, "command", "fail", shortErr(fmt.Errorf("%s", msg)))
		c.addNote(row, "bad", "running a command failed ("+msg+"). Tests and builds cannot run there. If the sandbox is on, try Sandbox: basic for this node.", "")
		return
	}
	sh := hello.Shell
	if sh == "" {
		sh = "sh"
	}
	c.setStep(row, "command", "ok", fmt.Sprintf("%s, %.1f s", sh, time.Since(start).Seconds()))
}

func (c *Controller) checkPort(row *checkRow, nc *nodeConn, hello Hello) {
	if hello.Peer == nil || hello.Peer.Port == 0 || nc == nil {
		c.setStep(row, "port", "skip", "not sharing")
		return
	}
	addr := net.JoinHostPort(hostOf(nc.addr), fmt.Sprint(hello.Peer.Port))
	c.setStep(row, "port", "running", "")
	start := time.Now()
	d := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", addr, pinnedTLS(hello.Peer.Fingerprint))
	if err != nil {
		c.setStep(row, "port", "fail", shortErr(err))
		fix := fmt.Sprintf("sudo ufw allow %d/tcp", hello.Peer.Port)
		if hello.OS == "windows" {
			// Same rule the installer adds (see peerFirewallAddArgs).
			fix = "netsh " + strings.Join(peerFirewallAddArgs(hello.Peer.Port), " ")
		}
		msg := fmt.Sprintf("the controller cannot reach its file-sharing port %d (%s). Large build outputs and model copies to and from this node will fail. Open the port in its firewall.", hello.Peer.Port, shortErr(err))
		if hello.OS == "windows" {
			msg += " The install command normally does this when run as administrator, so you only need it if the node was installed without admin rights. Run the command below in an Administrator PowerShell. The rule covers private and domain networks only, so if Windows has this network set to Public, switch it to Private."
		}
		c.addNote(row, "bad", msg, fix)
		return
	}
	conn.Close()
	c.setStep(row, "port", "ok", fmt.Sprintf("%d, %d ms", hello.Peer.Port, time.Since(start).Milliseconds()))
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && len(s) > 60 {
		s = s[i+2:]
	}
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}
