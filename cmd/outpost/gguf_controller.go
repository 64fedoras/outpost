package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"outpost/internal/gguf"
)

// ModelShape is what the KV cache size depends on, read from a GGUF file's header.
// The parser lives in internal/gguf; this alias keeps the name used across the controller.
type ModelShape = gguf.Shape

// modelShapeTimeout bounds reading a header from Hugging Face.
const modelShapeTimeout = 2 * time.Minute

// (for split models, part 1 holds the metadata).
func mainModelFile(files []string) string {
	for _, f := range files {
		if !isMMProj(f) {
			return f
		}
	}
	return ""
}

// readModelShape reads the model's GGUF header from the controller's copy, or
// streams just the header from Hugging Face for a direct model.
func (c *Controller) readModelShape(ctx context.Context, m *ModelRec) (*ModelShape, error) {
	c.mu.Lock()
	storage, repo, hf := m.Storage, m.Repo, mainModelFile(m.HFFiles)
	var local string
	for _, f := range m.Files {
		if !isMMProj(f.Path) {
			local = filepath.Join(c.modelsStoreDir(), m.Name, f.Path)
			break
		}
	}
	c.mu.Unlock()
	if storage == "controller" && local != "" {
		f, err := os.Open(local)
		if err == nil {
			defer f.Close()
			return gguf.ReadShape(f)
		}
	}
	if repo == "" || hf == "" {
		return nil, errors.New("no model file to read")
	}
	return c.readShapeFromHF(ctx, repo, hf)
}

// readShapeFromHF streams just the header of a file on Hugging Face.
func (c *Controller) readShapeFromHF(ctx context.Context, repo, hf string) (*ModelShape, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", hfURL(repo, hf), nil)
	if t := c.hfToken(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() // closing early stops the download after the header
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		return nil, fmt.Errorf("Hugging Face: %s", resp.Status)
	}
	return gguf.ReadShape(resp.Body)
}

// fillModelShape reads and saves a ready model's shape if it is not known yet.
func (c *Controller) fillModelShape(m *ModelRec) {
	c.mu.Lock()
	skip := (m.Shape != nil && !m.Shape.NeedsHeaderRefresh()) || m.Status != "ready" || !strings.HasSuffix(strings.ToLower(mainModelFile(m.HFFiles)), ".gguf")
	c.mu.Unlock()
	if skip {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), modelShapeTimeout)
	defer cancel()
	s, err := c.readModelShape(ctx, m)
	if err != nil {
		log.Printf("model %s: could not read the GGUF header for the context estimate: %v", m.Name, err)
		return
	}
	c.mu.Lock()
	if c.models[m.Name] == m {
		m.Shape = s
		c.saveModelLocked(m)
	}
	c.mu.Unlock()
}
