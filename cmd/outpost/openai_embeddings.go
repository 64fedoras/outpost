package main

// POST /v1/embeddings, the OpenAI embeddings API on top of Embed (embed.go).

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// readyEmbeddingModels lists the library's ready embedding models.
func (c *Controller) readyEmbeddingModels() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, m := range c.models {
		if m.Status == "ready" && isEmbeddingName(m.Name) {
			out = append(out, m.Name)
		}
	}
	sort.Strings(out)
	return out
}

// embedInput reads the "input" field: a string or a list of strings.
func embedInput(raw json.RawMessage) ([]string, string) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, "input is required"
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		if strings.TrimSpace(one) == "" {
			return nil, "input must not be empty"
		}
		return []string{one}, ""
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		if len(many) == 0 {
			return nil, "input must not be empty"
		}
		for i, s := range many {
			if strings.TrimSpace(s) == "" {
				return nil, "input[" + strconv.Itoa(i) + "] is empty"
			}
		}
		return many, ""
	}
	return nil, "input must be a string or a list of strings (token arrays are not supported)"
}

func (c *Controller) handleV1Embeddings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model          string          `json:"model"`
		Input          json.RawMessage `json:"input"`
		EncodingFormat string          `json:"encoding_format"`
		Dimensions     int             `json:"dimensions"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil {
		oaiError(w, 400, "invalid_request_error", "bad JSON body")
		return
	}
	if req.Dimensions > 0 {
		oaiError(w, 400, "invalid_request_error", "dimensions is not supported; the model's own size is used")
		return
	}
	if req.EncodingFormat != "" && req.EncodingFormat != "float" && req.EncodingFormat != "base64" {
		oaiError(w, 400, "invalid_request_error", "encoding_format must be float or base64")
		return
	}
	texts, bad := embedInput(req.Input)
	if bad != "" {
		oaiError(w, 400, "invalid_request_error", bad)
		return
	}
	res, err := c.Embed(r.Context(), req.Model, "", texts)
	if err != nil {
		code, typ := 503, "server_error"
		msg := err.Error()
		switch {
		case strings.Contains(msg, "not an embedding model"), strings.Contains(msg, "no embedding model"):
			code, typ = 404, "invalid_request_error"
		case strings.Contains(msg, "too long"), strings.Contains(msg, "at most"), strings.Contains(msg, "refused the text"):
			code, typ = 400, "invalid_request_error"
		}
		oaiError(w, code, typ, msg)
		return
	}
	data := make([]map[string]any, len(res.Vectors))
	for i, v := range res.Vectors {
		var e any = v
		if req.EncodingFormat == "base64" {
			e = base64.StdEncoding.EncodeToString(float32sLE(v))
		}
		data[i] = map[string]any{"object": "embedding", "index": i, "embedding": e}
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data, "model": res.Model,
		"usage": map[string]int{"prompt_tokens": res.Tokens, "total_tokens": res.Tokens}})
}

// float32sLE is the vector as little-endian float32 bytes, as the OpenAI base64 format wants.
func float32sLE(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}
