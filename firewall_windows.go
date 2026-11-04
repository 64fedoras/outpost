//go:build windows

package main

import (
	"log"
	"os/exec"
	"syscall"
)

// migratePeerFirewallRule replaces a model-sharing rule left by an older
// install (tied to one exe path, so it stopped matching after the first
// update) with the port-based rule. It needs administrator rights, which the
// service has; a per-user node skips it. Best effort and quiet.
func migratePeerFirewallRule() {
	if !isElevated() {
		return
	}
	probe := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"Get-NetFirewallRule -DisplayName '"+peerFirewallRule+"' -ErrorAction SilentlyContinue | Get-NetFirewallApplicationFilter | ForEach-Object { $_.Program }")
	probe.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	out, err := probe.Output()
	if err != nil || !peerRuleNeedsMigration(string(out)) {
		return
	}
	_ = hiddenCmd("netsh", peerFirewallDeleteArgs()...).Run()
	if err := hiddenCmd("netsh", peerFirewallAddArgs(peerPort)...).Run(); err != nil {
		log.Printf("could not update the Windows Firewall rule %q: %v", peerFirewallRule, err)
		return
	}
	log.Printf("updated the Windows Firewall rule %q to cover port %d for every Outpost version, so updates no longer trigger firewall prompts", peerFirewallRule, peerPort)
}

func hiddenCmd(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	return c
}
