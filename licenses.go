package main

import (
	_ "embed"
	"fmt"
	"net/http"
	"os"
)

// Outpost is AGPL-3.0 (LICENSE). Section 13 asks that users of the web UI can get the source,
// so the licence texts ship inside the binary and are served at /licenses.
//
//go:embed LICENSE
var licenseText string

//go:embed THIRD_PARTY_NOTICES.txt
var thirdPartyNotices string

const sourceURL = "https://github.com/64fedoras/outpost"

func licensesText() string {
	return fmt.Sprintf("Outpost %s\nCopyright (C) 2026 Mark (64fedoras)\nLicensed under the GNU Affero General Public License v3.0 (SPDX: AGPL-3.0-only).\nSource code: %s/tree/v%s\n\n%s\n\n%s",
		Version, sourceURL, Version, licenseText, thirdPartyNotices)
}

// handleLicenses is public on purpose: the login page links to it.
func handleLicenses(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, licensesText())
}

func runLicenses() {
	fmt.Fprint(os.Stdout, licensesText())
}
