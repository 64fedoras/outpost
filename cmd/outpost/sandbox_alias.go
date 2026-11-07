package main

import (
	"errors"
	"os/exec"

	"outpost/internal/sandbox"
)

// The exec sandbox lives in internal/sandbox. These names keep package main's
// call sites unchanged; the node's "sandbox-child" mode (main.go) runs
// sandbox.RunChild.

type sandboxPolicy = sandbox.Policy
type sandboxInput = sandbox.Input

const (
	sandboxOff    = sandbox.Off
	sandboxBasic  = sandbox.Basic
	sandboxStrict = sandbox.Strict
)

var (
	normSandbox        = sandbox.Norm
	effectiveSandbox   = sandbox.Effective
	buildSandboxPolicy = sandbox.Build
	sandboxLayers      = sandbox.Layers
	sandboxNote        = sandbox.Note
	looksSandboxDenied = sandbox.LooksDenied
	needsNetwork       = sandbox.NeedsNetwork
	execNetwork        = sandbox.ExecNetwork
	runSandboxChild    = sandbox.RunChild
)

// sandboxWrap rewrites cmd to start the sandbox child. A sandbox that cannot be
// set up is the job's error, never a silent run without it.
func sandboxWrap(cmd *exec.Cmd, p *sandboxPolicy) *ErrInfo {
	err := sandbox.Wrap(cmd, p)
	if err == nil {
		return nil
	}
	var u *sandbox.Unavailable
	if errors.As(err, &u) {
		return errf("sandbox_unavailable", false, "%s", u.Message)
	}
	return errf("sandbox_unavailable", false, "%v", err)
}

func sandboxAttach(cmd *exec.Cmd, mode string) func() {
	return sandbox.Attach(cmd, mode, windowsRAM())
}

func sandboxPlatformMode(mode string) string { return sandbox.PlatformMode(mode) }

// withSandboxNote adds the sandbox line to a failed command's output when the
// failure looks like the sandbox blocked it, so the fixer is not misled.
func withSandboxNote(text string, out *ExecOutput) string {
	if out != nil && out.SandboxDenied && out.SandboxNote != "" {
		return text + "\n[Outpost: " + out.SandboxNote + ". If the error above is about permissions or the network, the code may be fine; do not rewrite it for that.]"
	}
	return text
}
