package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func authTestController(t *testing.T) *Controller {
	t.Helper()
	c := &Controller{dataDir: t.TempDir(), sessions: map[string]time.Time{}, sessMeta: map[string]sessionMeta{},
		loginFails: map[string][]time.Time{}}
	if err := c.setAdminPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	return c
}

func login(c *Controller, ip, pw string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"Password":"`+pw+`"}`))
	r.RemoteAddr = ip + ":1234"
	w := httptest.NewRecorder()
	c.handleLogin(w, r)
	return w
}

func TestLoginLockout(t *testing.T) {
	c := authTestController(t)
	for i := 0; i < lockoutFails; i++ {
		if w := login(c, "10.0.0.9", "nope"); w.Code != 401 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	// Locked now, even with the right password; another IP is unaffected.
	if w := login(c, "10.0.0.9", "correct horse"); w.Code != 429 {
		t.Fatalf("expected lockout, got %d", w.Code)
	}
	if w := login(c, "10.0.0.10", "correct horse"); w.Code != 200 {
		t.Fatalf("other ip: %d", w.Code)
	}
	fails, locked := c.lockoutStatus()
	if fails != lockoutFails || len(locked) != 1 || locked[0] != "10.0.0.9" {
		t.Fatalf("status %d %v", fails, locked)
	}
	// Failures older than the window stop counting.
	c.loginFails["10.0.0.9"] = []time.Time{time.Now().Add(-lockoutWindow - time.Minute)}
	if w := login(c, "10.0.0.9", "correct horse"); w.Code != 200 {
		t.Fatalf("after window: %d", w.Code)
	}
}

func TestAPITokensAndSessions(t *testing.T) {
	c := authTestController(t)
	ok := func(r *http.Request) int {
		w := httptest.NewRecorder()
		c.auth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })(w, r)
		return w.Code
	}
	sessionOnly := func(r *http.Request) int {
		w := httptest.NewRecorder()
		c.authSession(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })(w, r)
		return w.Code
	}
	w := httptest.NewRecorder()
	c.handleNewAPIToken(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"ci"}`)))
	var tok struct{ ID, Token string }
	_ = json.Unmarshal(w.Body.Bytes(), &tok)
	if !strings.HasPrefix(tok.Token, "opt_") {
		t.Fatalf("token %q", tok.Token)
	}
	if b, _ := os.ReadFile(c.apiTokensPath()); bytes.Contains(b, []byte(tok.Token)) {
		t.Fatal("token stored in plain text")
	}
	req := httptest.NewRequest("GET", "/api/state", nil)
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	if ok(req) != 204 || sessionOnly(req) != 401 {
		t.Fatal("token should pass auth but not authSession")
	}
	if c.apiTokens[0].LastUsed.IsZero() {
		t.Fatal("last used not recorded")
	}
	bad := httptest.NewRequest("GET", "/", nil)
	bad.Header.Set("Authorization", "Bearer opt_wrong")
	if ok(bad) != 401 {
		t.Fatal("wrong token accepted")
	}
	// Revoke.
	rr := httptest.NewRequest("DELETE", "/", nil)
	rr.SetPathValue("id", tok.ID)
	c.handleRevokeAPIToken(httptest.NewRecorder(), rr)
	if ok(req) != 401 {
		t.Fatal("revoked token still works")
	}

	// Sessions: list, revoke one, sign out everywhere.
	lw := login(c, "10.0.0.5", "correct horse")
	cookie := lw.Result().Cookies()[0]
	login(c, "10.0.0.6", "correct horse")
	lr := httptest.NewRequest("GET", "/api/sessions", nil)
	lr.AddCookie(cookie)
	sw := httptest.NewRecorder()
	c.handleListSessions(sw, lr)
	var list []map[string]any
	_ = json.Unmarshal(sw.Body.Bytes(), &list)
	if len(list) != 2 {
		t.Fatalf("sessions: %s", sw.Body.String())
	}
	cur := 0
	for _, s := range list {
		if s["current"] == true {
			cur++
		}
	}
	if cur != 1 {
		t.Fatalf("current flag on %d sessions", cur)
	}
	c.handleLogoutAll(httptest.NewRecorder(), lr)
	cr := httptest.NewRequest("GET", "/", nil)
	cr.AddCookie(cookie)
	if ok(cr) != 401 {
		t.Fatal("session survived sign out everywhere")
	}
}

