package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const ddgPage = `<html><body>
<div class="result results_links result--ad"><a rel="nofollow" class="result__a" href="//duckduckgo.com/y.js?ad_domain=x.com&amp;u=1">Ad</a></div>
<div class="result"><h2><a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2Fdevel%2Frelease&amp;rut=abc">Go <b>Release</b> History</a></h2>
<a class="result__snippet" href="//duckduckgo.com/l/?uddg=x">Release notes &amp; <b>dates</b> for Go.</a></div>
<div class="result"><a class="result__a" href="https://example.org/two">Two</a></div>
</body></html>`

func useSearchStubs(t *testing.T, h http.Handler) {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	od, ob, op := ddgURL, braveURL, searchAllowPrivate
	ddgURL, braveURL, searchAllowPrivate = srv.URL+"/ddg", srv.URL+"/brave", true
	searchState.Lock()
	searchState.blockedUntil, searchState.last = time.Time{}, time.Time{}
	searchState.Unlock()
	t.Cleanup(func() { ddgURL, braveURL, searchAllowPrivate = od, ob, op })
}

func TestParseDDG(t *testing.T) {
	res := parseDDG(ddgPage)
	if len(res) != 2 {
		t.Fatalf("got %d results: %+v", len(res), res)
	}
	if res[0].URL != "https://go.dev/doc/devel/release" || res[0].Title != "Go Release History" || res[0].Snippet != "Release notes & dates for Go." {
		t.Errorf("first: %+v", res[0])
	}
	if res[1].URL != "https://example.org/two" || res[1].Snippet != "" {
		t.Errorf("second: %+v", res[1])
	}
}

func TestSearchDuckDuckGo(t *testing.T) {
	useSearchStubs(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.URL.Path != "/ddg" || r.PostForm.Get("q") != "go release" {
			t.Errorf("bad request %s %v", r.URL.Path, r.PostForm)
		}
		fmt.Fprint(w, ddgPage)
	}))
	c := &Controller{}
	res, engine, err := c.webSearch(context.Background(), "go release")
	if err != nil || engine != "DuckDuckGo" || len(res) != 2 {
		t.Fatalf("%v %s %v", err, engine, res)
	}
}

func TestSearchFailsOverToBrave(t *testing.T) {
	var mu sync.Mutex
	ddgHits := 0
	useSearchStubs(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ddg" {
			mu.Lock()
			ddgHits++
			mu.Unlock()
			w.WriteHeader(202)
			fmt.Fprint(w, `<div class="anomaly-modal">Unfortunately, bots use DuckDuckGo too.</div>`)
			return
		}
		if r.Header.Get("X-Subscription-Token") != "BK" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"web":{"results":[{"title":"A <strong>b</strong>","url":"https://a.example/","description":"d &amp; e"},{"title":"x","url":"javascript:1"}]}}`)
	}))
	c := &Controller{dataDir: t.TempDir()}
	// No key: the failure is explained.
	if _, _, err := c.webSearch(context.Background(), "q"); err == nil || !strings.Contains(err.Error(), "no Brave Search key") {
		t.Fatalf("want a no-key error, got %v", err)
	}
	enc, err := c.encrypt("BK")
	if err != nil {
		t.Fatal(err)
	}
	c.settings.BraveKeyEnc = enc
	res, engine, err := c.webSearch(context.Background(), "q")
	if err != nil || engine != "Brave" || len(res) != 1 || res[0].Title != "A b" || res[0].Snippet != "d & e" {
		t.Fatalf("%v %s %+v", err, engine, res)
	}
	// DuckDuckGo is skipped for a while after it blocked us.
	before := ddgHits
	if _, engine, _ := c.webSearch(context.Background(), "q2"); engine != "Brave" || ddgHits != before {
		t.Fatalf("engine %s, ddg hits %d -> %d", engine, before, ddgHits)
	}
}

func TestSearchBadBraveKeyReportsBoth(t *testing.T) {
	useSearchStubs(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ddg" {
			fmt.Fprint(w, "<html>something else entirely</html>")
			return
		}
		w.WriteHeader(401)
	}))
	c := &Controller{dataDir: t.TempDir()}
	c.settings.BraveKeyEnc, _ = c.encrypt("BAD")
	_, _, err := c.webSearch(context.Background(), "q")
	if err == nil || !strings.Contains(err.Error(), "DuckDuckGo") || !strings.Contains(err.Error(), "key was refused") {
		t.Fatalf("got %v", err)
	}
}

func TestSmallModel(t *testing.T) {
	for name, want := range map[string]bool{
		"qwen2.5-3b-instruct-q4_k_m.gguf": true, "Llama-3.2-1B-Instruct.gguf": true, "qwen2.5-0.5b.gguf": true,
		"qwen2.5-7b-instruct.gguf": false, "gemma-3-4b-it.gguf": false, "qwen3-30b-a3b.gguf": false, "phi-3-mini-13b.gguf": false, "m": false,
	} {
		if smallModel(name) != want {
			t.Errorf("%s: want %v", name, want)
		}
	}
}

// A slow DuckDuckGo request must not hold up a second search that is stopped:
// the lock is not held during the request.
func TestSearchStopDoesNotWaitForOtherSearch(t *testing.T) {
	release := make(chan struct{})
	useSearchStubs(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		fmt.Fprint(w, ddgPage)
	}))
	c := &Controller{}
	slow := make(chan struct{})
	go func() { c.webSearch(context.Background(), "slow one"); close(slow) }()
	defer func() { close(release); <-slow }() // finish before the stubs are put back
	time.Sleep(100 * time.Millisecond)        // the first request is in flight
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	if _, _, err := c.webSearch(ctx, "stopped"); err == nil {
		t.Fatal("a stopped search should fail")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the stopped search waited %s behind the other one", d)
	}
}
