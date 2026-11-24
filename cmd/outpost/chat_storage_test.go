package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChatStorageAndClearRuns(t *testing.T) {
	c := chatController(t)
	ch := &Chat{ID: "chat_a", Title: "Plot", Created: time.Now(), Updated: time.Now().Add(-48 * time.Hour), dir: c.chatsDir(),
		Messages: []ChatMsg{{Role: "assistant", Text: "done", Runs: []CodeRun{{Lang: "python", Files: []RunFile{{Name: "p.png", File: "1-p.png", Bytes: 5000}}}}}}}
	c.chatMu.Lock()
	c.chats[ch.ID] = ch
	c.chatMu.Unlock()
	ch.mu.Lock()
	ch.saveLocked()
	ch.mu.Unlock()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(ch.runsDir(), 0o700))
	must(os.WriteFile(filepath.Join(ch.runsDir(), "1-p.png"), make([]byte, 5000), 0o600))
	must(os.MkdirAll(ch.imagesDir(), 0o700))
	must(os.WriteFile(filepath.Join(ch.imagesDir(), "a.png"), make([]byte, 700), 0o600))

	_, images, _, runs := chatSizes(c.chatsDir())
	if images != 700 || runs != 5000 {
		t.Fatalf("sizes images=%d runs=%d", images, runs)
	}
	use := c.chatDiskUse(10)
	if len(use) != 1 || use[0].Runs != 5000 || use[0].Images != 700 || use[0].Total < 5700 {
		t.Fatalf("disk use %+v", use)
	}

	// A chat used recently is kept by the age rule; the old one is cleared, its answer and image stay.
	rec := httptest.NewRecorder()
	c.handleStorageClearRuns(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{"older_than_days":1}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"freed":5000`) {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(ch.runsDir()); err == nil {
		t.Fatal("run files should be gone")
	}
	if _, err := os.Stat(filepath.Join(ch.imagesDir(), "a.png")); err != nil {
		t.Fatal("images must stay")
	}
	if ch.Messages[0].Text != "done" || len(ch.Messages[0].Runs) != 1 || len(ch.Messages[0].Runs[0].Files) != 0 {
		t.Fatalf("message after clear: %+v", ch.Messages[0])
	}

	// A busy chat is skipped, and a bad request is refused.
	ch.mu.Lock()
	ch.Busy = "answering"
	ch.mu.Unlock()
	must(os.MkdirAll(ch.runsDir(), 0o700))
	must(os.WriteFile(filepath.Join(ch.runsDir(), "x"), []byte("x"), 0o600))
	rec = httptest.NewRecorder()
	c.handleStorageClearRuns(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{"id":"chat_a"}`)))
	if !strings.Contains(rec.Body.String(), `"busy":1`) {
		t.Fatalf("busy chat should be skipped: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	c.handleStorageClearRuns(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{}`)))
	if rec.Code != 400 {
		t.Fatalf("empty request: %d", rec.Code)
	}
}
