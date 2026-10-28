package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxUploadBytes = 512 << 20

type Release struct {
	Platform string    `json:"platform"`
	Version  string    `json:"version"`
	SHA256   string    `json:"sha256"`
	Bytes    int64     `json:"bytes"`
	Uploaded time.Time `json:"uploaded"`
	FileName string    `json:"file_name"`
	Source   string    `json:"source,omitempty"` // "github v0.7.2" when fetched from a GitHub release
}

func (c *Controller) releasesDir() string { return filepath.Join(c.dataDir, "releases") }
func (c *Controller) agentDir() string    { return filepath.Join(c.dataDir, "agent") }

func (c *Controller) releasePath(platform string) string {
	goos, _, _ := strings.Cut(platform, "-")
	return filepath.Join(c.releasesDir(), platform, "outpost"+exeSuffix(goos))
}

func (c *Controller) loadReleases() {
	c.releases = map[string]*Release{}
	entries, _ := os.ReadDir(c.releasesDir())
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(c.releasesDir(), e.Name(), "release.json"))
		if err != nil {
			continue
		}
		var r Release
		if json.Unmarshal(b, &r) == nil && r.Platform == e.Name() {
			c.releases[r.Platform] = &r
		}
	}
}

// handleUploadRelease accepts one binary as the raw request body (X-Filename header).
func (c *Controller) handleUploadRelease(w http.ResponseWriter, r *http.Request) {
	if err := os.MkdirAll(c.releasesDir(), 0o700); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	tmp, err := os.CreateTemp(c.releasesDir(), ".upload-*")
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, http.MaxBytesReader(w, r.Body, maxUploadBytes))
	tmp.Close()
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "upload failed: " + err.Error()})
		return
	}
	platform, version, err := inspectBinary(tmp.Name())
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	sum, size, err := fileSHA256(tmp.Name())
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	rel := &Release{Platform: platform, Version: version, SHA256: sum, Bytes: size, Uploaded: time.Now(),
		FileName: filepath.Base(r.Header.Get("X-Filename"))}
	if err := c.storeRelease(tmp.Name(), rel); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("release uploaded: %s %s (%d bytes)", platform, version, size)
	writeJSON(w, 200, rel)
}

// storeRelease moves a checked binary (a temp file in releasesDir) into place
// as the release for rel.Platform. Uploads and GitHub downloads both end here.
func (c *Controller) storeRelease(tmp string, rel *Release) error {
	dst := c.releasePath(rel.Platform)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	_ = os.Chmod(dst, 0o755)
	b, _ := json.MarshalIndent(rel, "", "  ")
	_ = os.WriteFile(filepath.Join(filepath.Dir(dst), "release.json"), b, 0o600)
	c.mu.Lock()
	c.releases[rel.Platform] = rel
	c.mu.Unlock()
	return nil
}

func (c *Controller) handleDeleteRelease(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("platform")
	c.mu.Lock()
	_, ok := c.releases[p]
	delete(c.releases, p)
	c.mu.Unlock()
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "no such release"})
		return
	}
	_ = os.RemoveAll(filepath.Dir(c.releasePath(p)))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleServeRelease lets an authenticated node download a release binary.
func (c *Controller) handleServeRelease(w http.ResponseWriter, r *http.Request) {
	if c.nodeFromBearer(r) == nil {
		writeJSON(w, 401, ErrInfo{Code: "bad_key", Message: "unknown node key"})
		return
	}
	p := r.PathValue("platform")
	c.mu.Lock()
	_, ok := c.releases[p]
	c.mu.Unlock()
	if !ok {
		writeJSON(w, 404, ErrInfo{Code: "no_release", Message: "no release uploaded for " + p})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, c.releasePath(p))
}

// updateCandidateLocked returns the release a node could update to, if newer.
func (c *Controller) updateCandidateLocked(n *NodeRec) *Release {
	if n.Hello == nil {
		return nil
	}
	rel := c.releases[platformKey(n.Hello.OS, n.Hello.Arch)]
	if rel == nil || compareVersions(rel.Version, n.Hello.AgentVersion) <= 0 {
		return nil
	}
	return rel
}

func (c *Controller) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := c.startNodeUpdate(id); err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) startNodeUpdate(id string) error {
	c.mu.Lock()
	n, ok := c.nodes[id]
	nc := c.conns[id]
	if !ok || nc == nil {
		c.mu.Unlock()
		return errors.New("node is offline")
	}
	rel := c.updateCandidateLocked(n)
	if rel == nil {
		c.mu.Unlock()
		return errors.New("no newer release uploaded for this node's platform")
	}
	if c.runningLocked(id) > 0 {
		c.mu.Unlock()
		return errors.New("node has running jobs; try again when it is idle")
	}
	c.updates[id] = &UpdateStatus{Version: rel.Version, State: "sent", BytesTotal: rel.Bytes, Updated: time.Now()}
	msg := NodeUpdate{Version: rel.Version, Platform: rel.Platform, SHA256: rel.SHA256, Bytes: rel.Bytes}
	c.mu.Unlock()
	log.Printf("node %s: update to %s requested", id, rel.Version)
	return nc.send("node.update", msg)
}

