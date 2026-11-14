package main

// Model catalog updates without a new release (idea 50). Every release on
// GitHub carries catalog.json (written by `outpost catalog-json` in CI and
// listed in SHA256SUMS). When the controller checks GitHub for updates it
// also fetches that file from the latest release, verifies its SHA-256,
// checks every entry, and uses it on top of the catalog built into this
// binary: an entry with a known ID replaces the built-in one, a new ID is
// added. Built-in entries are never removed. The verified file is kept in
// the data folder, so it survives a restart. The fit check still runs on
// every entry, so a wrong size only gives a wrong suggestion.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
)

const (
	catalogAsset     = "catalog.json"
	maxCatalogBytes  = 1 << 20
	maxCatalogEntrys = 200
)

type catalogFile struct {
	Schema  int            `json:"schema"`
	Version string         `json:"version"` // Outpost version it was published with
	Entries []CatalogEntry `json:"entries"`
}

var activeCatalog atomic.Pointer[[]CatalogEntry]

// catalogNow is the catalog in use: built in, plus a verified update if any.
func catalogNow() []CatalogEntry {
	if p := activeCatalog.Load(); p != nil {
		return *p
	}
	return modelCatalog
}

var (
	catIDRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,80}$`)
	catRepoRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	catFileRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*\.gguf$`)
)

// checkCatalog validates a downloaded catalog.
func checkCatalog(cf *catalogFile) error {
	if cf.Schema != 1 {
		return fmt.Errorf("catalog schema %d is not supported by this version", cf.Schema)
	}
	if len(cf.Entries) == 0 || len(cf.Entries) > maxCatalogEntrys {
		return fmt.Errorf("catalog has %d entries", len(cf.Entries))
	}
	seen := map[string]bool{}
	for i, e := range cf.Entries {
		where := fmt.Sprintf("entry %d (%s)", i+1, e.ID)
		switch {
		case !catIDRe.MatchString(e.ID):
			return errors.New(where + ": bad id")
		case seen[e.ID]:
			return errors.New(where + ": id used twice")
		case strings.TrimSpace(e.Label) == "" || len(e.Label) > 120:
			return errors.New(where + ": bad label")
		case !catRepoRe.MatchString(e.Repo):
			return errors.New(where + ": bad Hugging Face repo")
		case len(e.Files) == 0 || len(e.Files) > 16:
			return errors.New(where + ": no model files")
		case e.Bytes <= 0 || e.Bytes > 2<<40:
			return errors.New(where + ": bad size")
		case e.Ctx < 0 || e.Ctx > 1<<20 || e.MinRAMGB < 0 || e.MinRAMGB > 4096:
			return errors.New(where + ": bad context or RAM figure")
		case len(e.Notes) > 600 || len(e.For) > 200:
			return errors.New(where + ": text too long")
		}
		for _, f := range append(append([]string{}, e.Files...), e.MMProj) {
			if f == "" && e.MMProj == "" {
				continue
			}
			if f != "" && (!catFileRe.MatchString(f) || strings.Contains(f, "..")) {
				return errors.New(where + ": bad file name " + f)
			}
		}
		seen[e.ID] = true
	}
	return nil
}

// mergeCatalog lays the update over the built-in list: same ID replaces, new IDs are added at the end.
func mergeCatalog(base, upd []CatalogEntry) []CatalogEntry {
	byID := map[string]CatalogEntry{}
	for _, e := range upd {
		byID[e.ID] = e
	}
	out := make([]CatalogEntry, 0, len(base)+len(upd))
	used := map[string]bool{}
	for _, e := range base {
		if u, ok := byID[e.ID]; ok {
			e = u
		}
		used[e.ID] = true
		out = append(out, e)
	}
	for _, e := range upd {
		if !used[e.ID] {
			out = append(out, e)
		}
	}
	return out
}

func (c *Controller) catalogPath() string { return filepath.Join(c.dataDir, "catalog.json") }

// applyCatalog checks and activates a catalog file's bytes; with save it also keeps them.
func (c *Controller) applyCatalog(b []byte, save bool) (*catalogFile, error) {
	var cf catalogFile
	if err := json.Unmarshal(b, &cf); err != nil {
		return nil, fmt.Errorf("catalog is not valid JSON: %v", err)
	}
	if err := checkCatalog(&cf); err != nil {
		return nil, err
	}
	merged := mergeCatalog(modelCatalog, cf.Entries)
	activeCatalog.Store(&merged)
	if save {
		if err := writeFileAtomic(c.catalogPath(), b, 0o600); err != nil {
			return &cf, err
		}
	}
	c.mu.Lock()
	c.ghState.Catalog = cf.Version
	c.mu.Unlock()
	return &cf, nil
}

// loadSavedCatalog activates the catalog kept from an earlier check.
func (c *Controller) loadSavedCatalog() {
	b, err := os.ReadFile(c.catalogPath())
	if err != nil {
		return
	}
	if _, err := c.applyCatalog(b, false); err != nil {
		log.Printf("saved model catalog ignored: %v", err)
	}
}

// updateCatalog fetches catalog.json from a release when it is newer than the one in use.
func (c *Controller) updateCatalog(ctx context.Context, src updSource, rel ghLatest, latest string, sums map[string]string, token string) error {
	c.mu.Lock()
	have := c.ghState.Catalog
	c.mu.Unlock()
	if have != "" && compareVersions(latest, have) <= 0 {
		return nil
	}
	if compareVersions(latest, Version) < 0 {
		return nil // an older release's catalog is no newer than the built-in one
	}
	var asset *ghAsset
	for i := range rel.Assets {
		if rel.Assets[i].Name == catalogAsset {
			asset = &rel.Assets[i]
		}
	}
	want := sums[catalogAsset]
	if asset == nil || want == "" {
		return nil // releases before catalog updates have none
	}
	resp, err := src.get(ctx, asset.downloadURL(token != ""), "application/octet-stream", token)
	if err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes+1))
	resp.Body.Close()
	if err != nil {
		return err
	}
	if len(b) > maxCatalogBytes {
		return errors.New("catalog.json is too large")
	}
	if sha256Hex(b) != want {
		return errors.New("catalog.json does not match SHA256SUMS; ignored")
	}
	_, err = c.applyCatalog(b, true)
	return err
}

// runCatalogJSON prints the built-in catalog as catalog.json (used by CI).
func runCatalogJSON() {
	b, _ := json.MarshalIndent(catalogFile{Schema: 1, Version: Version, Entries: modelCatalog}, "", "  ")
	os.Stdout.Write(append(b, '\n'))
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
