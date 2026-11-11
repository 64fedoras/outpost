package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Settings live in <data>/settings.json. Secrets (Claude and OpenRouter API
// keys, Hugging Face token) are encrypted with AES-256-GCM using <data>/secret.key, so they never
// sit in plain text in the settings file or in backups of it alone.
type Settings struct {
	AnthropicKeyEnc   string         `json:"anthropic_key_enc,omitempty"`
	HFTokenEnc        string         `json:"hf_token_enc,omitempty"`
	ClaudeModel       string         `json:"claude_model"`
	DefaultCapUSD     float64        `json:"default_cap_usd"`
	MonthlyCapUSD     float64        `json:"monthly_cap_usd,omitempty"` // all cloud planner calls in a calendar month; 0 = off (spend.go)
	WakeForWork       bool           `json:"wake_for_work,omitempty"`   // Wake-on-LAN offline nodes when a task waits for a worker (wol.go)
	DefaultPlanner    string         `json:"default_planner"`           // "claude", "openrouter" or "node:<id>"
	AutoOrder         []string       `json:"auto_order,omitempty"`      // providers "Auto (local + API)" tries, in order: local, claude, openrouter; empty = all three in that order
	OpenRouterKeyEnc  string         `json:"openrouter_key_enc,omitempty"`
	OpenRouterModel   string         `json:"openrouter_model,omitempty"`
	WebSearch         bool           `json:"web_search,omitempty"`        // Claude planner may use the web search tool (paid per search)
	WebAllowPrivate   bool           `json:"web_allow_private,omitempty"` // pasted links may reach internal addresses
	BackupEnabled     bool           `json:"backup_enabled,omitempty"`
	BackupDir         string         `json:"backup_dir,omitempty"`
	BackupPassEnc     string         `json:"backup_pass_enc,omitempty"`
	AutoResumeOff     bool           `json:"auto_resume_off,omitempty"`     // default is on
	BestOfN           string         `json:"best_of_n,omitempty"`           // "off", "hard" (default) or "all": extra copies of subtasks on idle nodes, a judge picks one
	AgentOff          bool           `json:"agent_off,omitempty"`           // fixers and investigators use single-shot requests instead of tools
	Instructions      string         `json:"instructions,omitempty"`        // standing rules copied into every new task (OUTPOST.md text)
	PoolingEnabled    *bool          `json:"pooling_enabled,omitempty"`     // memory pooling (llama.cpp RPC); nil until decided, then off unless a pool already existed
	PoolingNotice     bool           `json:"pooling_notice,omitempty"`      // pooling was switched on by the upgrade because a pool existed; shown once
	AutoFreeOff       bool           `json:"auto_free_off,omitempty"`       // default is on: a node short of disk space deletes least recently used models the controller can copy back
	GitHubUpdatesOff  bool           `json:"github_updates_off,omitempty"`  // default is on: check this repo's releases and download newer binaries
	GitHubAutoInstall bool           `json:"github_auto_install,omitempty"` // also update idle nodes and the controller by itself
	GitHubRepo        string         `json:"github_repo,omitempty"`         // owner/repo trusted for updates; empty = ghDefaultRepo
	GitHubTokenEnc    string         `json:"github_token_enc,omitempty"`    // optional read-only token for a private repo; controller only
	GitHubHoldVersion string         `json:"github_hold_version,omitempty"` // set by a rollback: auto-install skips this version and older
	Sandbox           string         `json:"sandbox,omitempty"`             // default sandbox for exec jobs on nodes: off | basic | strict; empty = basic
	HealthAmber       int            `json:"health_amber,omitempty"`        // node health: points for amber (0 = default 5)
	HealthRed         int            `json:"health_red,omitempty"`          // node health: points for red (0 = default 15)
	IdleUnload        string         `json:"idle_unload,omitempty"`         // default idle unload wait for nodes, as typed ("1hr", "30m", "0" = never); empty = 1 hour
	CleanupDays       int            `json:"cleanup_days,omitempty"`        // remove old round copies, screenshots and result zips of tasks finished this many days ago (0 = never)
	CleanupBuilds     bool           `json:"cleanup_builds,omitempty"`      // also remove their build outputs
	Notify            NotifySettings `json:"notify"`
}

func defaultSettings() Settings {
	return Settings{ClaudeModel: "claude-sonnet-5-5", DefaultCapUSD: 2, DefaultPlanner: "claude"}
}

func (c *Controller) settingsPath() string { return filepath.Join(c.dataDir, "settings.json") }

func (c *Controller) loadSettings() {
	defer func() { c.healthLog().setThresholds(healthAmberOf(c.settings), healthRedOf(c.settings)) }()
	c.settings = defaultSettings()
	if usedBak, err := readWithBackup(c.settingsPath(), func(b []byte) error {
		c.settings = defaultSettings()
		return json.Unmarshal(b, &c.settings)
	}); err != nil {
		c.settings = defaultSettings()
		if !errors.Is(err, os.ErrNotExist) {
			c.noteRecovered("settings.json could not be read (" + err.Error() + "); defaults are in use until you save Settings")
		}
	} else if usedBak {
		c.noteRecovered("settings.json was damaged; loaded the previous copy")
	}
	if _, ok := claudePrices[c.settings.ClaudeModel]; !ok {
		c.settings.ClaudeModel = "claude-sonnet-5-5"
	}
	if c.settings.DefaultCapUSD <= 0 {
		c.settings.DefaultCapUSD = 2
	}
}

// saveSettingsLocked must be called with c.mu held.
func (c *Controller) saveSettingsLocked() error {
	b, _ := json.MarshalIndent(c.settings, "", "  ")
	return writeFileAtomicBackup(c.settingsPath(), b, 0o600)
}

func (c *Controller) secretKey() ([]byte, error) {
	p := filepath.Join(c.dataDir, "secret.key")
	if b, err := os.ReadFile(p); err == nil && len(b) == 32 {
		return b, nil
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(p, k, 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

func (c *Controller) encrypt(plain string) (string, error) {
	k, err := c.secretKey()
	if err != nil {
		return "", err
	}
	blk, _ := aes.NewCipher(k)
	gcm, _ := cipher.NewGCM(blk)
	nonce := make([]byte, gcm.NonceSize())
	_, _ = rand.Read(nonce)
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (c *Controller) decrypt(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	k, err := c.secretKey()
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	blk, _ := aes.NewCipher(k)
	gcm, _ := cipher.NewGCM(blk)
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("bad secret")
	}
	out, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	return string(out), err
}

func (c *Controller) anthropicKey() string {
	c.mu.Lock()
	enc := c.settings.AnthropicKeyEnc
	c.mu.Unlock()
	k, _ := c.decrypt(enc)
	return k
}

func (c *Controller) hfToken() string {
	c.mu.Lock()
	enc := c.settings.HFTokenEnc
	c.mu.Unlock()
	k, _ := c.decrypt(enc)
	return k
}

func keyHint(k string) string {
	if len(k) < 8 {
		return ""
	}
	return "…" + k[len(k)-4:]
}

func (c *Controller) settingsView() map[string]any {
	ak, hf, ork := c.anthropicKey(), c.hfToken(), c.openRouterKey()
	fails, locked := c.lockoutStatus()
	c.mu.Lock()
	defer c.mu.Unlock()
	models := []map[string]any{}
	for _, id := range claudeModelOrder {
		p := claudePrices[id]
		models = append(models, map[string]any{"id": id, "label": p.Label, "in": p.In, "out": p.Out})
	}
	v := map[string]any{
		"has_anthropic_key": ak != "", "anthropic_key_hint": keyHint(ak),
		"has_hf_token": hf != "", "claude_model": c.settings.ClaudeModel,
		"has_openrouter_key": ork != "", "openrouter_key_hint": keyHint(ork), "openrouter_model": c.settings.OpenRouterModel,
		"openrouter_label": openRouterLabel(c.settings.OpenRouterModel),
		"default_cap_usd":  c.settings.DefaultCapUSD, "default_planner": c.settings.DefaultPlanner, "auto_order": normAutoOrder(c.settings.AutoOrder), "monthly_cap_usd": c.settings.MonthlyCapUSD,
		"claude_models": models, "login_failures": fails, "locked_ips": locked,
		"wake_for_work": c.settings.WakeForWork,
		"auto_resume":   !c.settings.AutoResumeOff, "best_of_n": normBestOf(c.settings.BestOfN),
		"agent_mode": !c.settings.AgentOff, "web_search": c.settings.WebSearch, "web_allow_private": c.settings.WebAllowPrivate,
		"auto_free": !c.settings.AutoFreeOff, "pooling_enabled": c.poolingOnLocked(), "pooling_notice": c.settings.PoolingNotice,
		"github_repo": c.ghRepoLocked(), "github_repo_default": ghDefaultRepo, "github_token_set": c.settings.GitHubTokenEnc != "",
		"github_updates": !c.settings.GitHubUpdatesOff, "github_auto_install": c.settings.GitHubAutoInstall,
		"sandbox":      effectiveSandbox(c.settings.Sandbox, ""),
		"health_amber": healthAmberOf(c.settings), "health_red": healthRedOf(c.settings),
		"idle_unload": c.settings.IdleUnload, "idle_unload_effective": formatIdleDuration(idleUnloadFor(c.settings.IdleUnload, "")),
		"instructions": c.settings.Instructions, "backup_enabled": c.settings.BackupEnabled, "last_backup": c.lastBackup, "backup_dir": c.settings.BackupDir, "has_backup_pass": c.settings.BackupPassEnc != "",
	}
	for k, x := range c.notifyView() {
		v[k] = x
	}
	return v
}

func (c *Controller) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, c.settingsView())
}

func (c *Controller) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AnthropicKey    *string   `json:"anthropic_key"`
		HFToken         *string   `json:"hf_token"`
		ClaudeModel     *string   `json:"claude_model"`
		DefaultCapUSD   *float64  `json:"default_cap_usd"`
		WakeForWork     *bool     `json:"wake_for_work"`
		DefaultPlanner  *string   `json:"default_planner"`
		AutoOrder       *[]string `json:"auto_order"`
		HealthAmber     *int      `json:"health_amber"`
		HealthRed       *int      `json:"health_red"`
		OpenRouterKey   *string   `json:"openrouter_key"`
		OpenRouterModel *string   `json:"openrouter_model"`
		Notify          *struct {
			Browser    *bool           `json:"browser"`
			NtfyURL    *string         `json:"ntfy_url"`
			NtfyToken  *string         `json:"ntfy_token"`
			WebhookURL *string         `json:"webhook_url"`
			PublicURL  *string         `json:"public_url"`
			DigestHour *int            `json:"digest_hour"`
			Events     map[string]bool `json:"events"`
		} `json:"notify"`
		AutoResume      *bool   `json:"auto_resume"`
		BestOfN         *string `json:"best_of_n"`
		Instructions    *string `json:"instructions"`
		WebSearch       *bool   `json:"web_search"`
		AgentMode       *bool   `json:"agent_mode"`
		WebAllowPrivate *bool   `json:"web_allow_private"`
		PoolingEnabled  *bool   `json:"pooling_enabled"`
		AutoFree        *bool   `json:"auto_free"`
		GitHubUpdates   *bool   `json:"github_updates"`
		GitHubAuto      *bool   `json:"github_auto_install"`
		GitHubRepo      *string `json:"github_repo"`
		GitHubToken     *string `json:"github_token"` // "" removes
		IdleUnload      *string `json:"idle_unload"`  // "" = default (1 hour)
		Sandbox         *string `json:"sandbox"`      // off | basic | strict
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad json"})
		return
	}
	if req.IdleUnload != nil && strings.TrimSpace(*req.IdleUnload) != "" {
		if _, err := parseIdleDuration(*req.IdleUnload); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
	}
	if req.Sandbox != nil && normSandbox(*req.Sandbox) == "" {
		writeJSON(w, 400, map[string]string{"error": "sandbox must be off, basic or strict"})
		return
	}
	var akEnc, hfEnc, orEnc *string
	for _, s := range []struct {
		in  *string
		out **string
	}{{req.AnthropicKey, &akEnc}, {req.OpenRouterKey, &orEnc}} {
		if s.in == nil {
			continue
		}
		v := strings.TrimSpace(*s.in)
		enc := ""
		if v != "" {
			var err error
			if enc, err = c.encrypt(v); err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
		}
		*s.out = &enc
	}
	if req.HFToken != nil {
		v := strings.TrimSpace(*req.HFToken)
		enc := ""
		if v != "" {
			enc, _ = c.encrypt(v)
		}
		hfEnc = &enc
	}
	ghRepoNew := ""
	if req.GitHubRepo != nil {
		r, err := parseGitHubRepo(*req.GitHubRepo)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		ghRepoNew = r
	}
	var ghTokEnc *string
	if req.GitHubToken != nil {
		enc := ""
		if v := strings.TrimSpace(*req.GitHubToken); v != "" {
			var err error
			if enc, err = c.encrypt(v); err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
		}
		ghTokEnc = &enc
	}
	var ntfyEnc *string
	if req.Notify != nil && req.Notify.NtfyToken != nil {
		enc := ""
		if v := strings.TrimSpace(*req.Notify.NtfyToken); v != "" {
			enc, _ = c.encrypt(v)
		}
		ntfyEnc = &enc
	}
	c.mu.Lock()
	if nr := req.Notify; nr != nil {
		ns := &c.settings.Notify
		if nr.Browser != nil {
			ns.Browser = *nr.Browser
		}
		for _, f := range []struct {
			in  *string
			out *string
		}{{nr.NtfyURL, &ns.NtfyURL}, {nr.WebhookURL, &ns.WebhookURL}, {nr.PublicURL, &ns.PublicURL}} {
			if f.in != nil {
				*f.out = strings.TrimSpace(*f.in)
			}
		}
		if ntfyEnc != nil {
			ns.NtfyTokenEnc = *ntfyEnc
		}
		if nr.DigestHour != nil && *nr.DigestHour >= 0 && *nr.DigestHour <= 23 {
			ns.DigestHour = *nr.DigestHour
			if ns.DigestHour == 0 {
				ns.DigestHour = 24 // 0 in the file means "default (8)"; midnight is stored as 24
			}
		}
		for _, e := range notifyEvents {
			if on, ok := nr.Events[e.ID]; ok {
				if ns.Events == nil {
					ns.Events = map[string]bool{}
				}
				ns.Events[e.ID] = on
			}
		}
	}
	if akEnc != nil {
		c.settings.AnthropicKeyEnc = *akEnc
	}
	if hfEnc != nil {
		c.settings.HFTokenEnc = *hfEnc
	}
	if orEnc != nil {
		c.settings.OpenRouterKeyEnc = *orEnc
	}
	if req.AutoResume != nil {
		c.settings.AutoResumeOff = !*req.AutoResume
	}
	if req.BestOfN != nil {
		c.settings.BestOfN = normBestOf(*req.BestOfN)
	}
	if req.OpenRouterModel != nil {
		c.settings.OpenRouterModel = strings.TrimSpace(*req.OpenRouterModel)
	}
	if req.ClaudeModel != nil {
		if _, ok := claudePrices[*req.ClaudeModel]; ok {
			c.settings.ClaudeModel = *req.ClaudeModel
		}
	}
	if req.WakeForWork != nil {
		c.settings.WakeForWork = *req.WakeForWork
	}
	if req.DefaultCapUSD != nil && *req.DefaultCapUSD > 0 && *req.DefaultCapUSD <= 1000 {
		c.settings.DefaultCapUSD = *req.DefaultCapUSD
	}
	if req.DefaultPlanner != nil {
		c.settings.DefaultPlanner = *req.DefaultPlanner
	}
	if req.AutoOrder != nil {
		c.settings.AutoOrder = normAutoOrder(*req.AutoOrder)
	}
	if req.Instructions != nil {
		c.settings.Instructions = clipInstructions(*req.Instructions)
	}
	if req.WebSearch != nil {
		c.settings.WebSearch = *req.WebSearch
	}
	if req.AgentMode != nil {
		c.settings.AgentOff = !*req.AgentMode
	}
	if req.WebAllowPrivate != nil {
		c.settings.WebAllowPrivate = *req.WebAllowPrivate
	}
	if req.GitHubRepo != nil {
		old, _ := parseGitHubRepo(c.settings.GitHubRepo)
		if ghRepoNew == ghDefaultRepo {
			c.settings.GitHubRepo = ""
		} else {
			c.settings.GitHubRepo = ghRepoNew
		}
		if ghRepoNew != old { // another repository: its versions and rollback hold are unrelated
			c.settings.GitHubHoldVersion = ""
			c.ghState = ghUpdateState{}
		}
	}
	if ghTokEnc != nil {
		c.settings.GitHubTokenEnc = *ghTokEnc
	}
	if req.AutoFree != nil {
		c.settings.AutoFreeOff = !*req.AutoFree
	}
	if req.GitHubUpdates != nil {
		c.settings.GitHubUpdatesOff = !*req.GitHubUpdates
	}
	if req.GitHubAuto != nil {
		c.settings.GitHubAutoInstall = *req.GitHubAuto
	}
	if req.HealthAmber != nil || req.HealthRed != nil {
		a, r := c.settings.HealthAmber, c.settings.HealthRed
		if req.HealthAmber != nil {
			a = *req.HealthAmber
		}
		if req.HealthRed != nil {
			r = *req.HealthRed
		}
		if a < 1 || r <= a || r > 1000 {
			c.mu.Unlock()
			writeJSON(w, 400, map[string]string{"error": "red must be more points than amber, and amber at least 1"})
			return
		}
		c.settings.HealthAmber, c.settings.HealthRed = a, r
		c.healthLog().setThresholds(a, r)
	}
	idleChanged := false
	if req.IdleUnload != nil {
		c.settings.IdleUnload = strings.TrimSpace(*req.IdleUnload)
		idleChanged = true
	}
	if req.Sandbox != nil {
		c.settings.Sandbox = normSandbox(*req.Sandbox)
		idleChanged = true // pushes node settings
	}
	poolingOff := false
	if req.PoolingEnabled != nil {
		poolingOff = c.poolingOnLocked() && !*req.PoolingEnabled
		c.settings.PoolingEnabled = req.PoolingEnabled
		c.settings.PoolingNotice = false
	}
	err := c.saveSettingsLocked()
	c.mu.Unlock()
	if poolingOff {
		c.disablePooling()
	}
	if idleChanged && err == nil {
		c.pushNodeSettings()
	}
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, c.settingsView())
}

func (c *Controller) ghRepoLocked() string {
	if r, err := parseGitHubRepo(c.settings.GitHubRepo); err == nil {
		return r
	}
	return ghDefaultRepo
}

// normAutoOrder keeps the known providers of "Auto (local + API)" once each, in
// the given order; nothing valid means the default order.
func normAutoOrder(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range in {
		if (p == "local" || p == "claude" || p == "openrouter") && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{"local", "claude", "openrouter"}
	}
	return out
}
