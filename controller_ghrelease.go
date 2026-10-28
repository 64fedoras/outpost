package main

// Updates from GitHub releases: the controller lists the releases of the
// Outpost repo (Settings, default 64fedoras/outpost) and, on a click, downloads
// the platform binaries, checks each against the release's SHA256SUMS and
// stores them exactly like a manual upload. Installing them on the controller
// and nodes is still the usual Update button; nothing installs on its own.

import (
	"bufio"
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
	"sync"
	"time"
)

const defaultOutpostRepo = "64fedoras/outpost"

// outpostAssetRe matches the binaries build.sh produces.
var outpostAssetRe = regexp.MustCompile(`^outpost-(linux|darwin|windows)-(amd64|arm64)(\.exe)?$`)

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type ghOutpostAsset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"` // API URL; with Accept: application/octet-stream it serves the file
}

type ghOutpostRelease struct {
	TagName     string           `json:"tag_name"`
	Name        string           `json:"name"`
	HTMLURL     string           `json:"html_url"`
	Draft       bool             `json:"draft"`
	Prerelease  bool             `json:"prerelease"`
	PublishedAt time.Time        `json:"published_at"`
	Assets      []ghOutpostAsset `json:"assets"`
}

// GHStage is the progress of the one GitHub download that may run at a time.
type GHStage struct {
	Tag       string    `json:"tag"`
	State     string    `json:"state"` // downloading, done, failed
	File      string    `json:"file,omitempty"`
	BytesDone int64     `json:"bytes_done"`
	BytesAll  int64     `json:"bytes_total"`
	Staged    []string  `json:"staged,omitempty"`
	Error     string    `json:"error,omitempty"`
	Updated   time.Time `json:"updated"`
}

type outpostGH struct {
	mu       sync.Mutex // guards the cache
	repo     string
	checked  time.Time
	releases []ghOutpostRelease
	stage    *GHStage // guarded by Controller.mu
}

// normalizeRepo accepts "owner/repo" or a github.com URL and returns "owner/repo".
func normalizeRepo(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultOutpostRepo, nil
	}
	for _, p := range []string{"https://", "http://", "www.", "github.com/"} {
		s = strings.TrimPrefix(s, p)
	}
	s = strings.TrimSuffix(strings.TrimSuffix(strings.Trim(s, "/"), ".git"), "/")
	if !repoRe.MatchString(s) {
		return "", errors.New("the repository must look like owner/name or https://github.com/owner/name")
	}
	return s, nil
}

func (c *Controller) outpostRepo() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settings.GitHubRepo == "" {
		return defaultOutpostRepo
	}
	return c.settings.GitHubRepo
}

func (c *Controller) githubToken() string {
	c.mu.Lock()
	enc := c.settings.GitHubTokenEnc
	c.mu.Unlock()
	k, _ := c.decrypt(enc)
	return k
}

func githubAPIBase() string {
	if u := os.Getenv("OUTPOST_GITHUB_API"); u != "" { // for testing against a stub
		return strings.TrimRight(u, "/")
	}
	return "https://api.github.com"
}

func (c *Controller) githubGet(ctx context.Context, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "outpost/"+Version)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if tok := c.githubToken(); tok != "" {
		// Go drops this header when GitHub redirects the download to its file host.
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		switch resp.StatusCode {
		case 401:
			return nil, errors.New("GitHub refused the access token (401); check it in Settings")
		case 404:
			return nil, errors.New("GitHub: not found (404). A private repository needs an access token in Settings")
		case 403:
			return nil, errors.New("GitHub: forbidden (403); the token may lack access, or the rate limit was hit")
		}
		return nil, fmt.Errorf("GitHub: %s", resp.Status)
	}
	return resp, nil
}

// outpostReleases lists the repo's published releases (cached for 10 minutes).
func (c *Controller) outpostReleases(ctx context.Context, force bool) ([]ghOutpostRelease, error) {
	repo := c.outpostRepo()
	g := &c.gh
	g.mu.Lock()
	defer g.mu.Unlock()
	if !force && g.repo == repo && g.releases != nil && time.Since(g.checked) < 10*time.Minute {
		return g.releases, nil
	}
	resp, err := c.githubGet(ctx, githubAPIBase()+"/repos/"+repo+"/releases?per_page=20", "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var all []ghOutpostRelease
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		return nil, err
	}
	rels := []ghOutpostRelease{}
	for _, r := range all {
		if !r.Draft {
			rels = append(rels, r)
		}
	}
	g.repo, g.releases, g.checked = repo, rels, time.Now()
	return rels, nil
}

