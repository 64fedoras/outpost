// Package outpost holds the files that are compiled into the Outpost binary but live at
// the repository root (the web UI, the licence texts). go:embed cannot reach upward, so
// the main package in cmd/outpost reads them through this package.
package outpost

import "embed"

// WebFS is the web UI (web/index.html, web/app.js, ...), served by the controller.
//
//go:embed web
var WebFS embed.FS

// LicenseText is the AGPL-3.0 text (LICENSE), served at /licenses.
//
//go:embed LICENSE
var LicenseText string

// ThirdPartyNotices is THIRD_PARTY_NOTICES.txt, served at /licenses.
//
//go:embed THIRD_PARTY_NOTICES.txt
var ThirdPartyNotices string
