//go:build darwin

package main

import (
	"context"
	"log"
	"net/http"
	"os/exec"
	"time"
)

// lnpProbeScript opens one connection to the controller through Foundation's
// URL loading (Network.framework underneath). Unlike a plain socket connect,
// that makes macOS show "Allow outpost to find devices on local networks?".
// osascript runs as this node's child, so the system attributes the request to
// the node's binary. NSURLSession is not in JXA's bridge metadata on macOS 15
// ($.NSURLSession is undefined), so this uses NSData's synchronous loader,
// which is. It stays alive afterwards because macOS may not record the answer
// for a process that exits straight away. The TLS result does not matter.
const lnpProbeScript = `ObjC.import('Foundation');
function run(argv) {
  $.NSData.dataWithContentsOfURL($.NSURL.URLWithString(argv[0]));
  delay(20);
}`

func askLocalNetwork(controllerURL string) {
	if controllerURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", lnpProbeScript, controllerURL).CombinedOutput(); err != nil {
		log.Printf("local network prompt helper: %v %s", err, out)
	}
}

// waitNodeOnline watches a freshly installed node for up to d and returns its
// last status, so the installer can say whether it connected and, if macOS
// blocks it, where to allow it. The control file may still name an old,
// stopped process for a moment; such reads just fail and are retried.
func waitNodeOnline(dataDir string, d time.Duration) *controlStatus {
	t := &trayApp{dataDir: dataDir, client: &http.Client{Timeout: 3 * time.Second}}
	var last *controlStatus
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(time.Second) {
		if st, err := t.call("GET", "/status"); err == nil && st != nil {
			last = st
			if st.Online {
				break
			}
		}
	}
	return last
}