// parseSHA256SUMS reads `sha256sum` output: "<hex>  <name>" per line.
func parseSHA256SUMS(r io.Reader) map[string]string {
	sums := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && len(f[0]) == 64 {
			sums[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
		}
	}
	return sums
}

func (c *Controller) handleGitHubReleases(w http.ResponseWriter, r *http.Request) {
	rels, err := c.outpostReleases(r.Context(), r.URL.Query().Get("refresh") == "1")
	repo := c.outpostRepo()
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error(), "repo": repo})
		return
	}
	c.mu.Lock()
	staged := map[string]string{}
	for p, rel := range c.releases {
		staged[p] = rel.Version
	}
	c.mu.Unlock()
	out := []map[string]any{}
	for _, rel := range rels {
		v := strings.TrimPrefix(rel.TagName, "v")
		var platforms []string
		hasSums := false
		var size int64
		for _, a := range rel.Assets {
			if m := outpostAssetRe.FindStringSubmatch(a.Name); m != nil {
				platforms = append(platforms, platformKey(m[1], m[2]))
				size += a.Size
			} else if a.Name == "SHA256SUMS" {
				hasSums = true
			}
		}
		allStaged := len(platforms) > 0
		for _, p := range platforms {
			if compareVersions(staged[p], v) != 0 {
				allStaged = false
			}
		}
		out = append(out, map[string]any{
			"tag": rel.TagName, "version": v, "name": rel.Name, "url": rel.HTMLURL,
			"published": rel.PublishedAt, "prerelease": rel.Prerelease,
			"platforms": platforms, "bytes": size, "has_sums": hasSums,
			"newer": compareVersions(v, Version) > 0, "current": compareVersions(v, Version) == 0,
			"staged": allStaged,
		})
	}
	writeJSON(w, 200, map[string]any{"repo": repo, "releases": out})
}

// handleStageGitHubRelease starts downloading one release's binaries in the background.
func (c *Controller) handleStageGitHubRelease(w http.ResponseWriter, r *http.Request) {
	tag := r.PathValue("tag")
	c.mu.Lock()
	if s := c.gh.stage; s != nil && s.State == "downloading" {
		c.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "a download from GitHub is already running (" + s.Tag + ")"})
		return
	}
	c.gh.stage = &GHStage{Tag: tag, State: "downloading", Updated: time.Now()}
	c.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		staged, err := c.stageGitHubRelease(ctx, tag)
		c.mu.Lock()
		s := c.gh.stage
		s.Staged, s.Updated, s.File = staged, time.Now(), ""
		if err != nil {
			s.State, s.Error = "failed", err.Error()
			log.Printf("GitHub release %s: %v", tag, err)
		} else {
			s.State = "done"
			log.Printf("GitHub release %s: staged %s", tag, strings.Join(staged, ", "))
		}
		c.mu.Unlock()
	}()
	writeJSON(w, 202, map[string]bool{"ok": true})
}

func (c *Controller) setStage(f func(s *GHStage)) {
	c.mu.Lock()
	if c.gh.stage != nil {
		f(c.gh.stage)
		c.gh.stage.Updated = time.Now()
	}
	c.mu.Unlock()
}

