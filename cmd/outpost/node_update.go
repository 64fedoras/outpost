package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// applyUpdate downloads a release from the controller, verifies and installs
// it under <data>/agent, then restarts into it. Running jobs block an update.
func (n *Node) applyUpdate(u NodeUpdate) {
	status := func(state, errMsg string) {
		_ = n.send("node.update_status", UpdateStatus{Version: u.Version, State: state, Error: errMsg, BytesTotal: u.Bytes})
	}
	fail := func(format string, a ...any) {
		msg := withDefenderHint(fmt.Sprintf(format, a...), n.dataDir)
		log.Printf("update to %s failed: %s", u.Version, msg)
		n.mu.Lock()
		n.updating = false
		n.mu.Unlock()
		status("failed", msg)
	}

	if u.Platform != thisPlatform() {
		fail("release is for %s, this node is %s", u.Platform, thisPlatform())
		return
	}
	if compareVersions(u.Version, Version) <= 0 {
		fail("release %s is not newer than the running %s", u.Version, Version)
		return
	}
	n.mu.Lock()
	if n.updating {
		n.mu.Unlock()
		return
	}
	if len(n.jobs) > 0 {
		n.mu.Unlock()
		fail("%d job(s) are running; try again when the node is idle", len(n.jobs))
		return
	}
	n.updating = true
	n.mu.Unlock()

	log.Printf("updating %s -> %s", Version, u.Version)
	status("downloading", "")
	tmp := filepath.Join(n.agentDir(), ".download")
	err := n.fetchFile("/v1/releases/"+u.Platform, tmp, u.SHA256, u.Bytes, func(d int64) {
		_ = n.send("node.update_status", UpdateStatus{Version: u.Version, State: "downloading", BytesDone: d, BytesTotal: u.Bytes})
	})
	if err != nil {
		os.Remove(tmp)
		fail("download: %v", err)
		return
	}
	status("installing", "")
	path, err := installRelease(tmp, n.agentDir(), u.Version, u.SHA256)
	os.Remove(tmp)
	if err != nil {
		fail("%v", err)
		return
	}
	log.Printf("installed %s; restarting into it", path)
	n.llama.stop() // the new process reloads the model (autoload)
	status("restarting", "")
	time.Sleep(300 * time.Millisecond) // let the status message go out

	n.connMu.Lock()
	if n.conn != nil {
		n.conn.CloseNow()
	}
	n.connMu.Unlock()
	relaunch(n.agentDir(), n.args)
	fail("restart did not happen") // only reached if exec failed
}

// applyRollback restarts into an older installed version.
func (n *Node) applyRollback(version string) {
	status := func(state, errMsg string) {
		_ = n.send("node.update_status", UpdateStatus{Version: version, State: state, Error: errMsg})
	}
	n.mu.Lock()
	busy := len(n.jobs) > 0 || n.updating
	if !busy {
		n.updating = true
	}
	n.mu.Unlock()
	if busy {
		status("failed", "jobs are running or an update is in progress; try again when the node is idle")
		return
	}
	target, err := prepareRollback(n.agentDir(), version)
	if err != nil {
		n.mu.Lock()
		n.updating = false
		n.mu.Unlock()
		status("failed", err.Error())
		return
	}
	log.Printf("rolling back %s -> %s", Version, version)
	status("restarting", "")
	time.Sleep(300 * time.Millisecond)
	n.llama.stop()
	n.connMu.Lock()
	if n.conn != nil {
		n.conn.CloseNow()
	}
	n.connMu.Unlock()
	restartInto(target.path, n.args)
	n.mu.Lock()
	n.updating = false
	n.mu.Unlock()
	status("failed", "restart did not happen")
}

// restartSelf stops jobs and the model, then starts this node again in place.
// The model loaded before the restart is loaded again (autoload).
func (n *Node) restartSelf() { n.restartSelfFor("the controller") }

func (n *Node) restartSelfFor(who string) {
	n.mu.Lock()
	if n.updating {
		n.mu.Unlock()
		return
	}
	n.updating = true
	running := len(n.jobs)
	n.mu.Unlock()
	log.Printf("restart requested from %s (%d running job(s) will be cancelled)", who, running)
	n.cancelAll()
	if running > 0 {
		time.Sleep(2 * time.Second) // let cancelled jobs report and clean up
	}
	n.llama.stop()
	n.connMu.Lock()
	if n.conn != nil {
		n.conn.Close(websocket.StatusServiceRestart, "restarting")
	}
	n.connMu.Unlock()
	self, err := os.Executable()
	if err == nil {
		restartInto(self, n.args)
	}
	log.Printf("could not restart in place (%v); exiting so the service manager starts it again", err)
	os.Exit(1)
}

// withDefenderHint explains a Windows antivirus block. Unsigned Go programs
// that download and run new versions of themselves are a common false
// positive for Microsoft Defender; it deletes or locks the file mid-update.
func withDefenderHint(msg, dataDir string) string {
	l := strings.ToLower(msg)
	if !strings.Contains(l, "virus") && !strings.Contains(l, "potentially unwanted") {
		return msg
	}
	return msg + " — Microsoft Defender blocked an Outpost file (a false positive common for unsigned Go programs). " +
		"In an admin PowerShell run: Add-MpPreference -ExclusionPath '" + dataDir + "' then try again."
}