func TestScheduledBackupPrunes(t *testing.T) {
	c := authTestController(t)
	out := t.TempDir()
	enc, err := c.encrypt("a long passphrase")
	if err != nil {
		t.Fatal(err)
	}
	c.settings = Settings{BackupEnabled: true, BackupDir: out, BackupPassEnc: enc}
	for i := 1; i <= 9; i++ { // nine old copies
		p := filepath.Join(out, "outpost-backup-2020010"+string(rune('0'+i))+"-000000.opb")
		_ = os.WriteFile(p, []byte("x"), 0o600)
		old := time.Now().Add(-time.Duration(30-i) * 24 * time.Hour)
		_ = os.Chtimes(p, old, old)
	}
	if err := c.runScheduledBackup(); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(out, "outpost-backup-*.opb"))
	if len(files) != backupKeep {
		t.Fatalf("kept %d files, want %d", len(files), backupKeep)
	}
	// A second run the same day does nothing.
	if err := c.runScheduledBackup(); err != nil {
		t.Fatal(err)
	}
	if again, _ := filepath.Glob(filepath.Join(out, "outpost-backup-*.opb")); len(again) != backupKeep {
		t.Fatal("ran twice in one day")
	}
}

func TestSetTLSCertificate(t *testing.T) {
	c := authTestController(t)
	cert, fp, err := loadOrCreateCert(c.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	c.cert, c.fingerprint = cert, fp
	c.nodes = map[string]*NodeRec{"n1": {ID: "n1"}}
	c.conns = map[string]*nodeConn{}

	other := t.TempDir()
	_, fp2, _ := loadOrCreateCert(other)
	crt, _ := os.ReadFile(filepath.Join(other, "tls.crt"))
	key, _ := os.ReadFile(filepath.Join(other, "tls.key"))
	body, _ := json.Marshal(map[string]string{"cert": string(crt), "key": string(key)})

	w := httptest.NewRecorder()
	c.handleSetTLS(w, httptest.NewRequest("POST", "/api/tls", bytes.NewReader(body)))
	if w.Code != 200 || c.fingerprint != fp2 {
		t.Fatalf("swap failed: %d %s", w.Code, w.Body.String())
	}
	if b, _ := os.ReadFile(filepath.Join(c.dataDir, "tls.crt")); !bytes.Equal(b, crt) {
		t.Fatal("certificate not saved")
	}
	// The node was offline, so it needs a manual re-pin.
	var v struct {
		NeedsRepin []string `json:"needs_repin"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	if len(v.NeedsRepin) != 1 {
		t.Fatalf("needs_repin: %s", w.Body.String())
	}
	c.clearRepin("n1")
	if v := c.tlsView()["needs_repin"].([]string); len(v) != 0 {
		t.Fatal("repin not cleared")
	}
	// Mismatched key is refused.
	bad, _ := json.Marshal(map[string]string{"cert": string(crt), "key": "nope"})
	w = httptest.NewRecorder()
	c.handleSetTLS(w, httptest.NewRequest("POST", "/api/tls", bytes.NewReader(bad)))
	if w.Code != 400 {
		t.Fatalf("bad key: %d", w.Code)
	}
}

// A certificate that cannot be saved must not be switched to: the controller would
// come back with the old one after a restart while the nodes pin the new one.
func TestSetTLSCertificateSaveFailureKeepsOld(t *testing.T) {
	c := authTestController(t)
	cert, fp, err := loadOrCreateCert(c.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	c.cert, c.fingerprint = cert, fp
	c.nodes = map[string]*NodeRec{}
	c.conns = map[string]*nodeConn{}
	oldCrt, _ := os.ReadFile(filepath.Join(c.dataDir, "tls.crt"))

	// Make the key path unwritable in a way that also holds for root: a non-empty folder.
	keyPath := filepath.Join(c.dataDir, "tls.key")
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(keyPath, "x"), 0o755); err != nil {
		t.Fatal(err)
	}

	other := t.TempDir()
	_, _, _ = loadOrCreateCert(other)
	crt, _ := os.ReadFile(filepath.Join(other, "tls.crt"))
	key, _ := os.ReadFile(filepath.Join(other, "tls.key"))
	body, _ := json.Marshal(map[string]string{"cert": string(crt), "key": string(key)})

	w := httptest.NewRecorder()
	c.handleSetTLS(w, httptest.NewRequest("POST", "/api/tls", bytes.NewReader(body)))
	if w.Code != 500 {
		t.Fatalf("want 500 when the key cannot be saved, got %d %s", w.Code, w.Body.String())
	}
	if c.fingerprint != fp {
		t.Fatal("switched to a certificate that was not saved")
	}
	if b, _ := os.ReadFile(filepath.Join(c.dataDir, "tls.crt")); !bytes.Equal(b, oldCrt) {
		t.Fatal("tls.crt on disk no longer matches the key the controller will load")
	}
}

func setupPost(c *Controller, ip, pw string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/setup", strings.NewReader(`{"password":"`+pw+`"}`))
	r.RemoteAddr = ip + ":1234"
	w := httptest.NewRecorder()
	c.handleSetup(w, r)
	return w
}

func freshController(t *testing.T) *Controller {
	t.Helper()
	c := &Controller{dataDir: t.TempDir(), sessions: map[string]time.Time{}, sessMeta: map[string]sessionMeta{},
		loginFails: map[string][]time.Time{}}
	if err := c.loadAdmin(false); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFirstRunSetup(t *testing.T) {
	c := freshController(t)
	if !c.needsSetup() || c.checkPassword("") || c.checkPassword("anything") {
		t.Fatal("a fresh controller must have no password and accept none")
	}
	if w := setupPost(c, "10.0.0.5", "short"); w.Code != 400 {
		t.Fatalf("short password: %d", w.Code)
	}
	w := setupPost(c, "10.0.0.5", "a long enough one")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Set-Cookie"), "outpost_session=") {
		t.Fatalf("setup: %d %v", w.Code, w.Header())
	}
	if c.needsSetup() || !c.checkPassword("a long enough one") {
		t.Fatal("password not stored")
	}
	// Never again once a password exists.
	if w := setupPost(c, "10.0.0.5", "another password"); w.Code != 409 || !c.checkPassword("a long enough one") {
		t.Fatalf("second setup: %d", w.Code)
	}
	// Survives a restart.
	c2 := &Controller{dataDir: c.dataDir}
	c2.loadAdmin(false)
	if c2.needsSetup() || !c2.checkPassword("a long enough one") {
		t.Fatal("password lost on reload")
	}
}

func TestSetupAddressRestriction(t *testing.T) {
	c := freshController(t)
	for ip, want := range map[string]bool{"10.1.2.3": true, "192.168.0.9": true, "127.0.0.1": true, "::1": true, "fd00::1": true, "8.8.8.8": false, "2001:4860::1": false} {
		if c.setupAllowed(ip) != want {
			t.Errorf("default allow %s = %v, want %v", ip, !want, want)
		}
	}
	if w := setupPost(c, "8.8.8.8", "a long enough one"); w.Code != 403 || !c.needsSetup() {
		t.Fatalf("public address: %d", w.Code)
	}
	var err error
	if c.setupAllow, err = parseSetupAllow("203.0.113.0/24, 10.9.9.9"); err != nil {
		t.Fatal(err)
	}
	if !c.setupAllowed("203.0.113.7") || !c.setupAllowed("10.9.9.9") || c.setupAllowed("10.0.0.1") {
		t.Fatal("explicit list not honoured")
	}
	c.setupAllow, _ = parseSetupAllow("any")
	if !c.setupAllowed("8.8.8.8") {
		t.Fatal("any should allow everything")
	}
	if _, err := parseSetupAllow("nonsense"); err == nil {
		t.Fatal("bad CIDR accepted")
	}
}

func TestSetupRateLimitAndReset(t *testing.T) {
	c := freshController(t)
	c.setupAllow, _ = parseSetupAllow("10.0.0.0/8")
	for i := 0; i < lockoutFails; i++ {
		setupPost(c, "8.8.4.4", "a long enough one")
	}
	if w := setupPost(c, "8.8.4.4", "a long enough one"); w.Code != 429 {
		t.Fatalf("expected lockout, got %d", w.Code)
	}
	// Reset removes the password and nothing generated is left behind.
	if w := setupPost(c, "10.0.0.5", "a long enough one"); w.Code != 200 {
		t.Fatal("setup failed")
	}
	if err := c.loadAdmin(true); err != nil {
		t.Fatal(err)
	}
	c2 := &Controller{dataDir: c.dataDir}
	c2.loadAdmin(false)
	if !c2.needsSetup() {
		t.Fatal("reset should return to first-run state")
	}
}
