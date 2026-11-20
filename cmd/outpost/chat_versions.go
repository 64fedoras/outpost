package main

// Chat versions and instructions: edit a message and resend, regenerate an
// answer (on the same or another node), switch between the versions, branch a
// chat into a new one, and per-chat instructions added to the system prompt.
//
// Versions are kept on the first message of each version of the tail: when
// message i is edited or regenerated, the conversation from i on is stored in
// msgs[i].Alts of the new tail, and switching swaps the tails back. Nested
// versions inside a stored tail stay with it.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ChatAlt is another version of a chat from one message on.
type ChatAlt struct {
	Msgs []ChatMsg `json:"msgs"`
}

const (
	chatBaseSystem      = "You are a helpful, concise assistant. Format code in fenced blocks."
	maxChatInstructions = 4000
	maxChatVersions     = 20 // per message; the oldest extra version is dropped beyond this
)

func clipChatInstructions(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxChatInstructions {
		s = string(r[:maxChatInstructions])
	}
	return s
}

// chatSystemPrompt is the system message for a chat with these instructions.
func (c *Controller) chatSystemPrompt(instructions string) string {
	p := chatBaseSystem
	if instructions = strings.TrimSpace(instructions); instructions != "" {
		p += "\n\nFollow these instructions from the user for this chat:\n" + instructions
	}
	return p + c.memoryPrompt("")
}

// msgIndex finds a message by id in the current version of the chat.
func (ch *Chat) msgIndex(id string) int {
	for i, m := range ch.Messages {
		if m.ID == id {
			return i
		}
	}
	return -1
}

// cutTail removes messages i.. from the chat and returns all versions of the
// tail that starts at i (the current one at its Ver position) with their
// Alts cleared, ready to be stored on a new version.
func (ch *Chat) cutTail(i int) []ChatAlt {
	tail := append([]ChatMsg(nil), ch.Messages[i:]...)
	ch.Messages = ch.Messages[:i]
	others, ver := tail[0].Alts, tail[0].Ver
	tail[0].Alts, tail[0].Ver = nil, 0
	ver = min(max(ver, 0), len(others))
	all := make([]ChatAlt, 0, len(others)+1)
	all = append(all, others[:ver]...)
	all = append(all, ChatAlt{Msgs: tail})
	all = append(all, others[ver:]...)
	if len(all) >= maxChatVersions {
		all = all[len(all)-maxChatVersions+1:]
	}
	return all
}

func (c *Controller) chatForChange(w http.ResponseWriter, r *http.Request, req any) *Chat {
	ch := c.getChat(r.PathValue("id"))
	if ch == nil {
		writeJSON(w, 404, map[string]string{"error": "no such chat"})
		return nil
	}
	if err := readJSONBody(w, r, req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return nil
	}
	return ch
}

// handleChatEdit replaces one of the user's messages with new text and answers
// again from there. The old text and everything after it stay as a version.
func (c *Controller) handleChatEdit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MsgID string `json:"msg_id"`
		Text  string `json:"text"`
		Node  string `json:"node"`
	}
	ch := c.chatForChange(w, r, &req)
	if ch == nil {
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" || len(text) > maxChatMsgChars {
		writeJSON(w, 400, map[string]string{"error": "type a message first (at most 32,000 characters)"})
		return
	}
	ch.mu.Lock()
	i := ch.msgIndex(req.MsgID)
	switch {
	case ch.Busy != "":
		ch.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "wait for the answer (or stop it) first"})
		return
	case i < 0 || ch.Messages[i].Role != "user":
		ch.mu.Unlock()
		writeJSON(w, 404, map[string]string{"error": "no such message"})
		return
	}
	imgs, docs := ch.Messages[i].Images, ch.Messages[i].Docs
	all := ch.cutTail(i)
	ch.Messages = append(ch.Messages, ChatMsg{ID: newID("m"), Role: "user", Text: text, Images: imgs, Docs: docs, At: time.Now(), Alts: all, Ver: len(all)})
	if req.Node != "" {
		ch.Node = req.Node
	}
	c.beginAnswerLocked(ch, nil, 0)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleChatRegenerate asks again for an answer (optionally on another node);
