package main

// Pooling: a helper node runs llama.cpp's RPC server (ggml-rpc-server) so a
// main node's llama-server can place part of a model in the helper's RAM/VRAM.
// llama.cpp's RPC protocol has neither authentication nor encryption. Outpost
// keeps ggml-rpc-server on 127.0.0.1 and puts a gatekeeper in front that
// accepts only the main node's source IP (spoofable on a hostile LAN). Nothing
// listens unless the controller has pooling enabled (off by default) and asks
// this node to lend; the listener closes when the pool ends. Trusted LANs only.

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rpcConfig struct {
	Host    string   `json:"host"`
	Port    int      `json:"port"`
	Allow   []string `json:"allow"`
	Threads int      `json:"threads"`
}

type rpcMgr struct {
	n        *Node
	mu       sync.Mutex
	cmd      *exec.Cmd
	exited   chan struct{} // closed once cmd.Wait returns
	gate     net.Listener  // the gatekeeper in front of the RPC server
	target   string        // the RPC server's loopback address
	endpoint string
	gen      int
}

func newRPCMgr(n *Node) *rpcMgr { return &rpcMgr{n: n} }

func (n *Node) rpcConfigPath() string { return filepath.Join(n.llamaDir(), "rpc.json") }

func (r *rpcMgr) current() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil {
		return ""
	}
	return r.endpoint
}

func isLocalIP(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, l := range localIPs() {
		if l.Equal(ip) {
			return true
		}
	}
	return false
}

func portFree(host string, port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// start launches the RPC server (stopping this node's own model first) and
// reports op "rpc" ready with the endpoint in Detail.
func (r *rpcMgr) start(rs RPCStart) {
	n := r.n
	fail := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		log.Printf("rpc: %s", msg)
		n.opStatus("rpc", "serve", "failed", 0, 0, msg)
	}
	rt := n.currentRuntime()
	if rt == nil {
		fail("llama.cpp is not installed on this node yet")
		return
	}
	dir := filepath.Dir(rt.ServerPath)
	bin := ""
	for _, name := range []string{"ggml-rpc-server", "rpc-server"} {
		if p := filepath.Join(dir, name+exeSuffix(runtime.GOOS)); fileExists(p) {
			bin = p
			break
		}
	}
	if bin == "" {
		fail("this llama.cpp build has no RPC server")
		return
	}
	// Lending memory replaces this node's own model.
	n.llama.stop()
	n.saveAutoload(nil)
	r.stop(false)

	bind := rs.Host
	if !isLocalIP(bind) {
		// The controller sees this node through NAT or a proxy, so its address is not ours.
		// Still allow-list only; the port is open to the whole network for as long as the pool lasts.
		bind = "0.0.0.0"
		log.Printf("rpc: %q is not an address of this machine; listening on all interfaces (allow-list still applies)", rs.Host)
	}
	port := rs.Port
	if port == 0 {
		if b, err := os.ReadFile(n.rpcConfigPath()); err == nil {
			var old rpcConfig
			if json.Unmarshal(b, &old) == nil {
				port = old.Port
			}
		}
	}
	if port == 0 || !portFree(bind, port) {
		port = 0
		for p := 50052; p < 50100; p++ {
			if portFree(bind, p) {
				port = p
				break
			}
		}
	}
	if port == 0 {
		fail("no free port for the RPC server")
		return
	}
	threads := rs.Threads
	if threads <= 0 {
		threads = runtime.NumCPU()
	}
	if len(rs.Allow) == 0 {
		fail("no main node given; refusing to open the RPC server to everyone")
		return
	}
	cfg := rpcConfig{Host: rs.Host, Port: port, Allow: rs.Allow, Threads: threads}
	if err := r.launch(bin, bind, cfg, 0); err != nil {
		fail("%v", err)
		return
	}
	b, _ := json.Marshal(cfg)
	_ = os.WriteFile(n.rpcConfigPath(), b, 0o644)
	n.pushHeartbeat()
	n.resendHello()
	n.opStatus("rpc", "serve", "ready", 0, 0, "")
}

