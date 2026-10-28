package main

// Git for projects: clone at creation, one commit per finished round, optional push and
// optional GitHub pull request. It runs the `git` command on the controller and works with any
// HTTPS or SSH remote.
//
// Credentials are stored encrypted (secret.key). An HTTPS token is sent as an
// Authorization header through git's environment-based config (never in the URL, on the command
// line or in .git/config). An SSH key is written to a 0600 temp file for the one command.
// Output is scrubbed of the token before it is logged or shown.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// allowLocalGit lets tests clone from a local bare repository. Never set in production:
// a file:// or path remote would let a project read files off the controller.
var allowLocalGit = false

const defaultGitBranch = "outpost"

var scpLike = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+$`)

func validGitURL(u string) bool {
	if u == "" || strings.ContainsAny(u, " \t\r\n\x00") || strings.HasPrefix(u, "-") {
		return false
	}
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "ssh://") || scpLike.MatchString(u) {
		return true
	}
	return allowLocalGit
}

var branchOK = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

func validBranch(b string) bool {
	return b != "" && len(b) <= 200 && branchOK.MatchString(b) && !strings.HasPrefix(b, "-") && !strings.HasPrefix(b, "/") &&
		!strings.HasSuffix(b, "/") && !strings.HasSuffix(b, ".") && !strings.HasSuffix(b, ".lock") && !strings.Contains(b, "..") && !strings.Contains(b, "//")
}

// git runs one git command for a project in dir and returns its combined output.
func (c *Controller) git(ctx context.Context, p *Project, dir string, args ...string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("git is not installed on the controller (install it, for example: sudo apt install git)")
	}
	p.mu.Lock()
	user, tokEnc, keyEnc := p.GitUser, p.GitTokenEnc, p.GitKeyEnc
	p.mu.Unlock()
	protocols := "https:ssh"
	if allowLocalGit {
		protocols += ":file"
	}
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL="+protocols, "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C")
	token, _ := c.decrypt(tokEnc)
	token = strings.TrimSpace(token)
	if token != "" {
		if user == "" {
			user = "x-access-token"
		}
		auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + token))
		env = append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic "+auth)
	}
	if key, _ := c.decrypt(keyEnc); strings.TrimSpace(key) != "" {
		kf, err := os.CreateTemp("", "outpost-key-*")
		if err != nil {
			return "", err
		}
		defer os.Remove(kf.Name())
		_ = kf.Chmod(0o600)
		_, _ = kf.WriteString(key)
		kf.Close()
		kh := filepath.Join(p.dir, "known_hosts")
		env = append(env, "GIT_SSH_COMMAND=ssh -i "+kf.Name()+" -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="+kh)
	} else {
		env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="+filepath.Join(p.dir, "known_hosts"))
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	s := string(out)
	if token != "" {
		s = strings.ReplaceAll(s, token, "***")
	}
	s = strings.TrimSpace(s)
	if err != nil {
		if ctx.Err() != nil {
			return s, fmt.Errorf("git %s timed out", args[0])
		}
		return s, fmt.Errorf("git %s failed: %s", args[0], firstLine(lastGitLines(s, 3), 400))
	}
	return s, nil
}

func lastGitLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// cloneProject clones p.GitURL into the project's repo and checks out the working branch.
func (c *Controller) cloneProject(ctx context.Context, p *Project) error {
	if !validGitURL(p.GitURL) {
		return errors.New("the git URL must start with https:// or ssh:// (or be user@host:path)")
	}
	if p.GitBranch == "" {
		p.GitBranch = defaultGitBranch
	}
	if !validBranch(p.GitBranch) {
		return errors.New("that branch name is not valid")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if _, err := c.git(ctx, p, p.dir, "clone", "--quiet", "--", p.GitURL, p.repoDir()); err != nil {
		return err
	}
	base, err := c.git(ctx, p, p.repoDir(), "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return err
	}
	p.GitBase = strings.TrimSpace(base)
	if _, err := c.git(ctx, p, p.repoDir(), "rev-parse", "--verify", "-q", "refs/remotes/origin/"+p.GitBranch); err == nil {
		_, err = c.git(ctx, p, p.repoDir(), "checkout", "--quiet", "-B", p.GitBranch, "origin/"+p.GitBranch)
		return err
	}
	_, err = c.git(ctx, p, p.repoDir(), "checkout", "--quiet", "-B", p.GitBranch)
	return err
}

// roundRequest is the user's request that started the task's current round.
func roundRequest(t *Task) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	req := t.Prompt
	for _, m := range t.Messages {
		if m.Round == t.Round && m.Role == "user" && m.Kind == "text" {
			req = m.Text
			break
		}
	}
	return strings.TrimSpace(req)
}

// exportRound commits what the finished round wrote and, if the project asks for it, pushes and
// opens a pull request. Problems are logged on the task; they never fail it, and the commit stays
// in the project's repo either way.
func (c *Controller) exportRound(t *Task, p *Project) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	t.mu.Lock()
	round, id := t.Round, t.ID
	t.mu.Unlock()
	req := roundRequest(t)
	msg := firstLine(req, 72)
	if len(req) > len(msg) {
		body := req
		if len(body) > 4000 {
			body = body[:4000] + "…"
		}
		msg += "\n\n" + body
	}
	msg += fmt.Sprintf("\n\nOutpost task %s, round %d", id, round)
	note := func(level, f string, a ...any) {
		t.logf(level, f, a...)
		t.addMsg(TaskMessage{Role: "event", Kind: "event", Text: fmt.Sprintf(f, a...)})
	}
	repo := p.repoDir()
	if _, err := c.git(ctx, p, repo, "add", "-A"); err != nil {
		note("warn", "Git export: %v", err)
		return
	}
	if st, _ := c.git(ctx, p, repo, "status", "--porcelain"); strings.TrimSpace(st) == "" {
		note("info", "Git export: nothing changed this round, so no commit.")
		return
	}
	if _, err := c.git(ctx, p, repo, "-c", "user.name=Outpost", "-c", "user.email=outpost@localhost", "commit", "--quiet", "-m", msg); err != nil {
		note("warn", "Git export: %v", err)
		return
	}
	sha, _ := c.git(ctx, p, repo, "rev-parse", "--short", "HEAD")
	p.mu.Lock()
	branch, push, pr := p.GitBranch, p.GitPush, p.GitPR
	p.mu.Unlock()
	if !push {
		note("info", "Committed %s to branch %s (push is off for this project).", sha, branch)
		return
	}
	if _, err := c.git(ctx, p, repo, "push", "--quiet", "origin", "HEAD:refs/heads/"+branch); err != nil {
		note("warn", "Committed %s but the push failed: %v", sha, err)
		return
	}
	p.mu.Lock()
	p.LastPush = time.Now().UTC().Format(time.RFC3339)
	p.mu.Unlock()
	_ = p.save()
	note("info", "Committed %s and pushed it to branch %s.", sha, branch)
	if pr {
		if u, err := c.openPullRequest(ctx, p, req, id); err != nil {
			note("warn", "Pull request: %v", err)
		} else if u != "" {
			p.mu.Lock()
			p.LastPR = u
			p.mu.Unlock()
			_ = p.save()
			note("info", "Pull request: %s", u)
		}
	}
}

// githubRepoOf returns owner/repo for a github.com remote URL.
func githubRepoOf(raw string) (string, bool) {
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "/")
	var path string
	switch {
	case strings.HasPrefix(raw, "git@github.com:"):
		path = strings.TrimPrefix(raw, "git@github.com:")
	default:
		u, err := url.Parse(raw)
		if err != nil || (u.Host != "github.com" && u.Host != "www.github.com") {
			return "", false
		}
		path = strings.TrimPrefix(u.Path, "/")
	}
	path = strings.TrimSuffix(path, ".git")
	if parts := strings.Split(path, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return path, true
}

// openPullRequest opens a GitHub pull request from the project's branch to its base. It does
// nothing (and returns no error) when a request for that branch is already open.
func (c *Controller) openPullRequest(ctx context.Context, p *Project, request, taskID string) (string, error) {
	p.mu.Lock()
	repo, ok := githubRepoOf(p.GitURL)
	head, base, tokEnc := p.GitBranch, p.GitBase, p.GitTokenEnc
	p.mu.Unlock()
	if !ok {
		return "", errors.New("only github.com remotes are supported for pull requests")
	}
	token, _ := c.decrypt(tokEnc)
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errors.New("opening a pull request needs an HTTPS token with repo access")
	}
	if head == base {
		return "", errors.New("the branch is the repository's default branch, so there is nothing to compare")
	}
	api := "https://api.github.com"
	if u := os.Getenv("OUTPOST_GITHUB_API"); u != "" {
		api = strings.TrimRight(u, "/")
	}
	body, _ := json.Marshal(map[string]string{
		"title": firstLine(request, 100), "head": head, "base": base,
		"body": "Opened by Outpost for task " + taskID + ".\n\n" + request,
	})
	hr, _ := http.NewRequestWithContext(ctx, "POST", api+"/repos/"+repo+"/pulls", strings.NewReader(string(body)))
	hr.Header.Set("Authorization", "Bearer "+token)
	hr.Header.Set("Accept", "application/vnd.github+json")
	hr.Header.Set("User-Agent", "outpost/"+Version)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(hr)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == 201:
		var out struct {
			HTMLURL string `json:"html_url"`
		}
		_ = json.Unmarshal(b, &out)
		return out.HTMLURL, nil
	case resp.StatusCode == 422 && strings.Contains(string(b), "already exists"):
		return "", nil // the push updated the existing pull request
	}
	return "", fmt.Errorf("GitHub answered %d: %s", resp.StatusCode, firstLine(strings.ReplaceAll(string(b), token, "***"), 200))
}
