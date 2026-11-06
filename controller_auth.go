package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Login hardening (0.7): failed-login lockout per IP, a list of signed-in
// sessions, and named API tokens for scripts. Tokens are stored hashed.

const (
	lockoutFails  = 10
	lockoutWindow = 15 * time.Minute
	sessionTTL    = 7 * 24 * time.Hour
)

type sessionMeta struct {
	Created time.Time `json:"created"`
	IP      string    `json:"ip"`
	UA      string    `json:"ua"`
}

type apiToken struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Hash     string    `json:"hash"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"last_used,omitempty"`
	// Scope "chat" limits the token to the OpenAI-compatible /v1 API; "" is full access.
	Scope string `json:"scope,omitempty"`
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// lockedLocked reports whether ip is locked out; must hold c.mu.
func (c *Controller) lockedLocked(ip string, now time.Time) (bool, time.Time) {
	var keep []time.Time
	for _, t := range c.loginFails[ip] {
		if now.Sub(t) < lockoutWindow {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(c.loginFails, ip)
		return false, time.Time{}
	}
	c.loginFails[ip] = keep
	if len(keep) >= lockoutFails {
		return true, keep[len(keep)-1].Add(lockoutWindow)
	}
	return false, time.Time{}
}

func (c *Controller) lockoutStatus() (failures int, locked []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for ip := range c.loginFails {
		if l, _ := c.lockedLocked(ip, now); l {
			locked = append(locked, ip)
		}
	}
	for _, v := range c.loginFails {
		failures += len(v)
	}
	sort.Strings(locked)
	return
}

func sessionID(cookie string) string { return hashSecret("session-id", cookie)[:12] }

func hashAPIToken(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// newSessionLocked creates a session for a login; must hold c.mu.
func (c *Controller) newSessionLocked(r *http.Request) string {
	tok := randHex(32)
	now := time.Now()
	c.sessions[tok] = now.Add(sessionTTL)
	if c.sessMeta == nil {
		c.sessMeta = map[string]sessionMeta{}
	}
	ua := r.UserAgent()
	if len(ua) > 200 {
		ua = ua[:200]
	}
	c.sessMeta[tok] = sessionMeta{Created: now, IP: clientIP(r), UA: ua}
	c.saveSessionsLocked()
	return tok
}

// bearerAPIToken returns the index of the API token in the request, or -1.
func (c *Controller) bearerAPIToken(r *http.Request) int {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return -1
	}
	sum := hashAPIToken(strings.TrimSpace(h[7:]))
	for i, t := range c.apiTokens {
		if subtle.ConstantTimeCompare([]byte(sum), []byte(t.Hash)) == 1 {
			return i
		}
	}
	return -1
}

// authSession is like auth but accepts only a browser session: managing
// tokens, sessions, TLS and backups is not something a script token may do.
func (c *Controller) authSession(h http.HandlerFunc) http.HandlerFunc {
	return c.authWith(h, false)
}

func (c *Controller) auth(h http.HandlerFunc) http.HandlerFunc { return c.authWith(h, true) }

func (c *Controller) authWith(h http.HandlerFunc, allowToken bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok := false
		c.mu.Lock()
		if ck, err := r.Cookie("outpost_session"); err == nil {
			if exp, found := c.sessions[ck.Value]; found && time.Now().Before(exp) {
				ok = true
			}
		}
		if !ok && allowToken {
			if i := c.bearerAPIToken(r); i >= 0 && c.apiTokens[i].Scope == "" {
				ok = true
				if time.Since(c.apiTokens[i].LastUsed) > time.Minute {
					c.apiTokens[i].LastUsed = time.Now()
					c.saveAPITokensLocked()
				}
			}
		}
		c.mu.Unlock()
		if !ok {
			writeJSON(w, 401, map[string]string{"error": "login required"})
			return
		}
		h(w, r)
	}
}

