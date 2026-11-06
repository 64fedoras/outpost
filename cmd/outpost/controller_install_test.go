package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func installTestController(t *testing.T) (*Controller, string) {
	c := authTestController(t)
	cert, fp, err := loadOrCreateCert(c.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	c.cert, c.fingerprint = cert, fp
	c.advertise = "https://192.0.2.10:8443"
	c.tokens = map[string]time.Time{"sekrit": time.Now().Add(time.Hour)}
	c.releases = map[string]*Release{}
	body := []byte("#!/bin/true\n")
	c.releases["linux-amd64"] = &Release{Platform: "linux-amd64", SHA256: "abc123"}
	if err := os.MkdirAll(c.releasesDir()+"/linux-amd64", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.releasePath("linux-amd64"), body, 0o755); err != nil {
		t.Fatal(err)
	}
	return c, c.installCommands(mustToken(c, "sekrit"))["curl_unix"]
}

func mustToken(c *Controller, secret string) string {
	return encodeJoinToken(c.advertise, c.fingerprint, secret)
}

func installGet(c *Controller, path, remote string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	c.installRoutes(mux)
	r := httptest.NewRequest("GET", path, nil)
	r.RemoteAddr = remote + ":5000"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestInstallEndpoints(t *testing.T) {
	c, cmd := installTestController(t)
	if !strings.Contains(cmd, "--pinnedpubkey 'sha256//") || !strings.HasSuffix(cmd, "| sh") {
		t.Fatalf("bad command: %s", cmd)
	}
	tok := mustToken(c, "sekrit")
	w := installGet(c, "/install.sh?t="+tok, "192.168.1.50")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "install node --join") {
		t.Fatalf("script: %d %s", w.Code, w.Body.String())
	}
	if w := installGet(c, "/install/sha256/linux-amd64?t="+tok, "192.168.1.50"); w.Code != 200 || w.Body.String() != "abc123" {
		t.Fatalf("sum: %d %s", w.Code, w.Body.String())
	}
	if w := installGet(c, "/install/bin/linux-amd64?t="+tok, "192.168.1.50"); w.Code != 200 || !strings.HasPrefix(w.Body.String(), "#!") {
		t.Fatalf("bin: %d", w.Code)
	}
	if w := installGet(c, "/install/bin/darwin-arm64?t="+tok, "192.168.1.50"); w.Code != 404 {
		t.Fatalf("missing release should 404, got %d", w.Code)
	}
	if w := installGet(c, "/install.ps1?t="+tok, "192.168.1.50"); w.Code != 200 || !strings.Contains(w.Body.String(), "Get-FileHash") {
		t.Fatalf("ps1: %d", w.Code)
	}
	// Wrong code, public address, and expired code are refused.
	if w := installGet(c, "/install.sh?t="+mustToken(c, "other"), "192.168.1.50"); w.Code != 401 {
		t.Fatalf("wrong code: %d", w.Code)
	}
	if w := installGet(c, "/install.sh?t="+tok, "203.0.113.9"); w.Code != 403 {
		t.Fatalf("public address: %d", w.Code)
	}
	c.tokens["sekrit"] = time.Now().Add(-time.Minute)
	if w := installGet(c, "/install.sh?t="+tok, "192.168.1.50"); w.Code != 401 {
		t.Fatalf("expired: %d", w.Code)
	}
}
