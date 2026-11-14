package main

// Where release updates come from. The repository can be on GitHub (github.com or GitHub
// Enterprise), GitLab (gitlab.com or self-hosted) or Gitea/Forgejo (Codeberg or self-hosted).
// Each host has its own releases API; this file turns all three into the ghLatest/ghAsset shape
// that controller_ghupdate.go already works with, so SHA256SUMS checks, the version check of the
// binary, branch builds and rollback are the same whatever the host.
//
// The token, if any, is only sent to the origin (scheme, host and port) of the repository's API.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	kindGitHub = "github"
	kindGitLab = "gitlab"
	kindGitea  = "gitea" // also Forgejo and Codeberg, which keep Gitea's API
)

// updSource is a parsed "Repository to trust for updates".
type updSource struct {
	Kind   string // kindGitHub, kindGitLab, kindGitea; "" until the server has been asked
	Scheme string // "https", or "http" for a server on the owner's own network
	Host   string // lowercase host[:port]
	Path   string // "owner/repo"; GitLab may have subgroups ("group/sub/repo")
}

// Hosts known by name, so they need no probe.
var knownHostKinds = map[string]string{
	"github.com": kindGitHub, "gitlab.com": kindGitLab, "codeberg.org": kindGitea,
}

func validHostKind(k string) bool {
	return k == "" || k == kindGitHub || k == kindGitLab || k == kindGitea
}

func hostKindLabel(k string) string {
	switch k {
	case kindGitLab:
		return "GitLab"
	case kindGitea:
		return "Gitea"
	case kindGitHub:
		return "GitHub"
	}
	return "the git server"
}

func defaultSource() updSource {
	return updSource{Kind: kindGitHub, Scheme: "https", Host: "github.com", Path: ghDefaultRepo}
}

// URL is the repository's web address, as shown to the owner.
func (s updSource) URL() string { return s.Scheme + "://" + s.Host + "/" + s.Path }

// stored is what goes into Settings.GitHubRepo: plain owner/repo for github.com (what versions
// before other hosts were supported read), the full address otherwise.
func (s updSource) stored() string {
	if s.Host == "github.com" && s.Scheme == "https" {
		if s.Path == ghDefaultRepo {
			return ""
		}
		return s.Path
	}
	return s.URL()
}

func (s updSource) origin() string { return s.Scheme + "://" + s.Host }

func (s updSource) label() string { return hostKindLabel(s.Kind) }

func (s updSource) apiBase() string {
	switch s.Kind {
	case kindGitLab:
		return s.origin() + "/api/v4"
	case kindGitea:
		return s.origin() + "/api/v1"
	}
	if s.Host == "github.com" {
		return ghAPIBase()
	}
	return s.origin() + "/api/v3" // GitHub Enterprise Server
}

// tokenOrigin is the only scheme://host[:port] that ever receives the token.
func (s updSource) tokenOrigin() string {
	u, err := url.Parse(s.apiBase())
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

func (s updSource) repoAPI() string {
	if s.Kind == kindGitLab {
		return s.apiBase() + "/projects/" + url.PathEscape(s.Path)
	}
	return s.apiBase() + "/repos/" + s.Path
}

func (s updSource) jsonAccept() string {
	if s.Kind == kindGitHub {
		return "application/vnd.github+json"
	}
	return "application/json"
}

var (
	srcHostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?)*(:[0-9]{1,5})?$`)
	srcSegRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	srcSSHRe  = regexp.MustCompile(`^(?:git@([A-Za-z0-9.-]+):|ssh://git@([A-Za-z0-9.-]+)/)(.+)$`)
)

var errSourceShape = errors.New("use an address like https://github.com/owner/repo, https://gitlab.example.com/group/repo or https://gitea.example.com/owner/repo")

// parseUpdateSource turns what the owner typed into a source. It accepts a web address on any
// host (https, or http for a LAN server), the clone address (https or git@host:path), or plain
// owner/repo for github.com. kind is the "Server type" setting: "" decides from the host name,
// or later by asking the server (resolveSource). Empty input means the default repository.
func parseUpdateSource(in, kind string) (updSource, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return defaultSource(), nil
	}
	if !validHostKind(kind) {
		return updSource{}, errors.New("unknown server type")
	}
	scheme, hostGiven := "https", false
	if m := srcSSHRe.FindStringSubmatch(s); m != nil {
		s, hostGiven = m[1]+m[2]+"/"+m[3], true
	} else if i := strings.Index(s, "://"); i >= 0 {
		scheme, hostGiven = strings.ToLower(s[:i]), true
		if scheme != "https" && scheme != "http" {
			return updSource{}, errors.New("use an https:// address")
		}
		s = s[i+3:]
	}
	if strings.ContainsAny(s, "?#@ \t\\%") {
		return updSource{}, errSourceShape
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	host := "github.com"
	if hostGiven || strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost" {
		host, parts = strings.ToLower(parts[0]), parts[1:]
	} else if len(parts) != 2 {
		return updSource{}, errSourceShape // a bare path is owner/repo on github.com
	}
	host = strings.TrimPrefix(host, "www.")
	if !srcHostRe.MatchString(host) {
		return updSource{}, errors.New("that is not a valid server name")
	}
	name, _, _ := strings.Cut(host, ":")
	if known, ok := knownHostKinds[name]; ok {
		if scheme != "https" {
			return updSource{}, errors.New(name + " needs an https:// address")
		}
		kind = known
	}
	var path []string
	for _, p := range parts {
		if p == "-" { // GitLab: /group/repo/-/releases
			break
		}
		if p == "" || p == "." || p == ".." || !srcSegRe.MatchString(p) {
			return updSource{}, errSourceShape
		}
		path = append(path, p)
	}
	if len(path) > 20 {
		return updSource{}, errSourceShape
	}
	src := updSource{Scheme: scheme, Host: host, Path: strings.Join(path, "/")}
	if kind == "" {
		if len(path) < 2 {
			return updSource{}, errSourceShape
		}
		path[len(path)-1] = strings.TrimSuffix(path[len(path)-1], ".git")
		src.Path = strings.Join(path, "/")
		return src, nil
	}
	return src.withKind(kind)
}