// stageGitHubRelease downloads every Outpost binary in release tag, verifies
// it against SHA256SUMS and stores it as that platform's release. Platforms
// already holding the identical file are skipped. It returns the platforms stored.
func (c *Controller) stageGitHubRelease(ctx context.Context, tag string) ([]string, error) {
	rels, err := c.outpostReleases(ctx, false)
	if err != nil {
		return nil, err
	}
	var rel *ghOutpostRelease
	for i := range rels {
		if rels[i].TagName == tag {
			rel = &rels[i]
		}
	}
	if rel == nil {
		if rels, err = c.outpostReleases(ctx, true); err != nil {
			return nil, err
		}
		for i := range rels {
			if rels[i].TagName == tag {
				rel = &rels[i]
			}
		}
	}
	if rel == nil {
		return nil, fmt.Errorf("release %s not found in %s", tag, c.outpostRepo())
	}
	var sumsAsset *ghOutpostAsset
	var bins []ghOutpostAsset
	var total int64
	for _, a := range rel.Assets {
		if a.Name == "SHA256SUMS" {
			a := a
			sumsAsset = &a
		} else if outpostAssetRe.MatchString(a.Name) {
			bins = append(bins, a)
			total += a.Size
		}
	}
	if len(bins) == 0 {
		return nil, fmt.Errorf("release %s has no Outpost binaries", tag)
	}
	if sumsAsset == nil {
		return nil, fmt.Errorf("release %s has no SHA256SUMS file, so its binaries cannot be checked", tag)
	}
	resp, err := c.githubGet(ctx, sumsAsset.URL, "application/octet-stream")
	if err != nil {
		return nil, fmt.Errorf("SHA256SUMS: %w", err)
	}
	sums := parseSHA256SUMS(io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	c.setStage(func(s *GHStage) { s.BytesAll = total })

	want := strings.TrimPrefix(tag, "v")
	var staged []string
	var done int64
	for _, a := range bins {
		m := outpostAssetRe.FindStringSubmatch(a.Name)
		platform := platformKey(m[1], m[2])
		sum := sums[a.Name]
		if sum == "" {
			return staged, fmt.Errorf("%s is not listed in SHA256SUMS", a.Name)
		}
		c.mu.Lock()
		cur := c.releases[platform]
		c.mu.Unlock()
		if cur != nil && cur.SHA256 == sum {
			done += a.Size
			c.setStage(func(s *GHStage) { s.BytesDone = done })
			continue
		}
		c.setStage(func(s *GHStage) { s.File = a.Name })
		base := done
		rec, err := c.downloadReleaseAsset(ctx, a, sum, func(n int64) {
			c.setStage(func(s *GHStage) { s.BytesDone = base + n })
		})
		if err != nil {
			return staged, fmt.Errorf("%s: %w", a.Name, err)
		}
		if rec.Platform != platform {
			os.Remove(rec.tmp)
			return staged, fmt.Errorf("%s is really a %s binary", a.Name, rec.Platform)
		}
		if compareVersions(rec.Version, want) != 0 {
			os.Remove(rec.tmp)
			return staged, fmt.Errorf("%s reports version %s, not %s", a.Name, rec.Version, want)
		}
		rec.Source = "github " + tag
		if err := c.storeRelease(rec.tmp, &rec.Release); err != nil {
			os.Remove(rec.tmp)
			return staged, err
		}
		done += a.Size
		staged = append(staged, platform)
	}
	return staged, nil
}

type downloadedRelease struct {
	Release
	tmp string
}

// downloadReleaseAsset fetches one binary into a temp file in releasesDir and
// checks its SHA-256 and that it is an Outpost binary.
func (c *Controller) downloadReleaseAsset(ctx context.Context, a ghOutpostAsset, wantSum string, progress func(int64)) (*downloadedRelease, error) {
	if err := os.MkdirAll(c.releasesDir(), 0o700); err != nil {
		return nil, err
	}
	resp, err := c.githubGet(ctx, a.URL, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(c.releasesDir(), ".github-*")
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp.Name())
		}
	}()
	var n int64
	buf := make([]byte, 1<<20)
	last := time.Now()
	body := io.LimitReader(resp.Body, maxUploadBytes+1)
	for {
		k, rerr := body.Read(buf)
		if k > 0 {
			if _, werr := tmp.Write(buf[:k]); werr != nil {
				tmp.Close()
				return nil, werr
			}
			n += int64(k)
			if time.Since(last) > 500*time.Millisecond {
				progress(n)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			tmp.Close()
			return nil, rerr
		}
	}
	tmp.Close()
	progress(n)
	if n > maxUploadBytes {
		return nil, errors.New("file is too large")
	}
	sum, size, err := fileSHA256(tmp.Name())
	if err != nil {
		return nil, err
	}
	if sum != wantSum {
		return nil, fmt.Errorf("SHA-256 mismatch: got %s, SHA256SUMS says %s", sum, wantSum)
	}
	platform, version, err := inspectBinary(tmp.Name())
	if err != nil {
		return nil, err
	}
	ok = true
	return &downloadedRelease{Release: Release{Platform: platform, Version: version, SHA256: sum, Bytes: size,
		Uploaded: time.Now(), FileName: a.Name}, tmp: tmp.Name()}, nil
}
