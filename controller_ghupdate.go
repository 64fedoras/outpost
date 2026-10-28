package main

// Updates from this project's GitHub releases. The controller checks the repository's latest
// release, downloads the binaries it needs (its own platform and the platforms of known nodes),
// checks each one against the release's SHA256SUMS, and stores them as ordinary uploaded releases.
// From there the Updates tab, node update path and rollback work exactly as for a manual upload.
// With auto-install on, idle nodes are updated and then the controller itself.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	ghRepo          = "64fedoras/outpost"
	ghCheckInterval = 6 * time.Hour
	ghFirstCheck    = 2 * time.Minute
	maxSumsBytes    = 1 << 20
)

// ghUpdateState is what the Updates tab shows about the GitHub check.
type ghUpdateState struct {
	Checked time.Time `json:"checked,omitempty"`
	Latest  string    `json:"latest,omitempty"` // newest published version
	Error   string    `json:"error,omitempty"`
	Busy    string    `json:"busy,omitempty"` // "checking", "downloading <platform>"
	Fetched []string  `json:"fetched,omitempty"`
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

type ghLatest struct {
	Tag    string    `json:"tag_name"`
	Assets []ghAsset `json:"assets"`
}

func ghAPIBase() string {
	if u := os.Getenv("OUTPOST_GITHUB_API"); u != "" { // for testing against a stub
		return strings.TrimRight(u, "/")
	}
	return "https://api.github.com"
}

// parseSums reads a sha256sum-style file into name -> lowercase hex digest.
func parseSums(b []byte) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || len(f[0]) != 64 {
			continue
		}
		m[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
	}
	return m
}

func releaseFileName(platform string) string {
	goos, _, _ := strings.Cut(platform, "-")
	return "outpost-" + platform + exeSuffix(goos)
}

func (c *Controller) ghGet(ctx context.Context, url string, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "outpost/"+Version)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("GitHub: %s", resp.Status)
	}
	return resp, nil
}

