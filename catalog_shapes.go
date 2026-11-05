package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

// Catalog models are not on the controller, so their GGUF headers (KV cache
// size, how many weights a token reads) are fetched from Hugging Face once, in
// the background, and kept in catalog_shapes.json. Until a header is known the
// recommendation treats the model as dense and sizes it by the rule of thumb.

func (c *Controller) catalogShapesPath() string {
	return filepath.Join(c.dataDir, "catalog_shapes.json")
}

func (c *Controller) loadCatalogShapes() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.catShapes = map[string]*ModelShape{}
	if b, err := os.ReadFile(c.catalogShapesPath()); err == nil {
		_ = json.Unmarshal(b, &c.catShapes)
		if c.catShapes == nil {
			c.catShapes = map[string]*ModelShape{}
		}
	}
}

// fetchCatalogShapes reads the headers that are still missing and returns how many are.
func (c *Controller) fetchCatalogShapes() int {
	missing := 0
	for _, e := range modelCatalog {
		c.mu.Lock()
		have := c.catShapes[e.ID]
		c.mu.Unlock()
		if have != nil && have.ActiveFrac > 0 {
			continue
		}
		f := mainModelFile(e.Files)
		if f == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), modelShapeTimeout)
		s, err := c.readShapeFromHF(ctx, e.Repo, f)
		cancel()
		if err != nil {
			log.Printf("catalog model %s: could not read its GGUF header: %v", e.ID, err)
			missing++
			continue
		}
		c.mu.Lock()
		c.catShapes[e.ID] = s
		b, _ := json.MarshalIndent(c.catShapes, "", "  ")
		c.mu.Unlock()
		_ = writeFileAtomic(c.catalogShapesPath(), b, 0o644)
	}
	return missing
}

func (c *Controller) catalogShapeLoop() {
	c.loadCatalogShapes()
	time.Sleep(20 * time.Second) // let the controller finish starting
	for c.fetchCatalogShapes() > 0 {
		time.Sleep(30 * time.Minute)
	}
}
