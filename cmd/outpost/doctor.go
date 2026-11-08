package main

// outpost doctor: checks a machine before (or after) joining a cluster and says
// in plain words what is wrong and the command that fixes it. It looks at this
// machine only: the data folder, the controller's address and TLS certificate,
// the clock, the peer port and the hardware. Once a node has joined, the
// Check cluster button on the Nodes page checks the whole cluster.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type doctorResult struct {
	Name  string
	Level string // ok | warn | bad
	Text  string
	Fix   string
}

type doctorOpts struct {
	Controller  string // https://host:port
	Fingerprint string // optional: expected certificate SHA-256
	DataDir     string
	PeerPort    int
}

func (r doctorResult) String() string {
	mark := map[string]string{"ok": "  ok ", "warn": " warn", "bad": " FAIL"}[r.Level]
	s := fmt.Sprintf("[%s] %-18s %s", mark, r.Name, r.Text)
	if r.Fix != "" {
		label := "fix"
		if r.Level == "ok" {
			label = "note"
		}
		s += "\n                     " + label + ": " + r.Fix
	}
	return s
}

func runDoctor(args []string) error {
	fsf := flag.NewFlagSet("doctor", flag.ExitOnError)
	ctl := fsf.String("controller", "", "controller address, e.g. https://192.0.2.10:8443 (default: the one this node joined)")
	fp := fsf.String("fingerprint", "", "expected certificate fingerprint (SHA-256, hex)")
	data := fsf.String("data", defaultDataDir("node"), "node data directory")
	_ = fsf.Parse(args)
	o := doctorOpts{Controller: *ctl, Fingerprint: *fp, DataDir: *data, PeerPort: peerPort}
	if o.Controller == "" {
		if b, err := os.ReadFile(filepath.Join(o.DataDir, "node.json")); err == nil {
			var cfg NodeConfig
			if json.Unmarshal(b, &cfg) == nil {
				o.Controller = cfg.Controller
				if o.Fingerprint == "" {
					o.Fingerprint = cfg.Fingerprint
				}
			}
		}
	}
	bad := 0
	for _, r := range doctorChecks(context.Background(), o) {
		fmt.Println(r)
		if r.Level == "bad" {
			bad++
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d problem(s) found", bad)
	}
	return nil
}

func doctorChecks(ctx context.Context, o doctorOpts) []doctorResult {
	var out []doctorResult
	add := func(name, level, text, fix string) { out = append(out, doctorResult{name, level, text, fix}) }
	out = append(out, doctorFolder(o.DataDir)...)
	if o.Controller == "" {
		add("controller", "warn", "no controller address given and this machine has not joined one", "run: outpost doctor --controller https://CONTROLLER_IP:8443")
	} else {
		out = append(out, doctorController(ctx, o)...)
	}
	out = append(out, doctorPeerPort(o.PeerPort))
	out = append(out, doctorHardware()...)
	return out
}

func doctorFolder(dir string) []doctorResult {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return []doctorResult{{"data folder", "bad", fmt.Sprintf("cannot create %s: %v", dir, err), "run with sudo, or choose another place with --data"}}
	}
	probe := filepath.Join(dir, ".doctor")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		return []doctorResult{{"data folder", "bad", fmt.Sprintf("%s is not writable: %v", dir, err), "fix the folder's owner or permissions"}}
	}
	os.Remove(probe)
	res := []doctorResult{{"data folder", "ok", dir + " is writable", ""}}
	if free := diskFree(dir); free > 0 {
		switch {
		case free < 2<<30:
			res = append(res, doctorResult{"free disk", "bad", humanBytes(int64(free)) + " free; models need many GB", "free some space or put the data folder on a bigger disk (--data)"})
		case free < 20<<30:
			res = append(res, doctorResult{"free disk", "warn", humanBytes(int64(free)) + " free; only small models will fit", ""})
		default:
			res = append(res, doctorResult{"free disk", "ok", humanBytes(int64(free)) + " free", ""})
		}
	}
	return res
}

