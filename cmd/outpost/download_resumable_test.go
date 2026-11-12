package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sumHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// A connection that goes silent part way must be dropped and the download
// resumed from where it stopped, instead of hanging for ever.
func TestDownloadResumableRecoversFromStall(t *testing.T) {
	old := downloadStallTimeout
	downloadStallTimeout = 300 * time.Millisecond
	defer func() { downloadStallTimeout = old }()

	data := bytes.Repeat([]byte("outpost "), 4096)
	half := len(data) / 2
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Write(data[:half])
			w.(http.Flusher).Flush()
			select { // go silent with the connection open
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		want := fmt.Sprintf("bytes=%d-", half)
		if got := r.Header.Get("Range"); got != want {
			t.Errorf("Range = %q, want %q", got, want)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)-half))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(data[half:])
	}))
	defer srv.Close()
	defer close(release)

	dst := filepath.Join(t.TempDir(), "model.gguf")
	type result struct {
		sum string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		sum, err := downloadResumable(context.Background(), srv.Client(), srv.URL, "", dst, int64(len(data)), func(int64) {})
		ch <- result{sum, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.sum != sumHex(data) {
			t.Fatal("wrong checksum after resuming")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("download still hanging on the silent connection")
	}
	if b, _ := os.ReadFile(dst); !bytes.Equal(b, data) {
		t.Fatal("file contents differ after resuming")
	}
	if calls.Load() != 2 {
		t.Fatalf("requests = %d, want 2", calls.Load())
	}
}

// A refusal (gated or missing file) is reported at once, not after a minute of retries.
func TestDownloadResumableDoesNotRetryRefusal(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "gated", http.StatusForbidden)
	}))
	defer srv.Close()
	start := time.Now()
	_, err := downloadResumable(context.Background(), srv.Client(), srv.URL, "", filepath.Join(t.TempDir(), "f"), 10, func(int64) {})
	if err == nil || !strings.Contains(err.Error(), "Hugging Face: 403") {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 1 || time.Since(start) > time.Second {
		t.Fatalf("retried a refusal: %d requests in %s", calls.Load(), time.Since(start))
	}
}

// A complete .part left by a crash before the rename is finished without a request.
func TestDownloadResumableFinishesCompletePart(t *testing.T) {
	data := []byte("all of it")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request expected")
		http.Error(w, "range", http.StatusRequestedRangeNotSatisfiable)
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(dst+".part", data, 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := downloadResumable(context.Background(), srv.Client(), srv.URL, "", dst, int64(len(data)), func(int64) {})
	if err != nil || sum != sumHex(data) {
		t.Fatalf("sum %s err %v", sum, err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatal("not renamed into place")
	}
}

// A node download from a controller that goes silent must give up and keep the
// partial file, instead of holding the model, runtime or update step for ever.
func TestNodeFetchFileGivesUpOnSilentController(t *testing.T) {
	old := downloadStallTimeout
	downloadStallTimeout = 300 * time.Millisecond
	defer func() { downloadStallTimeout = old }()

	data := bytes.Repeat([]byte("outpost "), 4096)
	half := len(data) / 2
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Write(data[:half])
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	n := testNode(t)
	n.cfg.Controller, n.cfg.Fingerprint = srv.URL, certFingerprint(srv.Certificate().Raw)
	dst := filepath.Join(t.TempDir(), "file.bin")
	errc := make(chan error, 1)
	go func() { errc <- n.fetchFile("/v1/x", dst, sumHex(data), int64(len(data)), nil) }()
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "sent nothing") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("fetch still hanging on the silent controller")
	}
	if b, _ := os.ReadFile(dst + ".part"); !bytes.Equal(b, data[:half]) {
		t.Fatalf("partial file not kept for resuming (%d bytes)", len(b))
	}
}
