package main

// OpenAI-style image generation: POST /v1/images/generations and the picture
// files it links to. Off by default (Settings, Pictures, "Allow the image API");
// tokens need full access or the "chat and pictures" scope. The request waits
// until the pictures are done, like OpenAI's, and is cancelled if the client
// goes away.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var apiGenWait = 15 * time.Minute

func (c *Controller) openAIImageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/images/generations", c.authImages(c.handleV1Images))
	mux.HandleFunc("GET /v1/images/files/{id}/{index}", c.authImages(c.handleV1ImageFile))
}

// authImages accepts a browser session, or a token with full or "images" scope
// while the image API is switched on. It passes the token's name on in a header-less way
// through the request context-free helper tokenNameFor.
func (c *Controller) authImages(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session := false
		c.mu.Lock()
		if ck, err := r.Cookie("outpost_session"); err == nil {
			if exp, found := c.sessions[ck.Value]; found && time.Now().Before(exp) {
				session = true
			}
		}
		tok := -1
		scope := ""
		if !session {
			if tok = c.bearerAPIToken(r); tok >= 0 {
				scope = c.apiTokens[tok].Scope
				if time.Since(c.apiTokens[tok].LastUsed) > time.Minute {
					c.apiTokens[tok].LastUsed = time.Now()
					c.saveAPITokensLocked()
				}
			}
		}
		c.mu.Unlock()
		if !session && tok < 0 {
			oaiError(w, 401, "invalid_request_error", "missing or unknown API token (create one under Settings → API tokens)")
			return
		}
		if !session {
			if scope != "" && scope != "images" {
				oaiError(w, 403, "invalid_request_error", "this token is chat only; make one with picture access under Settings → API tokens")
				return
			}
			if !c.imageAPIOn() {
				oaiError(w, 403, "invalid_request_error", "the image API is off; turn it on under Settings → Pictures")
				return
			}
		}
		h(w, r)
	}
}

func (c *Controller) imageAPIOn() bool {
	if c.gens == nil {
		return false
	}
	c.gens.mu.Lock()
	defer c.gens.mu.Unlock()
	return c.gens.settings.APIEnabled
}

// tokenSource names who asked, for the Source line on the picture.
func (c *Controller) tokenSource(r *http.Request) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i := c.bearerAPIToken(r); i >= 0 {
		return "api:" + c.apiTokens[i].Name
	}
	return "web"
}

type v1ImageReq struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n"`
	Size           string `json:"size"`
	ResponseFormat string `json:"response_format"`
	Seed           *int64 `json:"seed"`
	Steps          int    `json:"steps"`
	Negative       string `json:"negative_prompt"`
}

func (c *Controller) handleV1Images(w http.ResponseWriter, r *http.Request) {
	var req v1ImageReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		oaiError(w, 400, "invalid_request_error", "bad JSON body")
		return
	}
	if req.ResponseFormat != "" && req.ResponseFormat != "b64_json" && req.ResponseFormat != "url" {
		oaiError(w, 400, "invalid_request_error", "response_format must be url or b64_json")
		return
	}
	if req.Model == "" {
		req.Model = imageCatalog[0].ID // the catalog starts with a picture model
	}
	def := imageModel(req.Model)
	if def != nil && def.Kind != genImage {
		oaiError(w, 400, "invalid_request_error", def.Label+" makes video; the API only makes pictures")
		return
	}
	if def == nil {
		oaiError(w, 404, "invalid_request_error", "unknown image model "+req.Model)
		return
	}
	gr := GenRequest{Model: def.ID, Prompt: req.Prompt, Negative: req.Negative, Steps: req.Steps, Count: req.N, Seed: req.Seed, Width: def.Size, Height: def.Size}
	if gr.Count == 0 {
		gr.Count = 1
	}
	if req.Size != "" {
		ws, hs, ok := strings.Cut(req.Size, "x")
		wv, e1 := strconv.Atoi(ws)
		hv, e2 := strconv.Atoi(hs)
		if !ok || e1 != nil || e2 != nil {
			oaiError(w, 400, "invalid_request_error", "size must look like 1024x1024")
			return
		}
		gr.Width, gr.Height = wv, hv
	}
	rec, err := c.newGen(gr, c.tokenSource(r))
	if err != nil {
		oaiError(w, 400, "invalid_request_error", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiGenWait)
	defer cancel()
	for {
		c.gens.mu.Lock()
		fin := rec.finished()
		c.gens.mu.Unlock()
		if fin {
			break
		}
		select {
		case <-ctx.Done():
			c.cancelGen(rec.ID)
			if r.Context().Err() == nil {
				oaiError(w, 504, "timeout", "the pictures took too long and were cancelled")
			}
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
	type pic struct {
		B64 string `json:"b64_json,omitempty"`
		URL string `json:"url,omitempty"`
	}
	var data []pic
	var firstErr string
	c.gens.mu.Lock()
	items := append([]*GenItem(nil), rec.Items...)
	statuses := make([]string, len(items))
	errs := make([]string, len(items))
	for i, it := range items {
		statuses[i], errs[i] = it.Status, it.Error
	}
	c.gens.mu.Unlock()
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	for i := range items {
		if statuses[i] != "done" {
			if firstErr == "" {
				firstErr = errs[i]
			}
			continue
		}
		if req.ResponseFormat == "url" {
			data = append(data, pic{URL: fmt.Sprintf("%s://%s/v1/images/files/%s/%d", scheme, r.Host, rec.ID, i)})
			continue
		}
		b, err := os.ReadFile(genFile(filepath.Join(c.genDir(), rec.ID), i))
		if err != nil {
			continue
		}
		data = append(data, pic{B64: base64.StdEncoding.EncodeToString(b)})
	}
	if len(data) == 0 {
		if firstErr == "" {
			firstErr = "no picture was made"
		}
		oaiError(w, 502, "server_error", firstErr)
		return
	}
	writeJSON(w, 200, map[string]any{"created": time.Now().Unix(), "data": data})
}

func (c *Controller) handleV1ImageFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	idx, err := strconv.Atoi(r.PathValue("index"))
	c.gens.mu.Lock()
	rec := c.gens.recs[id]
	ok := rec != nil && err == nil && idx >= 0 && idx < len(rec.Items) && rec.Items[idx].Status == "done"
	c.gens.mu.Unlock()
	if !ok {
		oaiError(w, 404, "invalid_request_error", "no such picture")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	http.ServeFile(w, r, genFile(filepath.Join(c.genDir(), id), idx))
}
