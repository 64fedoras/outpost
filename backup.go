package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Backup and restore (0.7). A backup is a tar.gz of the controller's state,
// encrypted with a passphrase (AES-256-GCM in 1 MiB chunks, key from PBKDF2),
// because secret.key protects the stored Claude API key. File format:
//
//	"OUTPOSTBK1" | salt(16) | iterations(u32) | chunks of: length(u32) ciphertext
//
// Each chunk's nonce is its counter; its additional data is 1 for the final
// chunk, so a truncated file fails to open.

const (
	backupMagic = "OUTPOSTBK1"
	backupChunk = 1 << 20
	backupIter  = 300000
	backupKeep  = 7
)

// Small state files always included; the big directories only on request.
var (
	backupFiles = []string{"admin.json", "settings.json", "nodes.json", "sessions.json", "sessions.json.meta",
		"api_tokens.json", "secret.key", "tls.crt", "tls.key", "tls_repin.json"}
	backupDirs    = []string{"tasks"}
	backupBigDirs = []string{"models", "runtimes", "releases"}
)

type chunkWriter struct {
	w    io.Writer
	gcm  cipher.AEAD
	buf  []byte
	ctr  uint64
	done bool
}

func (c *chunkWriter) nonce() []byte {
	n := make([]byte, c.gcm.NonceSize())
	binary.BigEndian.PutUint64(n[4:], c.ctr)
	return n
}

func (c *chunkWriter) flush(last bool) error {
	ad := []byte{0}
	if last {
		ad[0] = 1
	}
	ct := c.gcm.Seal(nil, c.nonce(), c.buf, ad)
	c.ctr++
	c.buf = c.buf[:0]
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(ct)))
	if _, err := c.w.Write(l[:]); err != nil {
		return err
	}
	_, err := c.w.Write(ct)
	return err
}

func (c *chunkWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		k := min(backupChunk-len(c.buf), len(p))
		c.buf = append(c.buf, p[:k]...)
		p = p[k:]
		if len(c.buf) == backupChunk {
			if err := c.flush(false); err != nil {
				return 0, err
			}
		}
	}
	return n, nil
}

func (c *chunkWriter) Close() error { return c.flush(true) }

func newBackupWriter(w io.Writer, pass string) (io.WriteCloser, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key, err := pbkdf2.Key(sha256.New, pass, salt, backupIter, 32)
	if err != nil {
		return nil, err
	}
	blk, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(blk)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], backupIter)
	if _, err := w.Write(append(append([]byte(backupMagic), salt...), hdr[:]...)); err != nil {
		return nil, err
	}
	return &chunkWriter{w: w, gcm: gcm}, nil
}

type chunkReader struct {
	r    *bufio.Reader
	gcm  cipher.AEAD
	ctr  uint64
	buf  []byte
	last bool
}

var errBadBackup = errors.New("wrong passphrase, or the backup is damaged or incomplete")

func (c *chunkReader) Read(p []byte) (int, error) {
	for len(c.buf) == 0 {
		if c.last {
			return 0, io.EOF
		}
		var l [4]byte
		if _, err := io.ReadFull(c.r, l[:]); err != nil {
			return 0, errBadBackup
		}
		n := binary.BigEndian.Uint32(l[:])
		if n > backupChunk+64 {
			return 0, errBadBackup
		}
		ct := make([]byte, n)
		if _, err := io.ReadFull(c.r, ct); err != nil {
			return 0, errBadBackup
		}
		nonce := make([]byte, c.gcm.NonceSize())
		binary.BigEndian.PutUint64(nonce[4:], c.ctr)
		pt, err := c.gcm.Open(nil, nonce, ct, []byte{0})
		if err != nil {
			// maybe the final chunk
			if pt, err = c.gcm.Open(nil, nonce, ct, []byte{1}); err != nil {
				return 0, errBadBackup
			}
			c.last = true
		}
		c.ctr++
		c.buf = pt
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

func newBackupReader(r io.Reader, pass string) (io.Reader, error) {
	br := bufio.NewReader(r)
	hdr := make([]byte, len(backupMagic)+16+4)
	if _, err := io.ReadFull(br, hdr); err != nil || string(hdr[:len(backupMagic)]) != backupMagic {
		return nil, errors.New("this is not an Outpost backup")
	}
	salt := hdr[len(backupMagic) : len(backupMagic)+16]
	iter := int(binary.BigEndian.Uint32(hdr[len(backupMagic)+16:]))
	if iter < 1000 || iter > 5_000_000 {
		return nil, errors.New("unsupported backup header")
	}
	key, err := pbkdf2.Key(sha256.New, pass, salt, iter, 32)
	if err != nil {
		return nil, err
	}
	blk, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(blk)
	return &chunkReader{r: br, gcm: gcm}, nil
}

// writeBackup writes the encrypted archive of dir to w.
func writeBackup(w io.Writer, dir, pass string, big bool) error {
	ew, err := newBackupWriter(w, pass)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(ew)
	tw := tar.NewWriter(gz)
	add := func(rel string) error {
		return filepath.WalkDir(filepath.Join(dir, rel), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			name, _ := filepath.Rel(dir, p)
			h := &tar.Header{Name: filepath.ToSlash(name), Mode: 0o600, Size: info.Size(), ModTime: info.ModTime()}
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		})
	}
	items := append([]string{}, backupFiles...)
	items = append(items, backupDirs...)
	if big {
		items = append(items, backupBigDirs...)
	}
	for _, it := range items {
		if err := add(it); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return ew.Close()
}

// restoreBackup unpacks an archive into dir, refusing paths outside it.
func restoreBackup(r io.Reader, dir, pass string) (int, error) {
	dr, err := newBackupReader(r, pass)
	if err != nil {
		return 0, err
	}
	gz, err := gzip.NewReader(dr)
	if err != nil {
		return 0, errBadBackup
	}
	tr := tar.NewReader(gz)
	n := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, errBadBackup
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		clean := filepath.Clean(filepath.FromSlash(h.Name))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return n, fmt.Errorf("unsafe path in backup: %s", h.Name)
		}
		dst := filepath.Join(dir, clean)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return n, err
		}
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return n, err
		}
		_, err = io.Copy(f, tr)
		f.Close()
		if err != nil {
			return n, errBadBackup
		}
		n++
	}
	// Drain so the final-chunk check runs: a truncated file must not pass.
	if _, err := io.Copy(io.Discard, dr); err != nil {
		return n, err
	}
	return n, nil
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
	return nil
}

func (c *Controller) backupLoop() {
	for {
		if err := c.runScheduledBackup(); err != nil {
			log.Printf("backup: %v", err)
		}
		time.Sleep(time.Hour)
	}
}
