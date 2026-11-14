package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
)

func TestLocalNetworkBlocked(t *testing.T) {
	// What websocket.Dial returns when macOS blocks a LaunchAgent's connect.
	dialErr := fmt.Errorf("failed to WebSocket dial: %w", &net.OpError{Op: "dial", Net: "tcp",
		Err: &os.SyscallError{Syscall: "connect", Err: syscall.EHOSTUNREACH}})
	cases := []struct {
		goos string
		root bool
		err  error
		want bool
	}{
		{"darwin", false, dialErr, true},
		{"darwin", false, errors.New("dial tcp 192.0.2.10:8443: connect: no route to host"), true},
		{"darwin", true, dialErr, false}, // the system service is exempt: a real network fault
		{"linux", false, dialErr, false},
		{"darwin", false, errors.New("dial tcp 192.0.2.10:8443: connect: connection refused"), false},
		{"darwin", false, nil, false},
	}
	for _, c := range cases {
		if got := localNetworkBlocked(c.goos, c.root, c.err); got != c.want {
			t.Errorf("localNetworkBlocked(%s, root=%v, %v) = %v, want %v", c.goos, c.root, c.err, got, c.want)
		}
	}
}

func TestNoteConnectErrorClears(t *testing.T) {
	n := &Node{}
	n.noteConnectError(errors.New("connection refused"))
	if n.problem != "" {
		t.Fatalf("problem = %q for an ordinary failure", n.problem)
	}
	n.problem = problemLocalNetwork
	n.clearProblem()
	if n.problem != "" {
		t.Fatal("clearProblem left the problem set")
	}
}
