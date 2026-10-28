package main

import (
	"bufio"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The admin password lives in <data>/admin.json as a PBKDF2-SHA256 hash, so it
// persists across restarts, reboots and updates. Records from 0.1–0.2 (a plain
// salted SHA-256) still verify and are upgraded on the next successful sign-in.

const pbkdf2Iter = 600000

type adminRecord struct {
	Algo string `json:"algo,omitempty"` // "" = legacy salted sha256
	Iter int    `json:"iter,omitempty"`
	Salt string `json:"Salt"`
	Hash string `json:"Hash"`
	// Initial is true only while the password is the one Outpost generated;
	// absent (false) on records from earlier versions, so they are never nagged.
	Initial bool `json:"initial,omitempty"`
}

func (c *Controller) adminPath() string { return filepath.Join(c.dataDir, "admin.json") }

func hashPassword(pw, salt string, iter int) string {
	k, err := pbkdf2.Key(sha256.New, pw, []byte(salt), iter, 32)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(k)
}

// setAdminPassword stores a password the user chose; it clears the initial flag.
func (c *Controller) setAdminPassword(pw string) error { return c.storeAdminPassword(pw, false) }

// storeAdminPassword stores a new password hash on disk and in memory. initial
// marks an auto-generated password the web UI should ask the user to replace.
func (c *Controller) storeAdminPassword(pw string, initial bool) error {
	rec := adminRecord{Algo: "pbkdf2-sha256", Iter: pbkdf2Iter, Salt: randHex(16), Initial: initial}
	rec.Hash = hashPassword(pw, rec.Salt, rec.Iter)
	b, _ := json.Marshal(rec)
	tmp := c.adminPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.adminPath()); err != nil {
		return err
	}
	c.mu.Lock()
	c.admin = rec
	c.mu.Unlock()
	return nil
}

// loadOrCreateAdmin loads admin.json, or creates a random password when the file
// is missing or reset is true. It returns the new password, or "" if unchanged.
func (c *Controller) loadOrCreateAdmin(reset bool) (string, error) {
	if b, err := os.ReadFile(c.adminPath()); err == nil && !reset {
		var rec adminRecord
		if err := json.Unmarshal(b, &rec); err == nil && rec.Hash != "" {
			c.mu.Lock()
			c.admin = rec
			c.mu.Unlock()
			return "", nil
		}
	}
	pw := randHex(8)
	return pw, c.storeAdminPassword(pw, true)
}

func (c *Controller) checkPassword(pw string) bool {
	c.mu.Lock()
	rec := c.admin
	c.mu.Unlock()
	var got string
	if rec.Algo == "pbkdf2-sha256" {
		got = hashPassword(pw, rec.Salt, rec.Iter)
	} else {
		got = hashSecret(rec.Salt, pw)
	}
	if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(rec.Hash)) != 1 {
		return false
	}
	if rec.Algo != "pbkdf2-sha256" {
		_ = c.storeAdminPassword(pw, rec.Initial) // upgrade the stored hash
	}
	return true
}

func validNewPassword(pw string) error {
	if len(pw) < 8 {
		return errors.New("use at least 8 characters")
	}
	if len(pw) > 256 {
		return errors.New("password is too long")
	}
	return nil
}

// handleChangePassword lets the signed-in admin choose a new password. Other
// browser sessions are signed out; this one stays signed in.
func (c *Controller) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad json"})
		return
	}
	if !c.checkPassword(req.Current) {
		time.Sleep(500 * time.Millisecond)
		writeJSON(w, 403, map[string]string{"error": "current password is wrong"})
		return
	}
	if err := validNewPassword(req.New); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if err := c.setAdminPassword(req.New); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	keep := ""
	if ck, err := r.Cookie("outpost_session"); err == nil {
		keep = ck.Value
	}
	c.mu.Lock()
	for s := range c.sessions {
		if s != keep {
			delete(c.sessions, s)
		}
	}
	c.saveSessionsLocked()
	c.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// readPassword prompts on the terminal without echo (Linux/macOS).
func readPassword(prompt string) string {
	fmt.Print(prompt)
	echoOff := false
	if st, err := os.Stdin.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 && runtime.GOOS != "windows" {
		cmd := exec.Command("stty", "-echo")
		cmd.Stdin = os.Stdin
		echoOff = cmd.Run() == nil
	}
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if echoOff {
		cmd := exec.Command("stty", "echo")
		cmd.Stdin = os.Stdin
		_ = cmd.Run()
		fmt.Println()
	}
	return strings.TrimRight(line, "\r\n")
}
