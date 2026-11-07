package main

import "outpost/internal/platform"

// Small OS helpers (disk space, process groups, the node lock, the Windows
// firewall rule text) live in internal/platform, which has the per-OS files.
// These names keep package main's call sites unchanged. Hardware detection,
// the launcher (relaunch_*.go) and the firewall migration stay in main because
// they use wire types and the update code that live here.

const peerFirewallRule = platform.PeerFirewallRule

var (
	diskFree               = platform.DiskFree
	diskTotal              = platform.DiskTotal
	setProcGroup           = platform.SetProcGroup
	killProcGroup          = platform.KillProcGroup
	osInterrupt            = platform.OSInterrupt
	windowsRAM             = platform.WindowsRAM
	acquireNodeLock        = platform.AcquireNodeLock
	peerFirewallAddArgs    = platform.PeerFirewallAddArgs
	peerFirewallDeleteArgs = platform.PeerFirewallDeleteArgs
	peerRuleNeedsMigration = platform.PeerRuleNeedsMigration
)