// withKind fixes the server type and trims the path to what that server's repositories look
// like: owner/repo on GitHub and Gitea (anything after it is a page of the repository), any depth
// on GitLab.
func (s updSource) withKind(kind string) (updSource, error) {
	s.Kind = kind
	parts := strings.Split(s.Path, "/")
	if kind != kindGitLab && len(parts) > 2 {
		parts = parts[:2]
	}
	if len(parts) < 2 {
		return updSource{}, errSourceShape
	}
	parts[len(parts)-1] = strings.TrimSuffix(parts[len(parts)-1], ".git")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return updSource{}, errSourceShape
		}
	}
	if kind == kindGitHub && !ghOwnerRe.MatchString(parts[0]) {
		return updSource{}, errors.New("that does not look like a GitHub owner/repo name")
	}
	s.Path = strings.Join(parts, "/")
	return s, nil
}

// detectedKinds caches what a self-hosted server said it is, by origin.
var detectedKinds sync.Map

// resolveSource asks a server of unknown type what it is, unless that is already known.
func resolveSource(ctx context.Context, s updSource) (updSource, error) {
	if s.Kind != "" {
		return s, nil
	}
	if k, ok := detectedKinds.Load(s.origin()); ok {
		return s.withKind(k.(string))
	}
	k, err := detectHostKind(ctx, s.origin())
	if err != nil {
		return updSource{}, err
	}
	detectedKinds.Store(s.origin(), k)
	return s.withKind(k)
}

// detectHostKind asks well-known, unauthenticated version endpoints. No token is sent.
func detectHostKind(ctx context.Context, origin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	probe := func(path string) (int, http.Header, []byte) {
		req, err := http.NewRequestWithContext(ctx, "GET", origin+path, nil)
		if err != nil {
			return 0, nil, nil
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "outpost/"+Version)
		resp, err := updClient().Do(req)
		if err != nil {
			return 0, nil, nil
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return resp.StatusCode, resp.Header, b
	}
	var v struct {
		Version string `json:"version"`
	}
	code, _, b := probe("/api/v1/version") // Gitea and Forgejo: public
	if code == 0 {
		return "", fmt.Errorf("could not reach %s", origin)
	}
	if code == 200 && json.Unmarshal(b, &v) == nil && v.Version != "" {
		return kindGitea, nil
	}
	if code, _, b := probe("/api/v4/version"); (code == 200 && json.Unmarshal(b, &v) == nil && v.Version != "") ||
		(code == 401 && strings.Contains(string(b), "Unauthorized")) { // GitLab wants a token here
		return kindGitLab, nil
	}
	if _, h, _ := probe("/api/v3/meta"); h != nil && (h.Get("X-GitHub-Enterprise-Version") != "" || h.Get("X-GitHub-Request-Id") != "") {
		return kindGitHub, nil
	}
	return "", fmt.Errorf("could not tell what kind of git server %s is; pick the server type next to the repository address", origin)
}

func sameOrigin(raw, origin string) bool {
	u, err := url.Parse(raw)
	if err != nil || origin == "" {
		return false
	}
	return strings.ToLower(u.Scheme+"://"+u.Host) == origin
}

// updClient never follows a redirect from https to http, and drops the token on any redirect
// that leaves the original origin (Go keeps it for a subdomain or another port).
func updClient() *http.Client {
	return &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("refusing a redirect from https to http")
		}
		if !strings.EqualFold(req.URL.Scheme+"://"+req.URL.Host, via[0].URL.Scheme+"://"+via[0].URL.Host) {
			req.Header.Del("Authorization")
		}
		return nil
	}}
}

