package platform

import (
	"strings"
	"testing"
)

func TestPeerFirewallPowerShell(t *testing.T) {
	got := PeerFirewallPowerShell(8444, "")
	for _, want := range []string{"New-NetFirewallRule", "-DisplayName '" + PeerFirewallRule + "'", "-LocalPort 8444",
		"-RemoteAddress LocalSubnet ", "-Profile Private,Domain", "-Direction Inbound", "-Action Allow", "-Protocol TCP"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, `"`) {
		t.Errorf("must not use double quotes (PowerShell drops them for native commands, and the command is pasted): %s", got)
	}
	// The controller's address is added, but only when it really is an address.
	if got := PeerFirewallPowerShell(8444, "192.0.2.10"); !strings.Contains(got, "-RemoteAddress LocalSubnet,192.0.2.10 ") {
		t.Errorf("controller address not added: %s", got)
	}
	for _, bad := range []string{"example.local", "1.2.3.4; Remove-Item x", "'", ""} {
		if got := PeerFirewallPowerShell(8444, bad); !strings.Contains(got, "-RemoteAddress LocalSubnet -Profile") {
			t.Errorf("a non-address %q was put in the command: %s", bad, got)
		}
	}
}
