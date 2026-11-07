package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// backupKeep is how many scheduled backups are kept.
const backupKeep = 7

// backupCheck is the result of checking the newest scheduled backup.
type backupCheck struct {
	File     string    `json:"file"`
	At       time.Time `json:"at"`
	Files    int       `json:"files"`
	Verified bool      `json:"verified"`
	Error    string    `json:"error,omitempty"`
}

// runRestore is `outpost install controller --restore FILE`.
func runRestore(file, dir string, force bool) error {
	if _, err := os.Stat(filepath.Join(dir, "admin.json")); err == nil && !force {
		return fmt.Errorf("%s already holds a controller; restore is for a fresh machine (add --force to overwrite)", dir)
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	pass := os.Getenv("OUTPOST_BACKUP_PASSPHRASE")
	if pass == "" {
		pass = readPassword("Backup passphrase: ")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	n, err := restoreBackup(f, dir, pass)
	if err != nil {
		return err
	}
	fmt.Printf("Restored %d files into %s. Nodes reconnect on their own: their keys and the TLS certificate are unchanged.\n", n, dir)
	return nil
}

// ---- web ----

func (c *Controller) handleBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Passphrase string `json:"passphrase"`
		Big        bool   `json:"include_large"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if len(req.Passphrase) < 8 {
		writeJSON(w, 400, map[string]string{"error": "the passphrase needs 8 or more characters"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+backupName()+`"`)
	if err := writeBackup(w, c.dataDir, req.Passphrase, req.Big); err != nil {
		log.Printf("backup: %v", err)
	}
}

func backupName() string { return "outpost-backup-" + time.Now().Format("20060102-150405") + ".opb" }

// handleBackupSchedule saves the nightly-copy settings. The passphrase is kept
// encrypted with secret.key (so it is only as safe as the controller itself).
func (c *Controller) handleBackupSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled    bool   `json:"enabled"`
		Dir        string `json:"dir"`
		Passphrase string `json:"passphrase"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	req.Dir = strings.TrimSpace(req.Dir)
	if req.Enabled {
		if req.Dir == "" || !filepath.IsAbs(req.Dir) {
			writeJSON(w, 400, map[string]string{"error": "give the full path of a folder on the controller, e.g. /mnt/nas/outpost"})
			return
		}
		if err := os.MkdirAll(req.Dir, 0o700); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
	}
	enc := ""
	if req.Passphrase != "" {
		if len(req.Passphrase) < 8 {
			writeJSON(w, 400, map[string]string{"error": "the passphrase needs 8 or more characters"})
			return
		}
		enc, _ = c.encrypt(req.Passphrase)
	}
	c.mu.Lock()
	if enc != "" {
		c.settings.BackupPassEnc = enc
	}
	if req.Enabled && c.settings.BackupPassEnc == "" {
		c.mu.Unlock()
		writeJSON(w, 400, map[string]string{"error": "set a passphrase for the scheduled backups"})
		return
	}
	c.settings.BackupEnabled, c.settings.BackupDir = req.Enabled, req.Dir
	err := c.saveSettingsLocked()
	c.mu.Unlock()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if req.Enabled {
		go c.scheduledBackup() // the first copy now (if none is recent), not up to an hour later
	}
	writeJSON(w, 200, c.settingsView())
}

// runScheduledBackup writes one backup now if the schedule is on and the
// newest copy is over a day old; it keeps the last backupKeep copies.
func (c *Controller) runScheduledBackup() error {
	c.mu.Lock()
	on, dir, enc := c.settings.BackupEnabled, c.settings.BackupDir, c.settings.BackupPassEnc
	c.mu.Unlock()
	if !on || dir == "" {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(dir, "outpost-backup-*.opb"))
	sort.Strings(files)
	if len(files) > 0 {
		if st, err := os.Stat(files[len(files)-1]); err == nil && time.Since(st.ModTime()) < 23*time.Hour {
			c.mu.Lock()
			known := c.lastBackup != nil && c.lastBackup.Verified
			c.mu.Unlock()
			if !known { // after a restart: check the newest copy once, so Settings can say it is good
				if pass, err := c.decrypt(enc); err == nil && pass != "" {
					c.checkBackup(files[len(files)-1], pass)
				}
			}
			return nil
		}
	}
	pass, err := c.decrypt(enc)
	if err != nil || pass == "" {
		return errors.New("scheduled backup: passphrase unreadable")
	}
	path := filepath.Join(dir, backupName())
	f, err := os.Create(path + ".part")
	if err != nil {
		return err
	}
	err = writeBackup(f, c.dataDir, pass, false)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path + ".part")
		return err
	}
	if err := os.Rename(path+".part", path); err != nil {
		return err
	}
	files = append(files, path)
	for len(files) > backupKeep {
		os.Remove(files[0])
		files = files[1:]
	}
	log.Printf("backup: wrote %s", path)
	c.checkBackup(path, pass)
	return nil
}

// checkBackup reads back a backup just written and records the result for
// Settings; a failure is also sent as a notification.
func (c *Controller) checkBackup(path, pass string) {
	n, err := verifyBackup(path, pass)
	bc := &backupCheck{File: filepath.Base(path), At: time.Now(), Files: n, Verified: err == nil}
	if err != nil {
		bc.Error = "could not be read back: " + err.Error()
		log.Printf("backup: %s failed its check: %v", path, err)
	} else {
		log.Printf("backup: %s verified (%d files)", path, n)
	}
	c.recordBackup(bc)
}

// recordBackup keeps the result for Settings and sends a notification when a
// backup fails, once per distinct failure (the loop retries every hour).
func (c *Controller) recordBackup(bc *backupCheck) {
	c.mu.Lock()
	prev := c.lastBackup
	c.lastBackup = bc
	c.mu.Unlock()
	if bc.Error != "" && (prev == nil || prev.Error != bc.Error) {
		what := "The nightly backup"
		if bc.File != "" {
			what = bc.File
		}
		go c.deliverNotification("backup_failed", "Backup failed", what+": "+bc.Error, "")
	}
}

// backupMu keeps the hourly loop and a schedule change from writing at once.
var backupMu sync.Mutex

func (c *Controller) scheduledBackup() {
	backupMu.Lock()
	defer backupMu.Unlock()
	if err := c.runScheduledBackup(); err != nil {
		log.Printf("backup: %v", err)
		c.recordBackup(&backupCheck{At: time.Now(), Error: err.Error()})
	}
}

func (c *Controller) backupLoop() {
	for {
		c.scheduledBackup()
		time.Sleep(time.Hour)
	}
}
