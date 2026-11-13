package main

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// One-paste install. The Add node dialog shows a command such as
//
//	curl -fsSk --pinnedpubkey sha256//... 'https://CONTROLLER/install.sh?t=CODE' | sh
//
// The script (and install.ps1 for Windows) finds the machine's platform,
// downloads the matching binary from this controller, checks its SHA-256 and
// runs the normal `install node --join CODE`. Everything is served only to a
// holder of an unused, unexpired join code, and only to LAN addresses unless the
// controller was started with --setup-allow-from. The transport is pinned to this
// controller's own certificate (curl --pinnedpubkey, or a fingerprint check in
// PowerShell), so no CA and no -k trust-on-first-use is involved.

// spkiPin is the base64 SHA-256 of the certificate's public key, the format
// curl's --pinnedpubkey wants.
func (c *Controller) spkiPin() string {
	c.mu.Lock()
	cert := c.cert
	c.mu.Unlock()
	if len(cert.Certificate) == 0 {
		return ""
	}
	x, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return ""
	}
	s := sha256.Sum256(x.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(s[:])
}

// installAuth checks the join code in ?t= and the caller's address. It writes the
// error and returns false when the request must not be served.
func (c *Controller) installAuth(w http.ResponseWriter, r *http.Request) (code string, ok bool) {
	if !c.setupAllowed(clientIP(r)) {
		http.Error(w, "install downloads are limited to local network addresses", http.StatusForbidden)
		return "", false
	}
	code = r.URL.Query().Get("t")
	raw, err := base64.RawURLEncoding.DecodeString(code)
	var jt JoinToken
	if err != nil || json.Unmarshal(raw, &jt) != nil || jt.Secret == "" {
		http.Error(w, "bad install code", http.StatusUnauthorized)
		return "", false
	}
	c.mu.Lock()
	exp, valid := c.tokens[jt.Secret]
	c.mu.Unlock()
	if !valid || time.Now().After(exp) {
		http.Error(w, "this install code has expired or was already used; make a new one in the controller's Add node dialog", http.StatusUnauthorized)
		return "", false
	}
	return code, true
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (c *Controller) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	code, ok := c.installAuth(w, r)
	if !ok {
		return
	}
	base := c.advertise
	pin := c.spkiPin()
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, installShellScript(base, pin, code))
}

func installShellScript(base, pin, code string) string {
	q := url.QueryEscape(code)
	return `#!/bin/sh
# Outpost node installer, served by your own controller.
set -eu
BASE=` + shellQuote(base) + `
PIN=` + shellQuote(pin) + `
CODE=` + shellQuote(code) + `
os=$(uname -s | tr 'A-Z' 'a-z')
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "Unsupported CPU: $(uname -m)" >&2; exit 1 ;;
esac
case "$os" in linux|darwin) ;; *) echo "Unsupported system: $os (Windows uses the PowerShell command)" >&2; exit 1 ;; esac
platform="$os-$arch"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fetch() { curl -fsSk --pinnedpubkey "sha256//$PIN" "$1" -o "$2"; }
echo "Downloading Outpost for $platform from $BASE ..."
fetch "$BASE/install/sha256/$platform?t=` + q + `" "$tmp/sum" || { echo "The controller has no release for $platform. Upload it in the Updates tab." >&2; exit 1; }
want=$(cat "$tmp/sum")
fetch "$BASE/install/bin/$platform?t=` + q + `" "$tmp/outpost"
if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum "$tmp/outpost" | cut -d' ' -f1); else got=$(shasum -a 256 "$tmp/outpost" | cut -d' ' -f1); fi
if [ "$got" != "$want" ]; then echo "Checksum mismatch; not installing." >&2; exit 1; fi
chmod +x "$tmp/outpost"
if [ "$(id -u)" != 0 ]; then
  sudo "$tmp/outpost" install node --join "$CODE"
else
  "$tmp/outpost" install node --join "$CODE"
fi
`
}