// launch starts ggml-rpc-server on 127.0.0.1 and, the first time, the
// gatekeeper on bind:cfg.Port that forwards only connections from cfg.Allow.
func (r *rpcMgr) launch(bin, bind string, cfg rpcConfig, restarts int) error {
	local := 0
	for p := 50152; p < 50300; p++ {
		if portFree("127.0.0.1", p) {
			local = p
			break
		}
	}
	if local == 0 {
		return fmt.Errorf("no free local port for the RPC server")
	}
	args := []string{"-H", "127.0.0.1", "-p", strconv.Itoa(local), "-t", strconv.Itoa(cfg.Threads), "-c"}
	cmd := exec.Command(bin, args...)
	cmd.Dir = filepath.Dir(bin)
	// The RPC tensor cache lives in Outpost's data folder (self-contained,
	// removed when the node stops lending).
	cmd.Env = append(llamaEnv(filepath.Dir(bin)), "LLAMA_CACHE="+r.n.rpcCacheDir())
	tail := &tailBuf{}
	cmd.Stdout, cmd.Stderr = tail, tail
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	// Wait until it accepts connections.
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(local))
	ok := false
	for i := 0; i < 50; i++ {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			ok = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ok {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("RPC server did not start: %s", lastLines(tail.String(), 3))
	}
	endpoint := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	r.mu.Lock()
	gate := r.gate
	r.mu.Unlock()
	if gate == nil {
		ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(cfg.Port)))
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return fmt.Errorf("cannot listen on port %d: %v", cfg.Port, err)
		}
		gate = ln
		go r.serveGate(ln, cfg.Allow)
	}
	r.mu.Lock()
	r.gate, r.target = gate, addr
	exited := make(chan struct{})
	r.cmd, r.exited, r.endpoint = cmd, exited, endpoint
	r.gen++
	gen := r.gen
	r.mu.Unlock()
	log.Printf("lending memory: RPC server on %s, open only to %s (%d threads)", endpoint, strings.Join(cfg.Allow, ", "), cfg.Threads)
	go func() {
		err := cmd.Wait()
		close(exited)
		r.mu.Lock()
		mine := r.gen == gen
		if mine {
			r.cmd, r.exited = nil, nil
		}
		r.mu.Unlock()
		if !mine {
			return
		}
		log.Printf("RPC server exited (%v): %s", err, lastLines(tail.String(), 3))
		if restarts < 5 {
			time.Sleep(time.Duration(2+3*restarts) * time.Second)
			if e := r.launch(bin, bind, cfg, restarts+1); e != nil {
				log.Printf("RPC server restart failed: %v", e)
			}
		}
		r.n.pushHeartbeat()
	}()
	return nil
}

// stop ends lending. forget also removes the saved config (no autostart).
func (r *rpcMgr) stop(forget bool) {
	r.mu.Lock()
	cmd, exited, gate := r.cmd, r.exited, r.gate
	r.cmd, r.exited, r.endpoint, r.gate = nil, nil, "", nil
	r.gen++
	r.mu.Unlock()
	if gate != nil {
		gate.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(osInterrupt())
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		log.Printf("stopped lending memory")
	}
	if forget {
		os.Remove(r.n.rpcConfigPath())
		os.RemoveAll(r.n.rpcCacheDir())
	}
}

// autostartRPC resumes lending after a restart, so the main node reconnects.
func (n *Node) autostartRPC() {
	b, err := os.ReadFile(n.rpcConfigPath())
	if err != nil {
		return
	}
	var cfg rpcConfig
	if json.Unmarshal(b, &cfg) != nil || cfg.Port == 0 {
		return
	}
	log.Printf("resuming lending memory on port %d (it was lending before this restart)", cfg.Port)
	n.rpc.start(RPCStart{Host: cfg.Host, Port: cfg.Port, Allow: cfg.Allow, Threads: cfg.Threads})
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func (n *Node) rpcCacheDir() string { return filepath.Join(n.llamaDir(), "rpc-cache") }

// serveGate accepts connections on the LAN port and forwards only those from
// allowed IPs (the main node) to the RPC server on loopback. llama.cpp's RPC
// server has no authentication of its own.
func (r *rpcMgr) serveGate(ln net.Listener, allow []string) {
	allowed := map[string]bool{"127.0.0.1": true, "::1": true} // same-machine only; not reachable from the LAN
	for _, a := range allow {
		if ip := net.ParseIP(a); ip != nil {
			allowed[ip.String()] = true
		}
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // closed by stop()
		}
		host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
		ip := net.ParseIP(host)
		if ip == nil || !allowed[ip.String()] {
			log.Printf("rpc gatekeeper: refused connection from %s (only %s may connect)", host, strings.Join(allow, ", "))
			conn.Close()
			continue
		}
		go r.forward(conn)
	}
}

func (r *rpcMgr) forward(in net.Conn) {
	defer in.Close()
	r.mu.Lock()
	target := r.target
	r.mu.Unlock()
	out, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		return
	}
	defer out.Close()
	if tc, ok := in.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	if tc, ok := out.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(out, in); done <- struct{}{} }()
	go func() { io.Copy(in, out); done <- struct{}{} }()
	<-done
}
