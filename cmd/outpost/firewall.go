package main

import (
	"strconv"
	"strings"
)

// peerFirewallRule is the Windows Firewall rule that lets other nodes reach
// this node's model-sharing port.
const peerFirewallRule = "Outpost model sharing"

// peerFirewallAddArgs is the netsh command line that creates the rule.
//
// The rule is tied to the port, not to a program path. Each update runs a new
// exe (agent\outpost-<version>.exe), and a rule bound to one exe path did not
// match it, so Windows showed its "allow this app on public and private
// networks" prompt after every update (and, under the service, silently
// blocked the port). To keep the scope tight the rule only applies on private
// and domain networks and only to addresses on the local subnet; the port
// itself only serves files to holders of a one-time token.
func peerFirewallAddArgs(port int) []string {
	return []string{"advfirewall", "firewall", "add", "rule", "name=" + peerFirewallRule,
		"dir=in", "action=allow", "protocol=TCP", "localport=" + strconv.Itoa(port),
		"remoteip=localsubnet", "profile=private,domain"}
}

func peerFirewallDeleteArgs() []string {
	return []string{"advfirewall", "firewall", "delete", "rule", "name=" + peerFirewallRule}
}

// peerRuleNeedsMigration reports whether the PowerShell probe output (the
// Program of each rule named peerFirewallRule, one per line) shows a rule from
// an older install that is still tied to one exe. No rule at all is left alone:
// the user may have removed it on purpose.
func peerRuleNeedsMigration(probe string) bool {
	for _, line := range strings.Split(probe, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.EqualFold(line, "Any") {
			return true
		}
	}
	return false
}