// neededPlatformsLocked lists the platforms worth downloading: this controller's and every known node's.
func (c *Controller) neededPlatformsLocked() []string {
	seen := map[string]bool{thisPlatform(): true}
	out := []string{thisPlatform()}
	for _, n := range c.nodes {
		if n.Hello == nil {
			continue
		}
		if p := platformKey(n.Hello.OS, n.Hello.Arch); !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func (c *Controller) setGH(f func(s *ghUpdateState)) {
	c.mu.Lock()
	f(&c.ghState)
	c.mu.Unlock()
}

// checkGitHub looks up the latest release and stores any newer binaries. It returns the version
// found. Only one check runs at a time.
func (c *Controller) checkGitHub(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.ghState.Busy != "" {
		c.mu.Unlock()
		return "", errors.New("a check is already running")
	}
	c.ghState.Busy = "checking"
	c.mu.Unlock()
	latest, fetched, err := c.checkGitHubRun(ctx)
	c.setGH(func(s *ghUpdateState) {
		s.Busy, s.Checked, s.Latest = "", time.Now(), latest
		s.Fetched = fetched
		s.Error = ""
		if err != nil {
			s.Error = err.Error()
		}
	})
	if err != nil {
		log.Printf("github update check: %v", err)
	}
	return latest, err
}

func (c *Controller) checkGitHubRun(ctx context.Context) (latest string, fetched []string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	resp, err := c.ghGet(ctx, ghAPIBase()+"/repos/"+ghRepo+"/releases/latest", "application/vnd.github+json")
	if err != nil {
		return "", nil, err
	}
	var rel ghLatest
	err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel)
	resp.Body.Close()
	if err != nil {
		return "", nil, err
	}
	latest = strings.TrimPrefix(rel.Tag, "v")
	if compareVersions(latest, "0.0.1") <= 0 {
		return "", nil, fmt.Errorf("unexpected release tag %q", rel.Tag)
	}
	c.mu.Lock()
	plats := c.neededPlatformsLocked()
	have := map[string]string{}
	for p, r := range c.releases {
		have[p] = r.Version
	}
	c.mu.Unlock()
	var want []string
	for _, p := range plats {
		if v, ok := have[p]; !ok || compareVersions(latest, v) > 0 {
			want = append(want, p)
		}
	}
	if len(want) == 0 {
		return latest, nil, nil
	}
	assets := map[string]ghAsset{}
	for _, a := range rel.Assets {
		assets[a.Name] = a
	}
	sumAsset, ok := assets["SHA256SUMS"]
	if !ok {
		return latest, nil, errors.New("release " + rel.Tag + " has no SHA256SUMS file; not installing")
	}
	sr, err := c.ghGet(ctx, sumAsset.URL, "application/octet-stream")
	if err != nil {
		return latest, nil, err
	}
	sumBytes, err := io.ReadAll(io.LimitReader(sr.Body, maxSumsBytes))
	sr.Body.Close()
	if err != nil {
		return latest, nil, err
	}
	sums := parseSums(sumBytes)
	var errs []string
	for _, p := range want {
		name := releaseFileName(p)
		a, ok := assets[name]
		wantSum := sums[name]
		if !ok || wantSum == "" {
			errs = append(errs, name+" is not in release "+rel.Tag)
			continue
		}
		c.setGH(func(s *ghUpdateState) { s.Busy = "downloading " + p })
		if err := c.fetchReleaseAsset(ctx, a, wantSum, latest); err != nil {
			errs = append(errs, name+": "+err.Error())
			continue
		}
		fetched = append(fetched, p)
	}
	if len(errs) > 0 {
		err = errors.New(strings.Join(errs, "; "))
	}
	return latest, fetched, err
}

// fetchReleaseAsset downloads one binary next to the stored releases, verifies its SHA-256 and
// that it is an Outpost binary of the announced version, then stores it like an upload.
func (c *Controller) fetchReleaseAsset(ctx context.Context, a ghAsset, wantSum, wantVersion string) error {
	if err := os.MkdirAll(c.releasesDir(), 0o700); err != nil {
		return err
	}
	resp, err := c.ghGet(ctx, a.URL, "application/octet-stream")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(c.releasesDir(), ".github-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, io.LimitReader(resp.Body, maxUploadBytes+1))
	tmp.Close()
	if err != nil {
		return err
	}
	sum, size, err := fileSHA256(tmp.Name())
	if err != nil {
		return err
	}
	if size > maxUploadBytes {
		return errors.New("file is too large")
	}
	if sum != wantSum {
		return errors.New("SHA-256 does not match SHA256SUMS; discarded")
	}
	_, ver, err := inspectBinary(tmp.Name())
	if err != nil {
		return err
	}
	if ver != wantVersion {
		return fmt.Errorf("binary reports version %s, release is %s; discarded", ver, wantVersion)
	}
	_, err = c.installRelease(tmp.Name(), a.Name)
	return err
}

// ghEnabled reports whether periodic checks are on (default on).
func (c *Controller) ghEnabled() (check, auto bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.settings.GitHubUpdatesOff, c.settings.GitHubAutoInstall
}

func (c *Controller) ghLoop() {
	time.Sleep(ghFirstCheck)
	for {
		if check, auto := c.ghEnabled(); check {
			if _, err := c.checkGitHub(context.Background()); err == nil && auto {
				c.autoInstall()
			}
		}
		time.Sleep(ghCheckInterval)
	}
}

// tasksBusyLocked is true while a task is planning, running or testing.
func (c *Controller) tasksBusyLocked() bool {
	for _, t := range c.tasks {
		switch t.Status {
		case "planning", "running", "fixing", "testing":
			return true
		}
	}
	return false
}

// autoInstall updates idle nodes, then the controller once no node update is in flight and no
// task is working. Anything skipped (busy node, running task) is retried by the next check.
// It never installs a version at or below the hold set by a rollback.
func (c *Controller) autoInstall() {
	c.mu.Lock()
	hold := c.settings.GitHubHoldVersion
	var ids []string
	for id, n := range c.nodes {
		if rel := c.updateCandidateLocked(n); rel != nil && c.conns[id] != nil && (hold == "" || compareVersions(rel.Version, hold) > 0) {
			ids = append(ids, id)
		}
	}
	c.mu.Unlock()
	for _, id := range ids {
		if err := c.startNodeUpdate(id); err != nil {
			log.Printf("auto-install: node %s: %v", id, err)
		}
	}
	c.mu.Lock()
	rel := c.releases[thisPlatform()]
	ok := rel != nil && compareVersions(rel.Version, Version) > 0 && (hold == "" || compareVersions(rel.Version, hold) > 0)
	if ok {
		for _, u := range c.updates {
			if time.Since(u.Updated) < 10*time.Minute && (u.State == "sent" || u.State == "downloading" || u.State == "installing" || u.State == "restarting") {
				ok = false
			}
		}
		if c.tasksBusyLocked() {
			ok = false
		}
	}
	c.mu.Unlock()
	if !ok {
		return
	}
	if len(ids) > 0 {
		// Give the node downloads a head start; the controller serves them.
		time.Sleep(5 * time.Second)
		c.mu.Lock()
		for _, u := range c.updates {
			if time.Since(u.Updated) < 10*time.Minute && (u.State == "sent" || u.State == "downloading" || u.State == "installing" || u.State == "restarting") {
				ok = false
			}
		}
		c.mu.Unlock()
		if !ok {
			return
		}
	}
	log.Printf("auto-install: updating the controller to %s", rel.Version)
	if _, err := installRelease(c.releasePath(rel.Platform), c.agentDir(), rel.Version, rel.SHA256); err != nil {
		log.Printf("auto-install: controller: %v", err)
		return
	}
	relaunch(c.agentDir(), os.Args[1:])
	log.Printf("auto-install: controller restart failed; still running %s", Version)
}

// holdAfterRollbackLocked stops auto-install from re-applying the version that was just left.
func (c *Controller) holdAfterRollbackLocked(from string) {
	if from != "" && compareVersions(from, c.settings.GitHubHoldVersion) > 0 {
		c.settings.GitHubHoldVersion = from
		_ = c.saveSettingsLocked()
	}
}

func (c *Controller) ghView() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ghViewLocked()
}

func (c *Controller) ghViewLocked() map[string]any {
	s := c.ghState
	return map[string]any{
		"enabled": !c.settings.GitHubUpdatesOff, "auto_install": c.settings.GitHubAutoInstall,
		"hold": c.settings.GitHubHoldVersion, "repo": ghRepo,
		"checked": s.Checked, "latest": s.Latest, "error": s.Error, "busy": s.Busy, "fetched": s.Fetched,
	}
}

// handleCheckGitHub runs a check now, whatever the periodic setting is.
func (c *Controller) handleCheckGitHub(w http.ResponseWriter, r *http.Request) {
	latest, err := c.checkGitHub(r.Context())
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	v := c.ghView()
	v["latest"] = latest
	writeJSON(w, 200, v)
}

// handleInstallGitHub checks, then updates idle nodes and the controller now, ignoring the
// rollback hold (the owner asked for it).
func (c *Controller) handleInstallGitHub(w http.ResponseWriter, r *http.Request) {
	if _, err := c.checkGitHub(r.Context()); err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	c.mu.Lock()
	c.settings.GitHubHoldVersion = ""
	_ = c.saveSettingsLocked()
	c.mu.Unlock()
	go c.autoInstall()
	writeJSON(w, 200, c.ghView())
}
