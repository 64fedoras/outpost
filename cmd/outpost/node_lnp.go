package main

// macOS "Local Network" privacy (macOS 15 and later) blocks a per-user
// LaunchAgent from opening connections to LAN addresses until the user allows
// it, and the failure is a plain "no route to host" (EHOSTUNREACH). A node
// that joined fine from Terminal (Terminal is exempt) then never connects from
// its LaunchAgent. This file spots that case so the log and the menu bar item
// can say what to do, and on macOS asks the system to show the Allow prompt
// (node_lnp_darwin.go), which plain BSD sockets, as Go uses, do not trigger.

import (
	"errors"
	"log"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const problemLocalNetwork = "local_network"

// lnpHelp is the instruction shown in node.log and printed by the installer.
const lnpHelp = "macOS is blocking Outpost from the local network. Open System Settings > Privacy & Security > Local Network and turn on \"outpost\" (or click Allow if macOS asks). The node connects within a few seconds."

// isNoRoute reports whether err is a "no route to host" failure.
func isNoRoute(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, syscall.EHOSTUNREACH) || strings.Contains(strings.ToLower(err.Error()), "no route to host")
}

// localNetworkBlocked reports whether a connect error on this OS looks like
// macOS local network privacy rather than a real network fault. A node that
// runs as root (the LaunchDaemon that install node sets up) is exempt from
// that privacy rule, so for it "no route to host" is a real network fault and
// the Local Network advice would send the owner to the wrong place.
func localNetworkBlocked(goos string, root bool, err error) bool {
	return goos == "darwin" && !root && isNoRoute(err)
}

// noteConnectError records why the last connection attempt failed, for the
// tray, and on macOS asks the system to show the Local Network prompt.
func (n *Node) noteConnectError(err error) {
	problem := ""
	if localNetworkBlocked(runtime.GOOS, os.Geteuid() == 0, err) {
		problem = problemLocalNetwork
	}
	n.mu.Lock()
	prev := n.problem
	n.problem = problem
	due := time.Since(n.lnpAsked) > 5*time.Minute
	if problem != "" && due {
		n.lnpAsked = time.Now()
	}
	n.mu.Unlock()
	if problem == "" {
		return
	}
	if prev != problem {
		log.Print(lnpHelp)
	}
	if due {
		go askLocalNetwork(n.cfg.Controller)
	}
}

func (n *Node) clearProblem() {
	n.mu.Lock()
	n.problem = ""
	n.mu.Unlock()
}
