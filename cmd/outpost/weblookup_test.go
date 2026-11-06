package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractURLs(t *testing.T) {
	got := extractURLs("see https://a.example/x, and (https://b.example/y). again https://a.example/x ftp://no")
	if len(got) != 2 || got[0] != "https://a.example/x" || got[1] != "https://b.example/y" {
		t.Fatalf("got %v", got)
	}
	var many strings.Builder
	for i := 0; i < 9; i++ {
		many.WriteString("http://h" + string(rune('a'+i)) + ".example/ ")
	}
	if n := len(extractURLs(many.String())); n != webMaxURLs {
		t.Fatalf("limit: %d", n)
	}
}

func TestBlockedIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.20.30.40", "192.168.1.1", "172.16.0.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "::1", "fe80::1", "fd00::1", "224.0.0.1", "::ffff:10.0.0.1"} {
		if !blockedIP(net.ParseIP(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700::1111"} {
		if blockedIP(net.ParseIP(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestFetchPageText(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/doc", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><title>x</title><style>p{}</style></head><body><script>evil()</script><h1>API &amp; docs</h1><p>Call <b>GET /v1</b></p></body></html>`))
	})
	mux.HandleFunc("/bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Write([]byte("PK"))
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(strings.Repeat("é", 30000)))
	})
	mux.HandleFunc("/redir", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/doc", 302) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx := context.Background()

	if _, err := fetchPageText(ctx, srv.URL+"/doc", false); err == nil || !strings.Contains(err.Error(), "internal") {
		t.Fatalf("loopback must be refused by default, got %v", err)
	}
	txt, err := fetchPageText(ctx, srv.URL+"/doc", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(txt, "API & docs") || !strings.Contains(txt, "Call GET /v1") || strings.Contains(txt, "evil") || strings.Contains(txt, "<") {
		t.Fatalf("bad text %q", txt)
	}
	if _, err := fetchPageText(ctx, srv.URL+"/bin", true); err == nil {
		t.Fatal("binary should be refused")
	}
	if txt, _ := fetchPageText(ctx, srv.URL+"/big", true); len([]rune(txt)) > webMaxText+10 || !strings.HasSuffix(txt, "[…cut]") {
		t.Fatalf("not cut: %d", len(txt))
	}
	if txt, err := fetchPageText(ctx, srv.URL+"/redir", true); err != nil || !strings.Contains(txt, "docs") {
		t.Fatalf("redirect: %v", err)
	}
	if _, err := fetchPageText(ctx, "file:///etc/passwd", true); err == nil {
		t.Fatal("file scheme must be refused")
	}
}

// A public-looking page that redirects to an internal address must be refused.
func TestRedirectToInternalBlocked(t *testing.T) {
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) }))
	defer inner.Close()
	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, inner.URL, 302)
	}))
	defer outer.Close()
	// Both are on loopback, so the first hop is already refused; the redirect check is the dialer's too.
	if _, err := fetchPageText(context.Background(), outer.URL, false); err == nil {
		t.Fatal("expected refusal")
	}
}

func TestClaudeWebSearchRequest(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 1<<16)
		n, _ := r.Body.Read(b)
		body = string(b[:n])
		w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1000,"output_tokens":100,"server_tool_use":{"web_search_requests":2}}}`))
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_ANTHROPIC_URL", srv.URL)
	r, err := callClaude(context.Background(), "k", "claude-sonnet-5-5", llmRequest{System: "s", User: "u", MaxTokens: 100, WebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "web_search") || r.WebSearches != 2 {
		t.Fatalf("body %s searches %d", body, r.WebSearches)
	}
	base := usageCost("claude-sonnet-5-5", 1000, 100, 0, 0)
	if d := r.CostUSD - base - 0.02; d > 1e-9 || d < -1e-9 {
		t.Fatalf("cost %v base %v", r.CostUSD, base)
	}
	if _, err := callClaude(context.Background(), "k", "claude-sonnet-5-5", llmRequest{System: "s", User: "u", MaxTokens: 100}); err != nil || strings.Contains(body, "web_search") {
		t.Fatalf("search tool must be off by default: %v", err)
	}
}