func (c *Controller) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	c.mu.Lock()
	if c.loginFails == nil {
		c.loginFails = map[string][]time.Time{}
	}
	locked, until := c.lockedLocked(ip, time.Now())
	c.mu.Unlock()
	if locked {
		w.Header().Set("Retry-After", strconv.Itoa(int(time.Until(until).Seconds())+1))
		writeJSON(w, 429, map[string]string{"error": "too many failed sign-ins from this address; try again in " + time.Until(until).Round(time.Minute).String()})
		return
	}
	var req struct{ Password string }
	_ = json.NewDecoder(r.Body).Decode(&req)
	if !c.checkPassword(req.Password) {
		c.mu.Lock()
		c.loginFails[ip] = append(c.loginFails[ip], time.Now())
		c.mu.Unlock()
		time.Sleep(500 * time.Millisecond)
		writeJSON(w, 401, map[string]string{"error": "wrong password"})
		return
	}
	c.mu.Lock()
	delete(c.loginFails, ip)
	tok := c.newSessionLocked(r)
	c.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "outpost_session", Value: tok, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds())})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleLogout(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie("outpost_session"); err == nil {
		c.mu.Lock()
		delete(c.sessions, ck.Value)
		delete(c.sessMeta, ck.Value)
		c.saveSessionsLocked()
		c.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "outpost_session", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- sessions list ----

func (c *Controller) handleListSessions(w http.ResponseWriter, r *http.Request) {
	cur := ""
	if ck, err := r.Cookie("outpost_session"); err == nil {
		cur = ck.Value
	}
	c.mu.Lock()
	out := []map[string]any{}
	for tok, exp := range c.sessions {
		if time.Now().After(exp) {
			continue
		}
		m := c.sessMeta[tok]
		out = append(out, map[string]any{"id": sessionID(tok), "created": m.Created, "expires": exp, "ip": m.IP, "ua": m.UA, "current": tok == cur})
	}
	c.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i]["created"].(time.Time).After(out[j]["created"].(time.Time)) })
	writeJSON(w, 200, out)
}

func (c *Controller) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c.mu.Lock()
	for tok := range c.sessions {
		if sessionID(tok) == id {
			delete(c.sessions, tok)
			delete(c.sessMeta, tok)
		}
	}
	c.saveSessionsLocked()
	c.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleLogoutAll(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.sessions = map[string]time.Time{}
	c.sessMeta = map[string]sessionMeta{}
	c.saveSessionsLocked()
	c.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "outpost_session", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- API tokens ----

func (c *Controller) apiTokensPath() string { return filepath.Join(c.dataDir, "api_tokens.json") }

func (c *Controller) loadAPITokens() {
	if b, err := os.ReadFile(c.apiTokensPath()); err == nil {
		_ = json.Unmarshal(b, &c.apiTokens)
	}
}

// saveAPITokensLocked must be called with c.mu held.
func (c *Controller) saveAPITokensLocked() {
	b, _ := json.MarshalIndent(c.apiTokens, "", "  ")
	_ = writeFileAtomic(c.apiTokensPath(), b, 0o600)
}

func (c *Controller) handleListAPITokens(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	out := []map[string]any{}
	for _, t := range c.apiTokens {
		out = append(out, map[string]any{"id": t.ID, "name": t.Name, "created": t.Created, "last_used": t.LastUsed, "scope": t.Scope})
	}
	c.mu.Unlock()
	writeJSON(w, 200, out)
}

func (c *Controller) handleNewAPIToken(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, Scope string }
	_ = json.NewDecoder(r.Body).Decode(&req)
	name := strings.TrimSpace(req.Name)
	if req.Scope != "" && req.Scope != "chat" {
		writeJSON(w, 400, map[string]string{"error": "scope must be empty (full access) or chat"})
		return
	}
	if name == "" || len(name) > 60 {
		writeJSON(w, 400, map[string]string{"error": "give the token a name (up to 60 characters)"})
		return
	}
	secret := "opt_" + randHex(24)
	c.mu.Lock()
	if len(c.apiTokens) >= 50 {
		c.mu.Unlock()
		writeJSON(w, 400, map[string]string{"error": "too many tokens; revoke some first"})
		return
	}
	t := apiToken{ID: randHex(6), Name: name, Hash: hashAPIToken(secret), Created: time.Now(), Scope: req.Scope}
	c.apiTokens = append(c.apiTokens, t)
	c.saveAPITokensLocked()
	c.mu.Unlock()
	writeJSON(w, 200, map[string]any{"id": t.ID, "name": t.Name, "token": secret})
}

func (c *Controller) handleRevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c.mu.Lock()
	keep := c.apiTokens[:0]
	for _, t := range c.apiTokens {
		if t.ID != id {
			keep = append(keep, t)
		}
	}
	c.apiTokens = keep
	c.saveAPITokensLocked()
	c.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}
