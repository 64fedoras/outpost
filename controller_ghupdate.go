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
	"regexp"
	"strings"
	"time"
)

const (
	ghDefaultRepo   = "64fedoras/outpost"
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
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	APIURL string `json:"url"` // authenticated download endpoint; the only one that works for private repos
	Size   int64  `json:"size"`
}

// downloadURL picks the asset address: the API endpoint when a token is in use (private repos),
// the public download link otherwise.
func (a ghAsset) downloadURL(authed bool) string {
	if authed && a.APIURL != "" {
		return a.APIURL
	}
	return a.URL
}

var (
	ghOwnerRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}$`)
	ghRepoRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// parseGitHubRepo turns what the owner typed into "owner/repo". It accepts a github.com URL
// (https://github.com/owner/repo, with or without .git, a trailing path or www.), github.com/owner/repo
// or owner/repo, and refuses every other host and scheme. Empty means the default repository.
func parseGitHubRepo(in string) (string, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return ghDefaultRepo, nil
	}
	if i := strings.Index(s, "://"); i >= 0 {
		if !strings.EqualFold(s[:i], "https") {
			return "", errors.New("use an https://github.com/owner/repo address")
		}
		s = s[i+3:]
	}
	if strings.ContainsAny(s, "?#@ \t") {
		return "", errors.New("use an address like https://github.com/owner/repo")
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) >= 3 || (len(parts) == 2 && strings.Contains(parts[0], ".")) {
		host := strings.ToLower(parts[0])
		if host != "github.com" && host != "www.github.com" {
			return "", errors.New("only github.com repositories can be used for updates")
		}
		parts = parts[1:]
	}
	if len(parts) < 2 {
		return "", errors.New("use an address like https://github.com/owner/repo")
	}
	owner, repo := parts[0], strings.TrimSuffix(parts[1], ".git")
	if !ghOwnerRe.MatchString(owner) || !ghRepoRe.MatchString(repo) || repo == "." || repo == ".." {
		return "", errors.New("that does not look like a GitHub owner/repo name")
	}
	return owner + "/" + repo, nil
}

// ghConfig returns the repository to trust for updates and the decrypted token (may be empty).
func (c *Controller) ghConfig() (repo, token string) {
	c.mu.Lock()
	repo, enc := c.settings.GitHubRepo, c.settings.GitHubTokenEnc
	c.mu.Unlock()
	if r, err := parseGitHubRepo(repo); err == nil {
		repo = r
	} else {
		repo = ghDefaultRepo
	}
	token, _ = c.decrypt(enc)
	return repo, token
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

// ghGet fetches url from GitHub. The token, if any, goes only to the GitHub API address; the
// redirect to the signed storage URL carries none (Go drops Authorization on a host change).
func (c *Controller) ghGet(ctx context.Context, url, accept, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "outpost/"+Version)
	if token != "" && strings.HasPrefix(url, ghAPIBase()+"/") {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, ghStatusError(resp.StatusCode, resp.Status, token != "")
	}
	return resp, nil
}

// ghStatusError turns a GitHub status into a message that says what to fix.
func ghStatusError(code int, status string, hadToken bool) error {
	switch code {
	case 401:
		return errors.New("GitHub rejected the access token (wrong, expired or revoked)")
	case 404:
		if hadToken {
			return errors.New("GitHub: repository or release not found, or the token has no access to it (it needs read access to Contents and Metadata for that repository)")
		}
		return errors.New("GitHub: repository or release not found; a private repository needs an access token")
	case 403, 429:
		return fmt.Errorf("GitHub: %s (rate limit reached, or the token lacks permission)", status)
	}
	return fmt.Errorf("GitHub: %s", status)
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
	repo, token := c.ghConfig()
	resp, err := c.ghGet(ctx, ghAPIBase()+"/repos/"+repo+"/releases/latest", "application/vnd.github+json", token)
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
	sr, err := c.ghGet(ctx, sumAsset.downloadURL(token != ""), "application/octet-stream", token)
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
		if err := c.fetchReleaseAsset(ctx, a, wantSum, latest, token); err != nil {
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
func (c *Controller) fetchReleaseAsset(ctx context.Context, a ghAsset, wantSum, wantVersion, token string) error {
	if err := os.MkdirAll(c.releasesDir(), 0o700); err != nil {
		return err
	}
	resp, err := c.ghGet(ctx, a.downloadURL(token != ""), "application/octet-stream", token)
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
	repo := ghDefaultRepo
	if r, err := parseGitHubRepo(c.settings.GitHubRepo); err == nil {
		repo = r
	}
	return map[string]any{
		"enabled": !c.settings.GitHubUpdatesOff, "auto_install": c.settings.GitHubAutoInstall,
		"hold": c.settings.GitHubHoldVersion, "repo": repo,
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

// handleTestGitHub checks a repository and token without saving them. Empty fields fall back to
// the saved ones, so the owner can test the stored token without retyping it.
func (c *Controller) handleTestGitHub(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Repo  string `json:"repo"`
		Token string `json:"token"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
	saved, savedTok := c.ghConfig()
	repo, token := saved, savedTok
	if strings.TrimSpace(req.Repo) != "" {
		var err error
		if repo, err = parseGitHubRepo(req.Repo); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
	}
	if t := strings.TrimSpace(req.Token); t != "" {
		token = t
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var meta struct {
		Private bool `json:"private"`
	}
	resp, err := c.ghGet(ctx, ghAPIBase()+"/repos/"+repo, "application/vnd.github+json", token)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&meta)
	resp.Body.Close()
	resp, err = c.ghGet(ctx, ghAPIBase()+"/repos/"+repo+"/releases/latest", "application/vnd.github+json", token)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": "repository found, but no usable release: " + err.Error()})
		return
	}
	var rel ghLatest
	err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel)
	resp.Body.Close()
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	hasSums := false
	for _, a := range rel.Assets {
		if a.Name == "SHA256SUMS" {
			hasSums = true
		}
	}
	msg := fmt.Sprintf("Connected to %s (%s). Latest release %s", repo, map[bool]string{true: "private", false: "public"}[meta.Private], rel.Tag)
	if hasSums {
		msg += " has a SHA256SUMS file."
	} else {
		msg += " has no SHA256SUMS file, so its binaries would not be installed."
	}
	writeJSON(w, 200, map[string]any{"ok": true, "repo": repo, "private": meta.Private, "latest": strings.TrimPrefix(rel.Tag, "v"), "sums": hasSums, "message": msg})
}