// the old answer and what followed it stay as a version.
func (c *Controller) handleChatRegenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MsgID string `json:"msg_id"`
		Node  string `json:"node"`
	}
	ch := c.chatForChange(w, r, &req)
	if ch == nil {
		return
	}
	ch.mu.Lock()
	i := ch.msgIndex(req.MsgID)
	switch {
	case ch.Busy != "":
		ch.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "wait for the answer (or stop it) first"})
		return
	case i < 1 || ch.Messages[i].Role != "assistant":
		ch.mu.Unlock()
		writeJSON(w, 404, map[string]string{"error": "no such answer"})
		return
	}
	all := ch.cutTail(i)
	if req.Node != "" {
		ch.Node = req.Node
	}
	c.beginAnswerLocked(ch, all, len(all))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleChatVersion shows another version of the conversation from a message on.
func (c *Controller) handleChatVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MsgID string `json:"msg_id"`
		Ver   int    `json:"ver"`
	}
	ch := c.chatForChange(w, r, &req)
	if ch == nil {
		return
	}
	ch.mu.Lock()
	defer ch.mu.Unlock()
	i := ch.msgIndex(req.MsgID)
	if ch.Busy != "" {
		writeJSON(w, 409, map[string]string{"error": "wait for the answer (or stop it) first"})
		return
	}
	if i < 0 || req.Ver < 0 || req.Ver > len(ch.Messages[i].Alts) {
		writeJSON(w, 404, map[string]string{"error": "no such version"})
		return
	}
	all := ch.cutTail(i)
	if req.Ver >= len(all) { // cutTail may have dropped the oldest; cannot happen below maxChatVersions
		req.Ver = len(all) - 1
	}
	tail := append([]ChatMsg(nil), all[req.Ver].Msgs...)
	tail[0].Alts = append(append([]ChatAlt(nil), all[:req.Ver]...), all[req.Ver+1:]...)
	tail[0].Ver = req.Ver
	ch.Messages = append(ch.Messages, tail...)
	ch.Updated = time.Now()
	ch.saveLocked()
	taskLive.changed("chat_" + ch.ID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleChatBranch starts a new chat with the conversation up to and including
// one message (current versions only), its images and instructions.
func (c *Controller) handleChatBranch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MsgID string `json:"msg_id"`
	}
	ch := c.chatForChange(w, r, &req)
	if ch == nil {
		return
	}
	ch.mu.Lock()
	i := ch.msgIndex(req.MsgID)
	if i < 0 {
		ch.mu.Unlock()
		writeJSON(w, 404, map[string]string{"error": "no such message"})
		return
	}
	now := time.Now()
	nc := &Chat{ID: newID("chat"), Title: chatTitle("Branch: " + ch.Title), Node: ch.Node, Instructions: ch.Instructions, GroupID: ch.GroupID, Created: now, Updated: now, dir: c.chatsDir()}
	for _, m := range ch.Messages[:i+1] {
		m.Alts, m.Ver = nil, 0
		nc.Messages = append(nc.Messages, m)
		for _, n := range m.Images {
			copyChatImage(filepath.Join(ch.dir, ch.ID+"-images", n), filepath.Join(nc.dir, nc.ID+"-images", n))
		}
		for _, d := range m.Docs {
			copyChatImage(filepath.Join(ch.docsDir(), d.File), filepath.Join(nc.docsDir(), d.File))
		}
	}
	ch.mu.Unlock()
	c.chatMu.Lock()
	if len(c.chats) >= maxChats {
		c.chatMu.Unlock()
		os.RemoveAll(filepath.Join(nc.dir, nc.ID+"-images"))
		writeJSON(w, 400, map[string]string{"error": "too many chats; delete some first"})
		return
	}
	c.chats[nc.ID] = nc
	c.chatMu.Unlock()
	nc.mu.Lock()
	nc.saveLocked()
	nc.mu.Unlock()
	writeJSON(w, 200, map[string]string{"id": nc.ID})
}

func copyChatImage(from, to string) {
	b, err := os.ReadFile(from)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(to), 0o700) == nil {
		os.WriteFile(to, b, 0o600)
	}
}

// handleChatInstructions sets this chat's instructions; "default": true also
// makes them the default for new chats.
func (c *Controller) handleChatInstructions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Instructions string `json:"instructions"`
		Default      bool   `json:"default"`
	}
	ch := c.chatForChange(w, r, &req)
	if ch == nil {
		return
	}
	text := clipChatInstructions(req.Instructions)
	ch.mu.Lock()
	ch.Instructions = text
	ch.saveLocked()
	ch.mu.Unlock()
	if req.Default {
		c.mu.Lock()
		c.settings.ChatInstructions = text
		c.saveSettingsLocked()
		c.mu.Unlock()
	}
	taskLive.changed("chat_" + ch.ID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
