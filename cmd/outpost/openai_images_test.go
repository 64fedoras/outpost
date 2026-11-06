package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestV1Images(t *testing.T) {
	c := genController(t)
	png := genTestPNG(t)
	var specs []GenSpec
	var mu sync.Mutex
	fakeGenNode(t, c, "n", png, &specs, &mu, func(GenSpec) *ErrInfo { return nil })
	go c.genLoop()
	c.mu.Lock()
	for name, scope := range map[string]string{"full": "", "chat": "chat", "pics": "images"} {
		c.apiTokens = append(c.apiTokens, apiToken{ID: name, Name: name, Hash: hashAPIToken("tok-" + name), Created: time.Now(), Scope: scope})
	}
	c.mu.Unlock()
	mux := http.NewServeMux()
	c.openAIImageRoutes(mux)
	do := func(method, path, tok, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	body := `{"model":"flux1-schnell-q8","prompt":"a stone outpost","n":2,"size":"768x768"}`
	if w := do("POST", "/v1/images/generations", "", body); w.Code != 401 {
		t.Fatalf("no token: %d", w.Code)
	}
	if w := do("POST", "/v1/images/generations", "tok-pics", body); w.Code != 403 || !strings.Contains(w.Body.String(), "off") {
		t.Fatalf("switch off: %d %s", w.Code, w.Body)
	}
	c.gens.mu.Lock()
	c.gens.settings.APIEnabled = true
	c.gens.mu.Unlock()
	if w := do("POST", "/v1/images/generations", "tok-chat", body); w.Code != 403 {
		t.Fatalf("chat-only token: %d", w.Code)
	}
	if w := do("POST", "/v1/images/generations", "tok-pics", `{"prompt":"x","size":"big"}`); w.Code != 400 {
		t.Fatalf("bad size: %d", w.Code)
	}
	w := do("POST", "/v1/images/generations", "tok-pics", body)
	if w.Code != 200 {
		t.Fatalf("generate: %d %s", w.Code, w.Body)
	}
	var out struct {
		Data []struct {
			B64 string `json:"b64_json"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Data) != 2 {
		t.Fatalf("data: %s", w.Body)
	}
	if b, _ := base64.StdEncoding.DecodeString(out.Data[0].B64); string(b) != string(png) {
		t.Fatal("picture bytes differ")
	}
	mu.Lock()
	if len(specs) != 2 || specs[0].Width != 768 {
		t.Fatalf("specs %+v", specs)
	}
	mu.Unlock()
	// url format, fetched with a full-access token
	w = do("POST", "/v1/images/generations", "tok-full", `{"prompt":"y","response_format":"url"}`)
	var u struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &u)
	if w.Code != 200 || len(u.Data) != 1 || !strings.Contains(u.Data[0].URL, "/v1/images/files/") {
		t.Fatalf("url: %d %s", w.Code, w.Body)
	}
	path := u.Data[0].URL[strings.Index(u.Data[0].URL, "/v1/"):]
	if f := do("GET", path, "tok-full", ""); f.Code != 200 || f.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("file: %d", f.Code)
	}
	// the source shows who asked
	c.gens.mu.Lock()
	found := false
	for _, g := range c.gens.recs {
		if g.Source == "api:pics" {
			found = true
		}
	}
	c.gens.mu.Unlock()
	if !found {
		t.Fatal("source not recorded")
	}
}
