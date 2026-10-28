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

func TestBackupRoundTrip(t *testing.T) {
	src := t.TempDir()
	must := func(p, s string) {
		t.Helper()
		full := filepath.Join(src, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o700)
		if err := os.WriteFile(full, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	must("admin.json", "{}")
	must("secret.key", "0123456789abcdef0123456789abcdef")
	must("tasks/t1/task.json", "task")
	must("models/big.gguf", "weights")
	big := strings.Repeat("x", 3*backupChunk+17) // spans several encrypted chunks
	must("tasks/t1/big.txt", big)

	var buf bytes.Buffer
	if err := writeBackup(&buf, src, "hunter2hunter2", false); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("0123456789abcdef")) {
		t.Fatal("backup is not encrypted")
	}
	dst := t.TempDir()
	n, err := restoreBackup(bytes.NewReader(buf.Bytes()), dst, "hunter2hunter2")
	if err != nil || n != 4 {
		t.Fatalf("restore: %d files, %v", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "tasks/t1/big.txt")); string(b) != big {
		t.Fatal("large file corrupted")
	}
	if _, err := os.Stat(filepath.Join(dst, "models/big.gguf")); err == nil {
		t.Fatal("models should be left out by default")
	}
	if _, err := restoreBackup(bytes.NewReader(buf.Bytes()), t.TempDir(), "wrong passphrase"); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	// A truncated backup must fail, not restore silently.
	if _, err := restoreBackup(bytes.NewReader(buf.Bytes()[:buf.Len()-40]), t.TempDir(), "hunter2hunter2"); err == nil {
		t.Fatal("truncated backup accepted")
	}
	var bigBuf bytes.Buffer
	if err := writeBackup(&bigBuf, src, "hunter2hunter2", true); err != nil {
		t.Fatal(err)
	}
	d2 := t.TempDir()
	if _, err := restoreBackup(&bigBuf, d2, "hunter2hunter2"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d2, "models/big.gguf")); err != nil {
		t.Fatal("include_large should keep models")
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

// The initial flag is set only for generated passwords and cleared by a chosen one.
func TestInitialPasswordFlag(t *testing.T) {
	dir := t.TempDir()
	c := &Controller{dataDir: dir, sessions: map[string]time.Time{}, sessMeta: map[string]sessionMeta{}}
	pw, err := c.loadOrCreateAdmin(false)
	if err != nil || pw == "" || !c.admin.Initial {
		t.Fatalf("generated password should be initial: %q %v %v", pw, err, c.admin.Initial)
	}
	// Survives a restart and a legacy-style rehash on sign-in.
	c2 := &Controller{dataDir: dir, sessions: map[string]time.Time{}, sessMeta: map[string]sessionMeta{}}
	if p, _ := c2.loadOrCreateAdmin(false); p != "" || !c2.admin.Initial || !c2.checkPassword(pw) {
		t.Fatal("flag lost across reload")
	}
	// Changing the password through the API clears it.
	r := httptest.NewRequest("POST", "/api/password", strings.NewReader(`{"current":"`+pw+`","new":"my own passphrase"}`))
	w := httptest.NewRecorder()
	c2.handleChangePassword(w, r)
	if w.Code != 200 || c2.admin.Initial {
		t.Fatalf("change: %d initial=%v", w.Code, c2.admin.Initial)
	}
	c3 := &Controller{dataDir: dir}
	c3.loadOrCreateAdmin(false)
	if c3.admin.Initial {
		t.Fatal("flag came back after reload")
	}
	// A user-chosen password (reset-password path) is never initial; a generated one is.
	if err := c3.storeAdminPassword("generated1", true); err != nil || !c3.admin.Initial {
		t.Fatal("generated reset should set the flag")
	}
	if err := c3.setAdminPassword("chosen password"); err != nil || c3.admin.Initial {
		t.Fatal("chosen password should clear the flag")
	}
}

// Records written before the flag existed must not be nagged.
func TestInitialFlagAbsentIsFalse(t *testing.T) {
	c := authTestController(t)
	c2 := &Controller{dataDir: c.dataDir}
	c2.loadOrCreateAdmin(false)
	if c2.admin.Initial {
		t.Fatal("existing install flagged as initial")
	}
}
