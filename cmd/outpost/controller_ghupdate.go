package main

// Updates from this project's releases on GitHub, GitLab or Gitea/Forgejo (update_source.go). The controller checks the repository's latest
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
	"sort"
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
	Catalog string    `json:"catalog,omitempty"` // release whose model catalog is in use (catalog_remote.go); "" = built in
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

var ghOwnerRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}$`)

// updConfig returns the configured source (its server type may still be unknown) and the
// decrypted token (may be empty). An unreadable setting falls back to the default repository.
func (c *Controller) updConfig() (updSource, string) {
	c.mu.Lock()
	src := c.updSourceLocked()
	enc := c.settings.GitHubTokenEnc
	c.mu.Unlock()
	token, _ := c.decrypt(enc)
	return src, token
}

func (c *Controller) updSourceLocked() updSource {
	if src, err := parseUpdateSource(c.settings.GitHubRepo, c.settings.UpdateHostKind); err == nil {
		return src
	}
	return defaultSource()
}

// updShownLocked is the source as the owner sees it: with the server type filled in (and the
// path trimmed) once a self-hosted server has said what it is.
func (c *Controller) updShownLocked() updSource {
	src := c.updSourceLocked()
	if k, ok := detectedKinds.Load(src.origin()); ok && src.Kind == "" {
		if r, err := src.withKind(k.(string)); err == nil {
			return r
		}
	}
	return src
}

// updResolved is updConfig with the server type known (asking the server if needed).
func (c *Controller) updResolved(ctx context.Context) (updSource, string, error) {
	src, token := c.updConfig()
	src, err := resolveSource(ctx, src)
	return src, token, err
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
		log.Printf("update check: %v", err)
	}
	return latest, err
}

func (c *Controller) checkGitHubRun(ctx context.Context) (latest string, fetched []string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	src, token, err := c.updResolved(ctx)
	if err != nil {
		return "", nil, err
	}
	branch := c.ghBranch()
	var rel ghLatest
	if branch == "" {
		if rel, err = src.latestRelease(ctx, token); err != nil {
			return "", nil, err
		}
	} else {
		builds, err := c.ghBranchBuilds(ctx, src, token)
		if err != nil {
			return "", nil, err
		}
		b, ok := builds[branch]
		if !ok {
			return "", nil, fmt.Errorf("branch %q has no build on %s (builds are published when the branch is pushed)", branch, src.label())
		}
		rel = b
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
		if v, ok := have[p]; ghWantFetch(branch, v, ok, latest) {
			want = append(want, p)
		}
	}
	assets := map[string]ghAsset{}
	for _, a := range rel.Assets {
		assets[a.Name] = a
	}
	sumAsset, ok := assets["SHA256SUMS"]
	if !ok {
		return latest, nil, errors.New("release " + rel.Tag + " has no SHA256SUMS file; not installing")
	}
	sr, err := src.get(ctx, sumAsset.downloadURL(token != ""), "application/octet-stream", token)
	if err != nil {
		return latest, nil, err
	}
	sumBytes, err := io.ReadAll(io.LimitReader(sr.Body, maxSumsBytes))
	sr.Body.Close()
	if err != nil {
		return latest, nil, err
	}
	sums := parseSums(sumBytes)
	if branch == "" { // a branch build never changes the suggested-models list
		if err := c.updateCatalog(ctx, src, rel, latest, sums, token); err != nil {
			log.Printf("model catalog update: %v", err)
		}
	}
	if len(want) == 0 {
		return latest, nil, nil
	}
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
		if err := c.fetchReleaseAsset(ctx, src, a, wantSum, latest, token); err != nil {
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
func (c *Controller) fetchReleaseAsset(ctx context.Context, src updSource, a ghAsset, wantSum, wantVersion, token string) error {
	if err := os.MkdirAll(c.releasesDir(), 0o700); err != nil {
		return err
	}
	resp, err := src.get(ctx, a.downloadURL(token != ""), "application/octet-stream", token)
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
			if _, err := c.checkGitHub(context.Background()); err == nil && auto && c.ghBranch() == "" {
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

// nodeUpdateWait caps how long autoInstall holds the controller update for node downloads.
var nodeUpdateWait = 10 * time.Minute

// nodeUpdatePoll is how often waitNodeUpdates looks at the node update states.
var nodeUpdatePoll = 2 * time.Second

// waitNodeUpdates blocks until none of ids is still sent, downloading or installing, or until
// max has passed. It reports whether they all settled.
func (c *Controller) waitNodeUpdates(ids []string, max time.Duration) bool {
	deadline := time.Now().Add(max)
	for {
		c.mu.Lock()
		busy := false
		for _, id := range ids {
			if u := c.updates[id]; u != nil && (u.State == "sent" || u.State == "downloading" || u.State == "installing") {
				busy = true
			}
		}
		c.mu.Unlock()
		if !busy {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(nodeUpdatePoll)
	}
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
	sort.Strings(ids)
	if len(ids) > 0 {
		// One node first, like Update all (rollout.go). A rollout already running is left alone.
		if _, err := c.startRollout(ids); err != nil {
			log.Printf("auto-install: %v", err)
		}
	}
	c.mu.Lock()
	rel := c.releases[thisPlatform()]
	ok := rel != nil && compareVersions(rel.Version, Version) > 0 && (hold == "" || compareVersions(rel.Version, hold) > 0) && !c.rollout.running()
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
		// The controller serves the node downloads, so restarting it mid-download would cut them
		// off. Wait until each node has its binary installed ("restarting") or has failed.
		if !c.waitNodeUpdates(ids, nodeUpdateWait) {
			log.Printf("auto-install: node updates still running after %s; updating the controller anyway", nodeUpdateWait)
		}
	}
	log.Printf("auto-install: updating the controller to %s", rel.Version)
	if _, err := installRelease(c.releasePath(rel.Platform), c.agentDir(), rel.Version, rel.SHA256); err != nil {
		log.Printf("auto-install: controller: %v", err)
		return
	}
	c.prepareRestart("update installed")
	relaunch(c.agentDir(), os.Args[1:])
	controllerClosing.Store(false) // still running: the restart did not happen
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
	src := c.updShownLocked()
	return map[string]any{
		"enabled": !c.settings.GitHubUpdatesOff, "auto_install": c.settings.GitHubAutoInstall,
		"hold": c.settings.GitHubHoldVersion, "repo": src.URL(), "host": src.label(), "branch": c.settings.GitHubBranch,
		"checked": s.Checked, "latest": s.Latest, "error": s.Error, "busy": s.Busy, "fetched": s.Fetched, "catalog": s.Catalog,
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
// the saved ones, so the owner can test the stored token without retyping it; the saved token is
// only used when the repository is on the same server as the saved one.
func (c *Controller) handleTestGitHub(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Repo  string  `json:"repo"`
		Kind  *string `json:"kind"`
		Token string  `json:"token"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req)
	saved, savedTok := c.updConfig()
	src := saved
	if strings.TrimSpace(req.Repo) != "" || req.Kind != nil {
		c.mu.Lock()
		kind := c.settings.UpdateHostKind
		c.mu.Unlock()
		if req.Kind != nil {
			kind = *req.Kind
		}
		var err error
		if src, err = parseUpdateSource(req.Repo, kind); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	src, err := resolveSource(ctx, src)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	token := ""
	if t := strings.TrimSpace(req.Token); t != "" {
		token = t
	} else if savedR, err := resolveSource(ctx, saved); err == nil && savedR.tokenOrigin() == src.tokenOrigin() {
		token = savedTok
	}
	private, err := src.repoPrivate(ctx, token)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	rel, err := src.latestRelease(ctx, token)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": "repository found, but no usable release: " + err.Error()})
		return
	}
	hasSums := false
	for _, a := range rel.Assets {
		if a.Name == "SHA256SUMS" {
			hasSums = true
		}
	}
	msg := fmt.Sprintf("Connected to %s (%s, %s). Latest release %s", src.URL(), src.label(), map[bool]string{true: "private", false: "public"}[private], rel.Tag)
	if hasSums {
		msg += " has a SHA256SUMS file."
	} else {
		msg += " has no SHA256SUMS file, so its binaries would not be installed."
	}
	if src.Scheme == "http" {
		msg += " The address is plain http://, so the token and the downloads are not encrypted."
	}
	writeJSON(w, 200, map[string]any{"ok": true, "repo": src.URL(), "host": src.label(), "private": private, "latest": strings.TrimPrefix(rel.Tag, "v"), "sums": hasSums, "message": msg})
}

