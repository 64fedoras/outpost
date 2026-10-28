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

// Settings live in <data>/settings.json. Secrets (Claude API key, Hugging Face
// and GitHub tokens) are encrypted with AES-256-GCM using <data>/secret.key, so they never
// sit in plain text in the settings file or in backups of it alone.
type Settings struct {
	AnthropicKeyEnc string  `json:"anthropic_key_enc,omitempty"`
	HFTokenEnc      string  `json:"hf_token_enc,omitempty"`
	ClaudeModel     string  `json:"claude_model"`
	DefaultCapUSD   float64 `json:"default_cap_usd"`
	DefaultPlanner  string  `json:"default_planner"` // "claude" or "node:<id>"
	BackupEnabled   bool    `json:"backup_enabled,omitempty"`
	BackupDir       string  `json:"backup_dir,omitempty"`
	BackupPassEnc   string  `json:"backup_pass_enc,omitempty"`
	GitHubRepo      string  `json:"github_repo,omitempty"`      // Outpost releases come from here; "" means defaultOutpostRepo
	GitHubTokenEnc  string  `json:"github_token_enc,omitempty"` // for a private repo
}

func defaultSettings() Settings {
	return Settings{ClaudeModel: "claude-sonnet-5-5", DefaultCapUSD: 2, DefaultPlanner: "claude"}
}

func (c *Controller) settingsPath() string { return filepath.Join(c.dataDir, "settings.json") }

func (c *Controller) loadSettings() {
	c.settings = defaultSettings()
	if b, err := os.ReadFile(c.settingsPath()); err == nil {
		_ = json.Unmarshal(b, &c.settings)
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
	return os.WriteFile(c.settingsPath(), b, 0o600)
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
	if err := os.WriteFile(p, k, 0o600); err != nil {
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
	ak, hf := c.anthropicKey(), c.hfToken()
	fails, locked := c.lockoutStatus()
	c.mu.Lock()
	defer c.mu.Unlock()
	models := []map[string]any{}
	for _, id := range claudeModelOrder {
		p := claudePrices[id]
		models = append(models, map[string]any{"id": id, "label": p.Label, "in": p.In, "out": p.Out})
	}
	return map[string]any{
		"has_anthropic_key": ak != "", "anthropic_key_hint": keyHint(ak),
		"has_hf_token": hf != "", "claude_model": c.settings.ClaudeModel,
		"default_cap_usd": c.settings.DefaultCapUSD, "default_planner": c.settings.DefaultPlanner,
		"claude_models": models, "login_failures": fails, "locked_ips": locked,
		"github_repo": repoOrDefault(c.settings.GitHubRepo), "has_github_token": c.settings.GitHubTokenEnc != "",
		"backup_enabled": c.settings.BackupEnabled, "backup_dir": c.settings.BackupDir, "has_backup_pass": c.settings.BackupPassEnc != "",
	}
}

func (c *Controller) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, c.settingsView())
}

func (c *Controller) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AnthropicKey   *string  `json:"anthropic_key"`
		HFToken        *string  `json:"hf_token"`
		ClaudeModel    *string  `json:"claude_model"`
		DefaultCapUSD  *float64 `json:"default_cap_usd"`
		DefaultPlanner *string  `json:"default_planner"`
		GitHubRepo     *string  `json:"github_repo"`
		GitHubToken    *string  `json:"github_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad json"})
		return
	}
	var akEnc, hfEnc *string
	if req.AnthropicKey != nil {
		v := strings.TrimSpace(*req.AnthropicKey)
		enc := ""
		if v != "" {
			var err error
			if enc, err = c.encrypt(v); err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
		}
		akEnc = &enc
	}
	if req.HFToken != nil {
		v := strings.TrimSpace(*req.HFToken)
		enc := ""
		if v != "" {
			enc, _ = c.encrypt(v)
		}
		hfEnc = &enc
	}
	var ghEnc, ghRepo *string
	if req.GitHubToken != nil {
		v := strings.TrimSpace(*req.GitHubToken)
		enc := ""
		if v != "" {
			enc, _ = c.encrypt(v)
		}
		ghEnc = &enc
	}
	if req.GitHubRepo != nil {
		v, err := normalizeRepo(*req.GitHubRepo)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		if v == defaultOutpostRepo {
			v = ""
		}
		ghRepo = &v
	}
	if ghEnc != nil || ghRepo != nil {
		c.gh.mu.Lock()
		c.gh.releases = nil // list again with the new repo or token
		c.gh.mu.Unlock()
	}
	c.mu.Lock()
	if ghEnc != nil {
		c.settings.GitHubTokenEnc = *ghEnc
	}
	if ghRepo != nil {
		c.settings.GitHubRepo = *ghRepo
	}
	if akEnc != nil {
		c.settings.AnthropicKeyEnc = *akEnc
	}
	if hfEnc != nil {
		c.settings.HFTokenEnc = *hfEnc
	}
	if req.ClaudeModel != nil {
		if _, ok := claudePrices[*req.ClaudeModel]; ok {
			c.settings.ClaudeModel = *req.ClaudeModel
		}
	}
	if req.DefaultCapUSD != nil && *req.DefaultCapUSD > 0 && *req.DefaultCapUSD <= 1000 {
		c.settings.DefaultCapUSD = *req.DefaultCapUSD
	}
	if req.DefaultPlanner != nil {
		c.settings.DefaultPlanner = *req.DefaultPlanner
	}
	err := c.saveSettingsLocked()
	c.mu.Unlock()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, c.settingsView())
}

func repoOrDefault(r string) string {
	if r == "" {
		return defaultOutpostRepo
	}
	return r
}
