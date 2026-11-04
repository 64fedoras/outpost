//go:build windows

package main

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// The service access list must parse and must give interactive users the
// start (RP) and stop (WP) rights, and nothing that changes the service.
func TestServiceSDDL(t *testing.T) {
	sd, err := windows.SecurityDescriptorFromString(serviceSDDL)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sd.DACL(); err != nil {
		t.Fatal(err)
	}
	for _, ace := range strings.Split(strings.TrimPrefix(serviceSDDL, "D:"), ")(") {
		if strings.HasSuffix(strings.Trim(ace, "()"), ";;;IU") {
			rights := strings.Split(strings.Trim(ace, "()"), ";")[2]
			if !strings.Contains(rights, "RP") || !strings.Contains(rights, "WP") || strings.Contains(rights, "DC") || strings.Contains(rights, "WD") || strings.Contains(rights, "SD") {
				t.Fatalf("interactive users get %q", rights)
			}
			return
		}
	}
	t.Fatal("no entry for interactive users")
}