// ---- branch builds ----
//
// CI publishes every build of a listed branch as a GitHub prerelease tagged v<X.Y.Z>-<slug>.<run>
// (see .github/workflows/build-branch.yml), with the same SHA256SUMS as a normal release. The
// "latest release" endpoint never returns a prerelease, so the Stable channel is unaffected.
// A controller set to a branch follows that branch's newest prerelease; everything after the
// download (preflight, canary rollout, trial and rollback) is the same as for any release.

var (
	ghBranchRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,11}$`)
	ghBuildTag  = regexp.MustCompile(`^v([0-9]+\.[0-9]+\.[0-9]+)-([a-z0-9][a-z0-9-]{0,11})\.([0-9]+)$`)
	ghListLimit = 4 << 20
)

// ghBranch returns the selected branch channel; "" is Stable.
func (c *Controller) ghBranch() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !ghBranchRe.MatchString(c.settings.GitHubBranch) {
		return ""
	}
	return c.settings.GitHubBranch
}

// ghWantFetch decides whether the binary for a platform must be downloaded. Stable only takes
// something newer, except that it replaces a stored branch build. A branch takes whatever its
// newest build is (an older one is stored but never offered as an update).
func ghWantFetch(branch, have string, haveOK bool, latest string) bool {
	if !haveOK || have == latest {
		return !haveOK
	}
	if branch != "" {
		return true
	}
	return compareVersions(latest, have) > 0 || isPrerelease(have)
}

// ghBranchBuilds lists the repository's branch builds (prereleases), newest per branch.
func (c *Controller) ghBranchBuilds(ctx context.Context, src updSource, token string) (map[string]ghLatest, error) {
	list, err := src.listReleases(ctx, token)
	if err != nil {
		return nil, err
	}
	out := map[string]ghLatest{}
	for _, r := range list {
		m := ghBuildTag.FindStringSubmatch(r.Tag)
		if m == nil || !r.Prerelease || r.Draft {
			continue
		}
		if cur, ok := out[m[2]]; !ok || compareVersions(r.Tag, cur.Tag) > 0 {
			out[m[2]] = r.ghLatest
		}
	}
	return out, nil
}

// handleGitHubBranches answers the Updates tab's dropdown with the branches that have a build
// on GitHub right now (fetched live on every call).
func (c *Controller) handleGitHubBranches(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	src, token, err := c.updResolved(ctx)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	builds, err := c.ghBranchBuilds(ctx, src, token)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	type row struct {
		Branch  string `json:"branch"`
		Version string `json:"version"`
	}
	rows := []row{}
	for b, rel := range builds {
		rows = append(rows, row{b, strings.TrimPrefix(rel.Tag, "v")})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Branch < rows[j].Branch })
	writeJSON(w, 200, map[string]any{"branches": rows, "selected": c.ghBranch()})
}

// handleGitHubChannel switches between Stable ("") and a branch, then checks at once.
func (c *Controller) handleGitHubChannel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Branch string `json:"branch"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	b := strings.TrimSpace(req.Branch)
	if b != "" && !ghBranchRe.MatchString(b) {
		writeJSON(w, 400, map[string]string{"error": "that is not a branch name Outpost builds"})
		return
	}
	c.mu.Lock()
	changed := c.settings.GitHubBranch != b
	c.settings.GitHubBranch = b
	if changed { // versions of another channel are unrelated to the rollback hold
		c.settings.GitHubHoldVersion = ""
		c.ghState = ghUpdateState{}
	}
	err := c.saveSettingsLocked()
	c.mu.Unlock()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("update channel set to %q", b)
	if changed {
		go c.checkGitHub(context.Background())
	}
	writeJSON(w, 200, c.ghView())
}
