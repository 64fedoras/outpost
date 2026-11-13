package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Built-in web search for local models. The controller runs the search itself, inside the binary:
// DuckDuckGo's plain-HTML results page first (no key; unofficial, so it can be rate limited or
// answer with a CAPTCHA), then the Brave Search API when a key is saved in Settings. Nothing here
// costs money through Outpost, so nothing is written to the spending ledger.
const (
	searchMaxResults = 6
	searchMaxBody    = 1 << 20
	searchTimeout    = 12 * time.Second
	ddgGap           = 1500 * time.Millisecond // least time between two DuckDuckGo requests
	ddgBlockedFor    = 10 * time.Minute        // after a CAPTCHA or rate limit, go straight to Brave for this long
)

var (
	ddgURL   = "https://html.duckduckgo.com/html/"
	braveURL = "https://api.search.brave.com/res/v1/web/search"
	// searchAllowPrivate is only set by tests, which talk to a local stub.
	searchAllowPrivate = false
)

// errSearchBlocked means the engine refused (CAPTCHA, rate limit, an unreadable page).
var errSearchBlocked = errors.New("blocked or rate limited")

type searchResult struct {
	Title   string
	URL     string
	Snippet string
}

var searchState struct {
	sync.Mutex
	last         time.Time // last DuckDuckGo request
	blockedUntil time.Time
}

func (c *Controller) braveKey() string {
	c.mu.Lock()
	enc := c.settings.BraveKeyEnc
	c.mu.Unlock()
	k, _ := c.decrypt(enc)
	return k
}

// webSearch runs one query: DuckDuckGo, then Brave if DuckDuckGo failed and a Brave key is saved.
// It returns the results and the engine that answered.
func (c *Controller) webSearch(ctx context.Context, query string) ([]searchResult, string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, "", errors.New("empty search query")
	}
	brave := c.braveKey()
	var ddgErr error
	searchState.Lock()
	skip := time.Now().Before(searchState.blockedUntil) && brave != ""
	searchState.Unlock()
	if !skip {
		res, err := ddgSearch(ctx, query)
		if err == nil {
			return res, "DuckDuckGo", nil
		}
		ddgErr = err
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
	}
	if brave == "" {
		return nil, "", fmt.Errorf("DuckDuckGo search failed (%v) and no Brave Search key is saved (Settings, API keys)", ddgErr)
	}
	res, err := braveSearch(ctx, brave, query)
	if err != nil {
		if ddgErr != nil {
			return nil, "", fmt.Errorf("DuckDuckGo: %v; Brave: %v", ddgErr, err)
		}
		return nil, "", fmt.Errorf("Brave: %v", err)
	}
	return res, "Brave", nil
}

func searchClient() *http.Client { return webClient(searchAllowPrivate) }

var (
	reDDGLink    = regexp.MustCompile(`(?is)<a\b[^>]*\bclass="[^"]*\bresult__a\b[^"]*"[^>]*>(.*?)</a>`)
	reDDGSnippet = regexp.MustCompile(`(?is)<a\b[^>]*\bclass="[^"]*\bresult__snippet\b[^"]*"[^>]*>(.*?)</a>`)
	reHref       = regexp.MustCompile(`(?is)\bhref="([^"]*)"`)
)

// ddgSearch scrapes DuckDuckGo's HTML results page.
func ddgSearch(ctx context.Context, query string) ([]searchResult, error) {
	// One request at a time and a small gap between them, so a burst does not trip the rate limit.
	searchState.Lock()
	defer searchState.Unlock()
	if wait := ddgGap - time.Since(searchState.last); wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	searchState.last = time.Now()
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	form := url.Values{"q": {query}}
	req, _ := http.NewRequestWithContext(ctx, "POST", ddgURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0")
	req.Header.Set("Accept", "text/html")
	resp, err := searchClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, searchMaxBody))
	page := string(body)
	if resp.StatusCode == 202 || resp.StatusCode == 403 || resp.StatusCode == 429 || ddgChallenge(page) {
		searchState.blockedUntil = time.Now().Add(ddgBlockedFor)
		return nil, errSearchBlocked
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	res := parseDDG(page)
	if len(res) == 0 && !strings.Contains(page, "no-results") && !strings.Contains(page, "No results") {
		return nil, errors.New("the results page could not be read (its layout may have changed)")
	}
	return res, nil
}

func ddgChallenge(page string) bool {
	return strings.Contains(page, "anomaly-modal") || strings.Contains(page, "bots use DuckDuckGo too") ||
		strings.Contains(page, "g-recaptcha") || strings.Contains(page, "challenge-form")
}

// parseDDG extracts organic results (not ads) from the HTML results page.
func parseDDG(page string) []searchResult {
	links := reDDGLink.FindAllStringSubmatchIndex(page, -1)
	snips := reDDGSnippet.FindAllStringSubmatchIndex(page, -1)
	var out []searchResult
	for i, m := range links {
		tag := page[m[0]:m[1]]
		hm := reHref.FindStringSubmatch(tag)
		if hm == nil {
			continue
		}
		target := ddgTarget(html.UnescapeString(hm[1]))
		if target == "" {
			continue
		}
		end := len(page)
		if i+1 < len(links) {
			end = links[i+1][0]
		}
		snippet := ""
		for _, s := range snips {
			if s[0] >= m[1] && s[0] < end {
				snippet = cleanSnippet(page[s[2]:s[3]])
				break
			}
		}
		out = append(out, searchResult{Title: cleanSnippet(page[m[2]:m[3]]), URL: target, Snippet: snippet})
		if len(out) == searchMaxResults {
			break
		}
	}
	return out
}

// ddgTarget unwraps DuckDuckGo's redirect link (//duckduckgo.com/l/?uddg=<url>). Ads and links that
// are not plain http(s) pages come back empty.
func ddgTarget(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if strings.HasSuffix(u.Hostname(), "duckduckgo.com") {
		if u.Path == "/y.js" {
			return "" // advertisement
		}
		t := u.Query().Get("uddg")
		if t == "" {
			return ""
		}
		if u, err = url.Parse(t); err != nil {
			return ""
		}
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || strings.HasSuffix(u.Hostname(), "duckduckgo.com") {
		return ""
	}
	return u.String()
}

func cleanSnippet(s string) string {
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

// braveSearch calls the Brave Search API (X-Subscription-Token).
func braveSearch(ctx context.Context, key, query string) ([]searchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	q := url.Values{"q": {query}, "count": {fmt.Sprint(searchMaxResults)}}
	req, _ := http.NewRequestWithContext(ctx, "GET", braveURL+"?"+q.Encode(), nil)
	req.Header.Set("X-Subscription-Token", key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Outpost/"+Version)
	resp, err := searchClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, searchMaxBody))
	switch resp.StatusCode {
	case 200:
	case 401, 403, 422:
		return nil, errors.New("the key was refused")
	case 429:
		return nil, errors.New("rate limit or monthly quota reached")
	default:
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var v struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, errors.New("unreadable answer")
	}
	var out []searchResult
	for _, r := range v.Web.Results {
		if !strings.HasPrefix(r.URL, "http") {
			continue
		}
		out = append(out, searchResult{Title: cleanSnippet(r.Title), URL: r.URL, Snippet: cleanSnippet(r.Description)})
		if len(out) == searchMaxResults {
			break
		}
	}
	return out, nil
}

// formatResults is what the model reads.
func formatResults(res []searchResult) string {
	if len(res) == 0 {
		return "No results."
	}
	var b strings.Builder
	for i, r := range res {
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", r.Snippet)
		}
	}
	return b.String()
}