func doctorController(ctx context.Context, o doctorOpts) []doctorResult {
	u, err := url.Parse(o.Controller)
	if err != nil || u.Host == "" {
		return []doctorResult{{"controller", "bad", "that address is not a URL like https://192.0.2.10:8443", ""}}
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return []doctorResult{{"controller", "bad", fmt.Sprintf("cannot connect to %s: %v", host, err),
			"check the address, that the controller is running, and that its firewall allows the port (Linux: sudo ufw allow " + portOf(host) + "/tcp)"}}
	}
	conn.Close()
	res := []doctorResult{{"controller reachable", "ok", host + " accepts connections", ""}}
	var seen string
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) > 0 {
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			seen = hex.EncodeToString(sum[:])
		}
		return nil
	}}, DisableKeepAlives: true}
	client := &http.Client{Transport: tr, Timeout: 8 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(o.Controller, "/")+"/healthz", nil)
	resp, err := client.Do(req)
	if err != nil {
		return append(res, doctorResult{"controller TLS", "bad", "connected, but the HTTPS request failed: " + err.Error(), "make sure the address starts with https:// and the port is the controller's"})
	}
	resp.Body.Close()
	switch {
	case o.Fingerprint == "":
		res = append(res, doctorResult{"certificate", "ok", "fingerprint " + seen + " (nothing to compare it with)", ""})
	case strings.EqualFold(strings.ReplaceAll(o.Fingerprint, ":", ""), seen):
		res = append(res, doctorResult{"certificate", "ok", "matches the expected fingerprint", ""})
	default:
		res = append(res, doctorResult{"certificate", "bad", "the controller's certificate is " + seen + ", not the expected " + o.Fingerprint, "if you replaced the controller's certificate, run: outpost repin --fingerprint " + seen})
	}
	if resp.StatusCode != 200 {
		res = append(res, doctorResult{"controller health", "warn", fmt.Sprintf("/healthz answered %d (it may be shutting down)", resp.StatusCode), ""})
	}
	if t, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		skew := time.Since(t)
		if skew < 0 {
			skew = -skew
		}
		if skew > 30*time.Second {
			fix := "sudo timedatectl set-ntp true"
			if runtime.GOOS == "windows" {
				fix = "w32tm /resync (as administrator)"
			} else if runtime.GOOS == "darwin" {
				fix = "System Settings, General, Date & Time: set the time automatically"
			}
			res = append(res, doctorResult{"clock", "warn", fmt.Sprintf("this machine's clock differs from the controller's by %s", skew.Round(time.Second)), fix})
		} else {
			res = append(res, doctorResult{"clock", "ok", "within 30 seconds of the controller", ""})
		}
	}
	return res
}

func portOf(hostport string) string {
	_, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return "8443"
	}
	return p
}

func doctorPeerPort(port int) doctorResult {
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return doctorResult{"peer port", "warn", fmt.Sprintf("port %d is already in use here (normal if an Outpost node is already running)", port), ""}
	}
	l.Close()
	fix := ""
	if runtime.GOOS == "linux" {
		fix = fmt.Sprintf("if a firewall is on, other nodes need it open: sudo ufw allow %d/tcp (the installer does this for ufw and firewalld)", port)
	}
	return doctorResult{"peer port", "ok", fmt.Sprintf("port %d is free to listen on (other nodes use it to copy models from this one)", port), fix}
}

func doctorHardware() []doctorResult {
	hw := detectHardware()
	res := []doctorResult{{"memory", "ok", fmt.Sprintf("%.1f GB RAM, %d CPU threads", float64(hw.RAMBytes)/(1<<30), hw.CPU.Threads), ""}}
	if hw.RAMBytes < 3<<30 {
		res[0].Level, res[0].Text = "warn", res[0].Text+" (models of 1 to 2 GB only)"
	}
	if len(hw.GPUs) == 0 {
		return append(res, doctorResult{"GPU", "warn", "no GPU found; models will run on the CPU, slowly", ""})
	}
	for _, g := range hw.GPUs {
		res = append(res, doctorResult{"GPU", "ok", fmt.Sprintf("%s (%s, %.1f GB, backend %s)", g.Name, g.Vendor, float64(g.VRAMBytes)/(1<<30), g.Backend), ""})
	}
	if !vulkanAvailable() && runtime.GOOS != "darwin" {
		fix := "install the Vulkan loader: sudo apt install libvulkan1 (Debian, Ubuntu) or sudo dnf install vulkan-loader"
		if runtime.GOOS == "windows" {
			fix = "update the graphics driver from the GPU maker; it includes Vulkan"
		}
		res = append(res, doctorResult{"Vulkan", "warn", "a GPU was found but no Vulkan loader, so llama.cpp will use the CPU", fix})
	}
	return res
}