// get fetches a URL for this source. The token goes only to the source's API origin.
func (s updSource) get(ctx context.Context, rawURL, accept, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "outpost/"+Version)
	if token != "" && sameOrigin(rawURL, s.tokenOrigin()) {
		if s.Kind == kindGitea {
			req.Header.Set("Authorization", "token "+token)
		} else { // GitHub, and GitLab personal/project tokens as OAuth bearer tokens
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	resp, err := updClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, srcStatusError(s.Kind, resp.StatusCode, resp.Status, token != "")
	}
	return resp, nil
}

func (s updSource) getJSON(ctx context.Context, rawURL, token string, out any) error {
	resp, err := s.get(ctx, rawURL, s.jsonAccept(), token)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, int64(ghListLimit))).Decode(out)
}

// srcStatusError turns an HTTP status into a message that says what to fix.
func srcStatusError(kind string, code int, status string, hadToken bool) error {
	name := hostKindLabel(kind)
	switch code {
	case 401:
		return errors.New(name + " rejected the access token (wrong, expired or revoked)")
	case 404:
		if hadToken {
			need := map[string]string{
				kindGitHub: "read access to Contents and Metadata for that repository",
				kindGitLab: "the read_api scope and at least Reporter access",
				kindGitea:  "the read:repository scope",
			}[kind]
			if need == "" {
				need = "read access to the repository"
			}
			return fmt.Errorf("%s: repository or release not found, or the token has no access to it (it needs %s)", name, need)
		}
		return errors.New(name + ": repository or release not found; a private repository needs an access token")
	case 403, 429:
		return fmt.Errorf("%s: %s (rate limit reached, or the token lacks permission)", name, status)
	}
	return fmt.Errorf("%s: %s", name, status)
}

// relEntry is one release in a list.
type relEntry struct {
	ghLatest
	Prerelease bool `json:"prerelease"`
	Draft      bool `json:"draft"`
}

// glRelease is GitLab's release shape: files are "links", often into the package registry.
type glRelease struct {
	Tag      string `json:"tag_name"`
	Upcoming bool   `json:"upcoming_release"`
	Assets   struct {
		Links []struct {
			Name   string `json:"name"`
			URL    string `json:"url"`
			Direct string `json:"direct_asset_url"`
		} `json:"links"`
	} `json:"assets"`
}

func (g glRelease) entry() relEntry {
	e := relEntry{ghLatest: ghLatest{Tag: g.Tag}, Draft: g.Upcoming}
	// GitLab has no prerelease flag; a suffix after the version (v0.12.19-dev.3) marks one.
	e.Prerelease = strings.Contains(strings.TrimPrefix(g.Tag, "v"), "-")
	for _, l := range g.Assets.Links {
		a := ghAsset{Name: l.Name, URL: l.Direct, APIURL: l.URL}
		if a.URL == "" {
			a.URL = l.URL
		}
		e.Assets = append(e.Assets, a)
	}
	return e
}

// listReleases returns the repository's recent releases, newest first as the server orders them.
func (s updSource) listReleases(ctx context.Context, token string) ([]relEntry, error) {
	if s.Kind == kindGitLab {
		var list []glRelease
		if err := s.getJSON(ctx, s.repoAPI()+"/releases?per_page=100&order_by=released_at&sort=desc", token, &list); err != nil {
			return nil, err
		}
		out := make([]relEntry, 0, len(list))
		for _, g := range list {
			out = append(out, g.entry())
		}
		return out, nil
	}
	var list []relEntry
	// per_page for GitHub, limit for Gitea (which caps it at its own maximum, 50 by default).
	err := s.getJSON(ctx, s.repoAPI()+"/releases?per_page=100&limit=50", token, &list)
	return list, err
}

// latestRelease returns the newest normal release (no prerelease, no draft). GitHub has an
// endpoint for it; for the others the highest version in the list is used.
func (s updSource) latestRelease(ctx context.Context, token string) (ghLatest, error) {
	if s.Kind == kindGitHub {
		var rel ghLatest
		err := s.getJSON(ctx, s.repoAPI()+"/releases/latest", token, &rel)
		return rel, err
	}
	list, err := s.listReleases(ctx, token)
	if err != nil {
		return ghLatest{}, err
	}
	var best *relEntry
	for i := range list {
		r := &list[i]
		v := strings.TrimPrefix(r.Tag, "v")
		if r.Draft || r.Prerelease || isPrerelease(v) || compareVersions(v, "0.0.1") <= 0 {
			continue
		}
		if best == nil || compareVersions(v, strings.TrimPrefix(best.Tag, "v")) > 0 {
			best = r
		}
	}
	if best == nil {
		return ghLatest{}, errors.New(s.label() + ": the repository has no release yet")
	}
	return best.ghLatest, nil
}

// repoPrivate reports whether the repository is private (GitLab "internal" counts as private).
func (s updSource) repoPrivate(ctx context.Context, token string) (bool, error) {
	var meta struct {
		Private    bool   `json:"private"`
		Visibility string `json:"visibility"`
	}
	if err := s.getJSON(ctx, s.repoAPI(), token, &meta); err != nil {
		return false, err
	}
	if s.Kind == kindGitLab {
		return meta.Visibility != "public", nil
	}
	return meta.Private, nil
}
