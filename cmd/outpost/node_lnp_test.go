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
		err  error
		want bool
	}{
		{"darwin", dialErr, true},
		{"darwin", errors.New("dial tcp 192.0.2.10:8443: connect: no route to host"), true},
		{"linux", dialErr, false},
		{"darwin", errors.New("dial tcp 192.0.2.10:8443: connect: connection refused"), false},
		{"darwin", nil, false},
	}
	for _, c := range cases {
		if got := localNetworkBlocked(c.goos, c.err); got != c.want {
			t.Errorf("localNetworkBlocked(%s, %v) = %v, want %v", c.goos, c.err, got, c.want)
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
