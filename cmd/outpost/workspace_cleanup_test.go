package main

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"testing"
	"time"
)

// A job that leaves a big collected file keeps its workspace only when it ran
// to the end (the controller fetches the file, then closes it). A cancelled
// or timed-out job's workspace is deleted at once, since nobody fetches it.
func TestWorkspaceKeptOnlyForFetchableJobs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	t.Setenv("OUTPOST_PEER_PORT", "0")
	n := newTestPeerNode(t)
	n.jobs, n.wsLastUsed = map[string]context.CancelFunc{}, map[string]time.Time{}
	n.execEnabled, n.sandbox = true, sandboxOff
	n.startPeerServer()
	if n.peerInfo() == nil {
		t.Fatal("peer server did not start")
	}
	run := func(id, script string, timeoutS int) {
		spec, _ := json.Marshal(ExecSpec{Argv: []string{"sh", "-c", script}})
		n.runJob(JobDispatch{JobID: id, Kind: "exec", TimeoutS: timeoutS, Collect: []string{"big.bin"}, Spec: spec})
	}
	const write = "head -c 1200000 /dev/zero > big.bin"

	run("jobok", write, 30)
	if _, err := os.Stat(n.wsDir("ws_jobok")); err != nil {
		t.Fatalf("a finished job's workspace should wait for the controller's fetch: %v", err)
	}
	if _, ok := n.wsLastUsed["ws_jobok"]; !ok {
		t.Error("a kept workspace must be on the janitor's list")
	}

	run("jobslow", write+"; sleep 10", 1)
	if _, err := os.Stat(n.wsDir("ws_jobslow")); !os.IsNotExist(err) {
		t.Fatalf("a timed-out job's workspace was left on the node (err %v)", err)
	}
	if _, ok := n.resolvePeerFile("art/jobslow/big.bin"); ok {
		t.Error("a timed-out job's file is still served")
	}
}