func (c *Controller) handleInstallPS(w http.ResponseWriter, r *http.Request) {
	code, ok := c.installAuth(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, installPowerShell(c.advertise, c.fingerprintNow(), code))
}

func installPowerShell(base, fp, code string) string {
	q := url.QueryEscape(code)
	return `# Outpost node installer, served by your own controller. Run in an Administrator PowerShell.
$ErrorActionPreference = 'Stop'
$base = '` + base + `'
$fp = '` + fp + `'
$code = '` + code + `'
if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Open PowerShell as Administrator and paste the command again.' }
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$platform = "windows-$arch"
$tmp = Join-Path $env:TEMP ('outpost-install-' + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  $wc = New-Object System.Net.WebClient
  Write-Host "Downloading Outpost for $platform from $base ..."
  $want = $wc.DownloadString("$base/install/sha256/$platform?t=` + q + `").Trim()
  $exe = Join-Path $tmp 'outpost.exe'
  $wc.DownloadFile("$base/install/bin/$platform?t=` + q + `", $exe)
  $got = (Get-FileHash $exe -Algorithm SHA256).Hash.ToLower()
  if ($got -ne $want) { throw 'Checksum mismatch; not installing.' }
  & $exe install node --join $code
} finally { Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue }
`
}

// psBootstrap is the one-liner for Windows: it pins this controller's
// certificate by fingerprint, then runs the script.
func psBootstrap(base, fp, code string) string {
	return `powershell -NoProfile -ExecutionPolicy Bypass -Command "Add-Type 'using System.Net;using System.Security.Cryptography.X509Certificates;public class OP{public static void Pin(string f){ServicePointManager.ServerCertificateValidationCallback=(s,c,ch,e)=>new X509Certificate2(c).GetCertHashString(\"SHA256\").ToLower()==f;}}'; [OP]::Pin('` + fp + `'); [Net.ServicePointManager]::SecurityProtocol='Tls12'; iex ((New-Object Net.WebClient).DownloadString('` + base + `/install.ps1?t=` + url.QueryEscape(code) + `'))"`
}

// The pinned callback above runs in the same process for the script's own
// downloads too, so the script needs no pin logic of its own.

func (c *Controller) handleInstallSum(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.installAuth(w, r); !ok {
		return
	}
	p := r.PathValue("platform")
	c.mu.Lock()
	rel := c.releases[p]
	c.mu.Unlock()
	if rel == nil {
		http.Error(w, "no release uploaded for "+p, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, rel.SHA256)
}

func (c *Controller) handleInstallBin(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.installAuth(w, r); !ok {
		return
	}
	p := r.PathValue("platform")
	c.mu.Lock()
	rel := c.releases[p]
	c.mu.Unlock()
	if rel == nil {
		http.Error(w, "no release uploaded for "+p, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, c.releasePath(p))
}

func (c *Controller) installRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /install.sh", c.handleInstallScript)
	mux.HandleFunc("GET /install.ps1", c.handleInstallPS)
	mux.HandleFunc("GET /install/sha256/{platform}", c.handleInstallSum)
	mux.HandleFunc("GET /install/bin/{platform}", c.handleInstallBin)
}

// installCommands are the pasteable one-liners for the Add node dialog.
func (c *Controller) installCommands(code string) map[string]string {
	base := c.advertise
	return map[string]string{
		"curl_unix": "curl -fsSk --pinnedpubkey 'sha256//" + c.spkiPin() + "' '" + base + "/install.sh?t=" + url.QueryEscape(code) + "' | sh",
		"curl_win":  psBootstrap(base, c.fingerprintNow(), code),
	}
}

// releasePlatforms lists the platforms that have an uploaded binary, so the
// dialog can say which one-paste commands will work.
func (c *Controller) releasePlatforms() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.releases))
	for p := range c.releases {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func encodeJoinToken(url, fp, secret string) string {
	b, _ := json.Marshal(JoinToken{URL: url, Fingerprint: fp, Secret: secret})
	return base64.RawURLEncoding.EncodeToString(b)
}
