package main

// Disk use of chats (Settings → Storage): how much each chat takes for its
// text, images, documents and files its code wrote, and a way to clear the
// run files of one chat or of all chats not used for a while without
// deleting the conversation.

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// chatSizes is the disk use of the chats folder split by kind, found from the file names.
func chatSizes(dir string) (text, images, docs, runs int64) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		n := e.Name()
		switch {
		case strings.HasSuffix(n, "-images") && e.IsDir():
			images += dirSize(filepath.Join(dir, n))
		case strings.HasSuffix(n, "-docs") && e.IsDir():
			docs += dirSize(filepath.Join(dir, n))
		case strings.HasSuffix(n, "-runs") && e.IsDir():
			runs += dirSize(filepath.Join(dir, n))
		default:
			if fi, err := e.Info(); err == nil && !e.IsDir() {
				text += fi.Size()
			} else if e.IsDir() {
				text += dirSize(filepath.Join(dir, n))
			}
		}
	}
	return
}

type chatDisk struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Updated time.Time `json:"updated"`
	Text    int64     `json:"text"`
	Images  int64     `json:"images"`
	Docs    int64     `json:"docs"`
	Runs    int64     `json:"runs"`
	Total   int64     `json:"total"`
}

// chatDiskUse lists the chats that use disk, largest first (at most limit).
func (c *Controller) chatDiskUse(limit int) []chatDisk {
	c.chatMu.Lock()
	chats := make([]*Chat, 0, len(c.chats))
	for _, ch := range c.chats {
		chats = append(chats, ch)
	}
	c.chatMu.Unlock()
	dir := c.chatsDir()
	var out []chatDisk
	for _, ch := range chats {
		ch.mu.Lock()
		d := chatDisk{ID: ch.ID, Title: ch.Title, Updated: ch.Updated}
		ch.mu.Unlock()
		if fi, err := os.Stat(filepath.Join(dir, d.ID+".json")); err == nil {
			d.Text = fi.Size()
		}
		d.Images = dirSize(filepath.Join(dir, d.ID+"-images"))
		d.Docs = dirSize(filepath.Join(dir, d.ID+"-docs"))
		d.Runs = dirSize(filepath.Join(dir, d.ID+"-runs"))
		d.Total = d.Text + d.Images + d.Docs + d.Runs
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// GET /api/storage/chats
func (c *Controller) handleStorageChats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"chats": c.chatDiskUse(100)})
}

// clearChatRuns removes the files a chat's code wrote and the links to them. The
// answers, the code and its output text stay. It returns the bytes freed, and
// does nothing to a chat that is answering.
func (c *Controller) clearChatRuns(ch *Chat) (int64, bool) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if ch.Busy != "" {
		return 0, false
	}
	freed := dirSize(ch.runsDir())
	os.RemoveAll(ch.runsDir())
	var strip func(ms []ChatMsg)
	strip = func(ms []ChatMsg) {
		for i := range ms {
			for j := range ms[i].Runs {
				ms[i].Runs[j].Files = nil
			}
			for _, a := range ms[i].Alts {
				strip(a.Msgs)
			}
		}
	}
	strip(ch.Messages)
	ch.saveLocked()
	return freed, true
}

// POST /api/storage/chats/clear-runs {"id":"chat_…"} for one chat, or {"older_than_days":30} for all chats not used since.
func (c *Controller) handleStorageClearRuns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Days int    `json:"older_than_days"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || (req.ID == "" && req.Days <= 0) {
		writeJSON(w, 400, map[string]string{"error": "give a chat id or older_than_days"})
		return
	}
	var targets []*Chat
	if req.ID != "" {
		ch := c.getChat(req.ID)
		if ch == nil {
			writeJSON(w, 404, map[string]string{"error": "no such chat"})
			return
		}
		targets = []*Chat{ch}
	} else {
		cut := time.Now().Add(-time.Duration(req.Days) * 24 * time.Hour)
		c.chatMu.Lock()
		for _, ch := range c.chats {
			ch.mu.Lock()
			if ch.Updated.Before(cut) {
				targets = append(targets, ch)
			}
			ch.mu.Unlock()
		}
		c.chatMu.Unlock()
	}
	var freed int64
	n, skipped := 0, 0
	for _, ch := range targets {
		f, ok := c.clearChatRuns(ch)
		switch {
		case !ok:
			skipped++
		case f > 0:
			freed += f
			n++
		}
	}
	c.storage.mu.Lock()
	c.storage.parts = nil // measure again
	c.storage.mu.Unlock()
	writeJSON(w, 200, map[string]any{"freed": freed, "chats": n, "busy": skipped})
}
