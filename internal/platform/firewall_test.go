package platform

import (
	"strings"
	"testing"
)

func TestPeerFirewallRuleIsPortBased(t *testing.T) {
	args := strings.Join(PeerFirewallAddArgs(8444), " ")
	for _, want := range []string{"name=" + PeerFirewallRule, "dir=in", "action=allow", "protocol=TCP",
		"localport=8444", "remoteip=localsubnet", "profile=private,domain"} {
		if !strings.Contains(args, want) {
			t.Errorf("rule %q lacks %q", args, want)
		}
	}
	// A program= filter ties the rule to one exe, which every update replaces.
	if strings.Contains(args, "program=") {
		t.Errorf("rule must not be tied to an exe path: %q", args)
	}
	if strings.Contains(args, "public") {
		t.Errorf("rule must not apply on public networks: %q", args)
	}
}

func TestPeerRuleNeedsMigration(t *testing.T) {
	cases := map[string]bool{
		"":        false, // no rule: leave it alone
		"\r\n":    false,
		"Any\r\n": false,
		"C:\\ProgramData\\outpost\\outpost.exe\r\n":        true,
		"Any\r\nC:\\ProgramData\\outpost\\outpost.exe\r\n": true,
	}
	for in, want := range cases {
		if got := PeerRuleNeedsMigration(in); got != want {
			t.Errorf("PeerRuleNeedsMigration(%q) = %v, want %v", in, got, want)
		}
	}
}
