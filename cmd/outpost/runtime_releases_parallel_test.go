package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The upstream and the compatible-builds lists are fetched at the same time, not one after the other.
func TestLatestReleasesFetchesBothListsAtOnce(t *testing.T) {
	const delay = 400 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		if strings.Contains(r.URL.Path, "ggml-org/llama.cpp") {
			fmt.Fprint(w, `[{"tag_name":"b9","assets":[{"name":"llama-b9-bin-ubuntu-x64.tar.gz","size":1,"browser_download_url":"https://example.invalid/u"}]}]`)
			return
		}
		fmt.Fprint(w, `[{"tag_name":"llama-compat-b9","assets":[{"name":"llama-b9-bin-ubuntu22-arm64.tar.gz","size":1,"browser_download_url":"https://example.invalid/c"}]}]`)
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_GITHUB_API", srv.URL)
	c := &Controller{rtCache: &runtimeCache{}}
	start := time.Now()
	rels, err := c.latestReleases(context.Background(), true)
	took := time.Since(start)
	if err != nil || len(rels) != 1 || len(rels[0].Assets) != 2 {
		t.Fatalf("rels %+v err %v", rels, err)
	}
	if took >= 2*delay-50*time.Millisecond {
		t.Fatalf("took %s: the two lookups ran one after the other", took)
	}
}