func (c *Controller) handleUpdateAll(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	var ids []string
	for id, n := range c.nodes {
		if c.conns[id] != nil && c.updateCandidateLocked(n) != nil {
			ids = append(ids, id)
		}
	}
	c.mu.Unlock()
	sort.Strings(ids)
	started, skipped := 0, map[string]string{}
	for _, id := range ids {
		if err := c.startNodeUpdate(id); err != nil {
			skipped[id] = err.Error()
		} else {
			started++
		}
	}
	writeJSON(w, 200, map[string]any{"started": started, "skipped": skipped})
}

// handleUpdateController installs the release for the controller's own
// platform and restarts into it. The web UI is embedded, so it updates too.
func (c *Controller) handleUpdateController(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	rel := c.releases[thisPlatform()]
	c.mu.Unlock()
	if rel == nil || compareVersions(rel.Version, Version) <= 0 {
		writeJSON(w, 409, map[string]string{"error": "no newer release uploaded for " + thisPlatform()})
		return
	}
	if _, err := installRelease(c.releasePath(rel.Platform), c.agentDir(), rel.Version, rel.SHA256); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("controller update to %s installed; restarting", rel.Version)
	writeJSON(w, 200, map[string]string{"version": rel.Version})
	go func() {
		time.Sleep(500 * time.Millisecond)
		relaunch(c.agentDir(), os.Args[1:])
		log.Printf("controller restart failed; still running %s", Version)
	}()
}

// ---- sessions survive controller restarts (and updates) ----

func (c *Controller) sessionsPath() string { return filepath.Join(c.dataDir, "sessions.json") }

func (c *Controller) loadSessions() {
	b, err := os.ReadFile(c.sessionsPath())
	if err != nil {
		return
	}
	var m map[string]time.Time
	if json.Unmarshal(b, &m) == nil {
		for k, v := range m {
			if time.Now().Before(v) {
				c.sessions[k] = v
			}
		}
	}
	if mb, err := os.ReadFile(c.sessionsPath() + ".meta"); err == nil {
		var meta map[string]sessionMeta
		if json.Unmarshal(mb, &meta) == nil {
			for k, v := range meta {
				if _, ok := c.sessions[k]; ok {
					c.sessMeta[k] = v
				}
			}
		}
	}
}

// saveSessionsLocked must be called with c.mu held.
func (c *Controller) saveSessionsLocked() {
	b, _ := json.Marshal(c.sessions)
	_ = os.WriteFile(c.sessionsPath(), b, 0o600)
	mb, _ := json.Marshal(c.sessMeta)
	_ = os.WriteFile(c.sessionsPath()+".meta", mb, 0o600)
}

func (c *Controller) handleRollbackNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c.mu.Lock()
	n := c.nodes[id]
	nc := c.conns[id]
	var to string
	if n != nil && n.Hello != nil {
		to = n.Hello.RollbackTo
	}
	running := c.runningLocked(id)
	if to != "" && nc != nil && running == 0 {
		c.updates[id] = &UpdateStatus{Version: to, State: "sent", Updated: time.Now()}
	}
	c.mu.Unlock()
	switch {
	case nc == nil:
		writeJSON(w, 409, map[string]string{"error": "node is offline"})
	case to == "":
		writeJSON(w, 409, map[string]string{"error": "this node has no older version to roll back to"})
	case running > 0:
		writeJSON(w, 409, map[string]string{"error": "node has running jobs; try again when it is idle"})
	default:
		log.Printf("node %s: rollback to %s requested", id, to)
		if err := nc.send("node.rollback", NodeRollback{Version: to}); err != nil {
			writeJSON(w, 502, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]string{"version": to})
	}
}

func (c *Controller) handleRollbackController(w http.ResponseWriter, r *http.Request) {
	to := previousVersion(c.agentDir())
	if to == "" {
		writeJSON(w, 409, map[string]string{"error": "no older controller version is installed"})
		return
	}
	target, err := prepareRollback(c.agentDir(), to)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("controller rollback to %s; restarting", to)
	writeJSON(w, 200, map[string]string{"version": to})
	go func() {
		time.Sleep(500 * time.Millisecond)
		restartInto(target.path, os.Args[1:])
		log.Printf("controller rollback restart failed; still running %s", Version)
	}()
}

func (c *Controller) handleRestartNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c.mu.Lock()
	nc := c.conns[id]
	if nc != nil {
		c.restarts[id] = time.Now()
	}
	c.mu.Unlock()
	if nc == nil {
		writeJSON(w, 409, map[string]string{"error": "node is offline"})
		return
	}
	log.Printf("node %s: restart requested", id)
	if err := nc.send("node.restart", struct{}{}); err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
