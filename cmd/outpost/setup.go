package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// First-run setup: a controller with no admin password lets the first visitor
// choose one in the web UI. There is no generated password and no token in the
// log, so the window is guarded instead: only private/LAN addresses by default
// (or --setup-allow-from), over the controller's TLS port, rate limited, and the
// endpoint refuses once a password exists.

// parseSetupAllow turns the --setup-allow-from value into prefixes. Empty means
// the default (private and loopback addresses); "any" allows every address.
func parseSetupAllow(v string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		switch {
		case part == "":
		case strings.EqualFold(part, "any"):
			out = append(out, netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0"))
		default:
			p, err := netip.ParsePrefix(part)
			if err != nil {
				a, aerr := netip.ParseAddr(part)
				if aerr != nil {
					return nil, fmt.Errorf("--setup-allow-from: %q is not a CIDR or address", part)
				}
				p = netip.PrefixFrom(a, a.BitLen())
			}
			out = append(out, p.Masked())
		}
	}
	return out, nil
}

func describeSetupAllow(allow []netip.Prefix) string {
	if len(allow) == 0 {
		return "private/LAN addresses only"
	}
	parts := make([]string, len(allow))
	for i, p := range allow {
		parts[i] = p.String()
	}
	return strings.Join(parts, ", ")
}

// setupAllowed reports whether ip may choose the first password.
func (c *Controller) setupAllowed(ip string) bool {
	a, err := netip.ParseAddr(strings.SplitN(ip, "%", 2)[0])
	if err != nil {
		return false
	}
	a = a.Unmap()
	if len(c.setupAllow) == 0 {
		return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast()
	}
	for _, p := range c.setupAllow {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// handleSetupStatus tells the login page whether to show "choose a password".
func (c *Controller) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	if !c.needsSetup() {
		writeJSON(w, 200, map[string]bool{"needed": false})
		return
	}
	writeJSON(w, 200, map[string]any{"needed": true, "allowed": c.setupAllowed(clientIP(r))})
}

// handleSetup creates the first admin password and signs the caller in.
func (c *Controller) handleSetup(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	c.mu.Lock()
	if c.loginFails == nil {
		c.loginFails = map[string][]time.Time{}
	}
	locked, until := c.lockedLocked(ip, time.Now())
	c.mu.Unlock()
	if locked {
		writeJSON(w, 429, map[string]string{"error": "too many attempts from this address; try again in " + time.Until(until).Round(time.Minute).String()})
		return
	}
	fail := func(code int, msg string) {
		c.mu.Lock()
		c.loginFails[ip] = append(c.loginFails[ip], time.Now())
		c.mu.Unlock()
		time.Sleep(500 * time.Millisecond)
		writeJSON(w, code, map[string]string{"error": msg})
	}
	if !c.setupAllowed(ip) {
		fail(403, "first-run setup is not allowed from this address; open the web UI from the local network, or start the controller with --setup-allow-from")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		fail(400, "bad json")
		return
	}
	if err := validNewPassword(req.Password); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	c.setupMu.Lock()
	defer c.setupMu.Unlock()
	if !c.needsSetup() {
		fail(409, "a password is already set; sign in instead")
		return
	}
	if err := c.setAdminPassword(req.Password); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	c.mu.Lock()
	tok := c.newSessionLocked(r)
	c.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "outpost_session", Value: tok, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds())})
	writeJSON(w, 200, map[string]bool{"ok": true})
}
