package main

import "errors"

// needAdminForService is the Windows install rule: the node installs as the
// Outpost Node service (runs at boot with nobody signed in), which needs an
// Administrator shell. Without one, install stops before the join code is
// used and says how to continue; --no-service keeps the per-user mode.
func needAdminForService(noService, elevated bool) error {
	if noService || elevated {
		return nil
	}
	return errors.New(`Outpost installs as a Windows service so it runs with nobody signed in, and that needs an Administrator PowerShell.
  Open one: right-click Start, choose "Terminal (Admin)" or "Windows PowerShell (Admin)", cd to this folder and paste the same command again.
  The join code was not used, so it still works.
  To install for this user only instead (runs while you are signed in), add --no-service.`)
}
