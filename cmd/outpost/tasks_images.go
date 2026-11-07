package main

// Image attachments (roadmap row 19). Images are stored in the task folder
// (outside repo/, so they are not part of the zip) and shown to the planner
// only: Claude and OpenRouter as image input, a local planner as image_url
// parts for a vision model (one loaded with its --mmproj file). Nodes get the
// bytes inside the inference request and keep nothing.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxTaskImages     = 5
	maxImageBytes     = 5 << 20 // Claude's limit per image
	maxImageBodyBytes = 40 << 20
)

// imageUpload is one attachment as the web UI sends it.
type imageUpload struct {
	Name string `json:"name"`
	Data string `json:"data"` // base64, or a data: URI
}

var imageExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif"}

// decodeImages validates uploads: size, count and real content type (sniffed,
// not trusted from the name).
func decodeImages(in []imageUpload) ([]llmImage, error) {
	if len(in) > maxTaskImages {
		return nil, fmt.Errorf("at most %d images per message", maxTaskImages)
	}
	out := make([]llmImage, 0, len(in))
	for _, u := range in {
		data := u.Data
		if strings.HasPrefix(data, "data:") {
			if i := strings.Index(data, ","); i >= 0 {
				data = data[i+1:]
			}
		}
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil || len(raw) == 0 {
			return nil, fmt.Errorf("%s: not a readable image", u.Name)
		}
		if len(raw) > maxImageBytes {
			return nil, fmt.Errorf("%s is larger than %d MB", u.Name, maxImageBytes>>20)
		}
		media := http.DetectContentType(raw)
		if _, ok := imageExt[media]; !ok {
			return nil, fmt.Errorf("%s: only PNG, JPEG, WebP and GIF images are supported", u.Name)
		}
		out = append(out, llmImage{Media: media, B64: base64.StdEncoding.EncodeToString(raw)})
	}
	return out, nil
}

func (t *Task) imagesDir() string { return filepath.Join(t.dir, "images") }

// saveImages writes images to the task folder and returns their file names.
func (t *Task) saveImages(imgs []llmImage) ([]string, error) {
	if len(imgs) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(t.imagesDir(), 0o755); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(imgs))
	for _, img := range imgs {
		raw, _ := base64.StdEncoding.DecodeString(img.B64)
		name := newID("img") + imageExt[img.Media]
		if err := os.WriteFile(filepath.Join(t.imagesDir(), name), raw, 0o644); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

// roundImages loads the images attached in the task's current round.
func (t *Task) roundImages() []llmImage {
	t.mu.Lock()
	var names []string
	for _, m := range t.Messages {
		if m.Round == t.Round && m.Role == "user" && !m.Superseded {
			names = append(names, m.Images...)
		}
	}
	t.mu.Unlock()
	var out []llmImage
	for _, n := range names {
		raw, err := os.ReadFile(filepath.Join(t.imagesDir(), filepath.Base(n)))
		if err != nil {
			continue
		}
		media := http.DetectContentType(raw)
		if _, ok := imageExt[media]; ok {
			out = append(out, llmImage{Media: media, B64: base64.StdEncoding.EncodeToString(raw)})
		}
	}
	return out
}

var errNoVisionLocal = errors.New("this planner's model cannot read images. Load a vision model (a catalog model marked Vision, such as Qwen3.6 or Gemma 4) on the node, or plan with Claude or an OpenRouter model that accepts images")

// nodeVision reports whether the model loaded on a node can read images.
func (c *Controller) nodeVision(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	hb := c.lastHB[id]
	if hb == nil || hb.LoadedModel == "" {
		return false
	}
	m := c.models[hb.LoadedModel]
	return m != nil && m.hasVision()
}

// checkPlannerVision says why a planner cannot take images, or nil if it can.
func (c *Controller) checkPlannerVision(planner, orModel string) error {
	switch {
	case planner == "claude":
		return nil // every Claude model in the price table reads images
	case planner == "openrouter":
		_ = openRouterCatalog().Refresh(context.Background(), c.openRouterKey(), false)
		if m := openRouterCatalog().Get(orModel); m != nil && !m.Vision {
			return fmt.Errorf("%s does not accept images. Pick a vision model in Settings, or plan with Claude", m.Name)
		}
		return nil
	case strings.HasPrefix(planner, "node:"):
		if !c.nodeVision(strings.TrimPrefix(planner, "node:")) {
			return errNoVisionLocal
		}
	}
	return nil
}

// handleTaskImage serves an attached image to the web UI.
func (c *Controller) handleTaskImage(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	raw, err := os.ReadFile(filepath.Join(t.imagesDir(), filepath.Base(r.URL.Query().Get("name"))))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "no such image"})
		return
	}
	media := http.DetectContentType(raw)
	if _, ok := imageExt[media]; !ok {
		writeJSON(w, 404, map[string]string{"error": "no such image"})
		return
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(raw)
}

// readJSONBody decodes a request that may carry images (larger limit).
func readJSONBody(w http.ResponseWriter, r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, maxImageBodyBytes)).Decode(v)
}
