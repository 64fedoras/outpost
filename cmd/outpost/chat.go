package main

// Chat mode (idea 14): talk straight to a model on a node, no planning and no
// files. A conversation is kept on the controller (data/chats/<id>.json);
// nodes keep nothing. Replies stream to the browser through the same live hub
// tasks use (key "chat_<id>"). A chat only takes a free node slot, so it never
// interrupts task work; it waits up to chatWait for one.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type ChatMsg struct {
	ID     string    `json:"id"`
	Role   string    `json:"role"` // user | assistant
	Text   string    `json:"text"`
	Images []string  `json:"images,omitempty"` // user: screenshots saved with the chat (chat_images.go)
	Docs   []DocInfo `json:"docs,omitempty"`   // user: documents read into text (chat_docs.go)
	Node   string    `json:"node,omitempty"`   // who answered
	Model  string    `json:"model,omitempty"`  // the model that answered
	TPS    float64   `json:"tok_s,omitempty"`
	Error  string    `json:"error,omitempty"`
	Note   string    `json:"note,omitempty"` // e.g. that the web was searched, or why it was not
	At     time.Time `json:"at"`
	// Other versions of the conversation from this message on (chat_versions.go):
	// an edit adds one on the user message, a regenerate on the answer. Ver is
	// this version's place among all of them, counted from 0.
	Alts []ChatAlt `json:"alts,omitempty"`
	Ver  int       `json:"ver,omitempty"`
	// Suggest is a fact the answering model offered to remember (memory.go),
	// shown under the answer until the owner remembers or dismisses it.
	Suggest string `json:"suggest,omitempty"`
	// Runs are the programs the model ran for this answer (chat_run.go).
	Runs []CodeRun `json:"runs,omitempty"`
	// Research is the record of a research-mode answer: its sub-questions and findings (research.go).
	Research *ResearchRun `json:"research,omitempty"`
}

type Chat struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Node     string    `json:"node"` // "auto" or a node id
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Messages []ChatMsg `json:"messages"`
	GroupID  string    `json:"group_id,omitempty"` // sidebar group (groups.go)
	// Instructions are this chat's standing rules, added to the system prompt.
	Instructions string `json:"instructions,omitempty"`
	Busy         string `json:"busy,omitempty"`      // "" | waiting | answering (not saved meaningfully; reset at load)
	MemTries     int    `json:"mem_tries,omitempty"` // answers that asked for a memory suggestion (memory.go)
	RunCode      bool   `json:"run_code,omitempty"`  // the model may run code on an exec node (chat_run.go)
	RunOn        string `json:"run_on,omitempty"`    // while Busy is "running": the node running it
	// Research is the live state of a research answer while it works (research.go).
	Research *ResearchRun `json:"research,omitempty"`

	dir    string
	cancel context.CancelFunc
	mu     sync.Mutex
}

// Timings, shortened by tests.
var (
	chatWait    = 5 * time.Minute  // for a free node
	chatTimeout = 15 * time.Minute // one answer
)

const (
	maxChatMsgChars = 32000
	maxChats        = 500
)

func (c *Controller) chatsDir() string { return filepath.Join(c.dataDir, "chats") }

func (ch *Chat) saveLocked() {
	b, _ := json.MarshalIndent(ch, "", "  ")
	if err := os.MkdirAll(ch.dir, 0o700); err == nil {
		if err := writeFileAtomic(filepath.Join(ch.dir, ch.ID+".json"), b, 0o600); err != nil {
			log.Printf("chat %s: save failed: %v", ch.ID, err)
		}
	}
}

func (c *Controller) loadChats() {
	c.chatMu.Lock()
	defer c.chatMu.Unlock()
	c.chats = map[string]*Chat{}
	ents, _ := os.ReadDir(c.chatsDir())
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(c.chatsDir(), e.Name()))
		if err != nil {
			continue
		}
		ch := &Chat{}
		if json.Unmarshal(b, ch) != nil || ch.ID+".json" != e.Name() {
			continue
		}
		ch.dir, ch.Busy, ch.RunOn, ch.Research = c.chatsDir(), "", "", nil
		c.chats[ch.ID] = ch
	}
}

func (c *Controller) getChat(id string) *Chat {
	c.chatMu.Lock()
	defer c.chatMu.Unlock()
	return c.chats[id]
}

func (c *Controller) chatRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/chats", c.auth(c.handleListChats))
	mux.HandleFunc("POST /api/chats", c.auth(c.handleNewChat))
	mux.HandleFunc("GET /api/chats/{id}", c.auth(c.handleGetChat))
	mux.HandleFunc("DELETE /api/chats/{id}", c.auth(c.handleDeleteChat))
	mux.HandleFunc("POST /api/chats/{id}/messages", c.auth(c.handleChatSend))
	mux.HandleFunc("POST /api/chats/{id}/stop", c.auth(c.handleChatStop))
	mux.HandleFunc("GET /api/chats/{id}/image", c.auth(c.handleChatImage))
	mux.HandleFunc("GET /api/chats/{id}/doc", c.auth(c.handleChatDoc))
	mux.HandleFunc("POST /api/chats/{id}/runcode", c.auth(c.handleChatRunCode))
	mux.HandleFunc("GET /api/chats/{id}/run-file", c.auth(c.handleChatRunFile))
	mux.HandleFunc("GET /api/chats/{id}/events", c.authSession(c.handleChatEvents))
	mux.HandleFunc("POST /api/chats/{id}/edit", c.auth(c.handleChatEdit))
	mux.HandleFunc("POST /api/chats/{id}/regenerate", c.auth(c.handleChatRegenerate))
	mux.HandleFunc("POST /api/chats/{id}/version", c.auth(c.handleChatVersion))
	mux.HandleFunc("POST /api/chats/{id}/branch", c.auth(c.handleChatBranch))
	mux.HandleFunc("POST /api/chats/{id}/instructions", c.auth(c.handleChatInstructions))
}

func (c *Controller) handleListChats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, c.chatSummaries())
}

// chatSummaries is the chat list for the sidebar, newest first (also pushed
// with the state, state_stream.go).
func (c *Controller) chatSummaries() []map[string]any {
	c.chatMu.Lock()
	out := []map[string]any{}
	for _, ch := range c.chats {
		ch.mu.Lock()
		out = append(out, map[string]any{"id": ch.ID, "title": ch.Title, "updated": ch.Updated, "busy": ch.Busy, "messages": len(ch.Messages), "group_id": ch.GroupID})
		ch.mu.Unlock()
	}
	c.chatMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i]["updated"].(time.Time).After(out[j]["updated"].(time.Time)) })
	return out
}

func (c *Controller) handleGetChat(w http.ResponseWriter, r *http.Request) {
	ch := c.getChat(r.PathValue("id"))
	if ch == nil {
		writeJSON(w, 404, map[string]string{"error": "no such chat"})
		return
	}
	ch.mu.Lock()
	b, _ := json.Marshal(ch)
	ch.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

type chatReq struct {
	Text   string        `json:"text"`
	Node   string        `json:"node"` // "auto" or a node id; empty keeps the chat's choice
	Images []imageUpload `json:"images"`
	Docs   []imageUpload `json:"docs"` // PDF, DOCX, XLSX, ODT, CSV (chat_docs.go)
	// Instructions (new chats only): nil takes the default from Settings.
	Instructions *string `json:"instructions"`
	RunCode      bool    `json:"run_code"` // new chats only: start with Run code on
}

func (req *chatReq) check() error {
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" && len(req.Images) == 0 && len(req.Docs) == 0 {
		return errors.New("type a message first")
	}
	if len(req.Text) > maxChatMsgChars {
		return fmt.Errorf("messages are limited to %d characters", maxChatMsgChars)
	}
	return nil
}

// checked validates the message and its images (type, size, count) and, when
// there are images, that a node that can read them is ready.
func (req *chatReq) checked(c *Controller) ([]llmImage, error) {
	if err := req.check(); err != nil {
		return nil, err
	}
	if len(req.Docs) > 0 && req.Text == "" && len(req.Images) == 0 {
		req.Text = "Read the attached document and summarise it."
	}
	if len(req.Images) == 0 {
		return nil, nil
	}
	imgs, err := decodeImages(req.Images)
	if err != nil {
		return nil, err
	}
	if !c.anyVisionNode(firstNonEmpty(req.Node, "auto")) {
		return nil, errNoVisionChat
	}
	return imgs, nil
}

func (c *Controller) handleNewChat(w http.ResponseWriter, r *http.Request) {
	var req chatReq
	if err := readJSONBody(w, r, &req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	imgs, err := req.checked(c)
	var docs []chatDoc
	if err == nil {
		docs, err = readChatDocs(req.Docs)
	}
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	now := time.Now()
	ch := &Chat{ID: newID("chat"), Title: chatTitle(firstNonEmpty(req.Text, "Image")), Node: firstNonEmpty(req.Node, "auto"), RunCode: req.RunCode, Created: now, Updated: now, dir: c.chatsDir()}
	if req.Instructions != nil {
		ch.Instructions = clipChatInstructions(*req.Instructions)
	} else {
		c.mu.Lock()
		ch.Instructions = c.settings.ChatInstructions
		c.mu.Unlock()
	}
	c.chatMu.Lock()
	if len(c.chats) >= maxChats {
		c.chatMu.Unlock()
		writeJSON(w, 400, map[string]string{"error": "too many chats; delete some first"})
		return
	}
	c.chats[ch.ID] = ch
	c.chatMu.Unlock()
	c.startChatReplyAll(ch, req.Text, imgs, docs)
	writeJSON(w, 200, map[string]string{"id": ch.ID})
}

func (c *Controller) handleChatSend(w http.ResponseWriter, r *http.Request) {
	ch := c.getChat(r.PathValue("id"))
	if ch == nil {
		writeJSON(w, 404, map[string]string{"error": "no such chat"})
		return
	}
	var req chatReq
	if err := readJSONBody(w, r, &req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	ch.mu.Lock()
	if req.Node == "" {
		req.Node = ch.Node
	}
	ch.mu.Unlock()
	imgs, err := req.checked(c)
	var docs []chatDoc
	if err == nil {
		docs, err = readChatDocs(req.Docs)
	}
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	ch.mu.Lock()
	if req.Node != "" && ch.Busy == "" {
		ch.Node = req.Node
	}
	ch.mu.Unlock()
	if !c.startChatReplyAll(ch, req.Text, imgs, docs) {
		writeJSON(w, 409, map[string]string{"error": "wait for the answer (or stop it) first"})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleChatStop(w http.ResponseWriter, r *http.Request) {
	ch := c.getChat(r.PathValue("id"))
	if ch == nil {
		writeJSON(w, 404, map[string]string{"error": "no such chat"})
		return
	}
	ch.mu.Lock()
	if ch.cancel != nil {
		ch.cancel()
	}
	ch.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleDeleteChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c.chatMu.Lock()
	ch := c.chats[id]
	delete(c.chats, id)
	c.chatMu.Unlock()
	if ch == nil {
		writeJSON(w, 404, map[string]string{"error": "no such chat"})
		return
	}
	ch.mu.Lock()
	if ch.cancel != nil {
		ch.cancel()
	}
	ch.mu.Unlock()
	os.Remove(filepath.Join(c.chatsDir(), id+".json"))
	os.RemoveAll(filepath.Join(c.chatsDir(), id+"-images"))
	os.RemoveAll(filepath.Join(c.chatsDir(), id+"-docs"))
	os.RemoveAll(filepath.Join(c.chatsDir(), id+"-runs"))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleChatEvents(w http.ResponseWriter, r *http.Request) {
	if c.getChat(r.PathValue("id")) == nil {
		writeJSON(w, 404, map[string]string{"error": "no such chat"})
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, 500, map[string]string{"error": "streaming not supported"})
		return
	}
	serveLive(w, r, fl, "chat_"+r.PathValue("id"))
}

func chatTitle(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	if len([]rune(t)) > 60 {
		t = string([]rune(t)[:60]) + "…"
	}
	return t
}

// startChatReply adds the user's message and answers it in the background. It
// returns false, and changes nothing, while the chat is still answering: the
// check and the Busy mark happen under one lock, so a double send cannot start
// two answers.
func (c *Controller) startChatReply(ch *Chat, text string) bool {
	return c.startChatReplyImgs(ch, text, nil)
}

func (c *Controller) startChatReplyImgs(ch *Chat, text string, imgs []llmImage) bool {
	return c.startChatReplyAll(ch, text, imgs, nil)
}

func (c *Controller) startChatReplyAll(ch *Chat, text string, imgs []llmImage, docs []chatDoc) bool {
	ch.mu.Lock()
	if ch.Busy != "" {
		ch.mu.Unlock()
		return false
	}
	names, err := ch.saveImages(imgs)
	if err != nil {
		log.Printf("chat %s: saving images failed: %v", ch.ID, err)
	}
	id := newID("m")
	ch.Messages = append(ch.Messages, ChatMsg{ID: id, Role: "user", Text: text, Images: names, Docs: ch.saveDocs(id, docs), At: time.Now()})
	c.beginAnswerLocked(ch, nil, 0)
	return true
}

// beginAnswerLocked answers the conversation as it stands (its last message is
// the user's) in the background. It is called with ch.mu held and Busy empty,
// and unlocks ch.mu. alts and ver, when set, become the new answer's other
// versions (a regenerate).
func (c *Controller) beginAnswerLocked(ch *Chat, alts []ChatAlt, ver int) {
	pref := ch.Node
	timeout := chatWait + chatTimeout
	if pref == chatResearch {
		timeout = researchTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	ch.Updated, ch.Busy, ch.cancel = time.Now(), "waiting", cancel
	hist := chatHistoryDocs(ch.Messages, ch.imageURI, ch.docText)
	hist[0].Content = c.chatSystemPrompt(ch.Instructions)
	askMem := ch.MemTries < maxMemoryTries && c.memorySuggestOn()
	if askMem {
		for _, m := range ch.Messages {
			askMem = askMem && m.Suggest == ""
		}
	}
	if askMem {
		ch.MemTries++
	}
	ch.saveLocked()
	ch.mu.Unlock()
	taskLive.changed("chat_" + ch.ID)
	run := c.newCodeRunner(ch, func(node string) {
		ch.mu.Lock()
		ch.Busy, ch.RunOn = "answering", ""
		if node != "" {
			ch.Busy, ch.RunOn = "running", node
		}
		ch.mu.Unlock()
		taskLive.changed("chat_" + ch.ID)
	})
	go func() {
		defer cancel()
		started := func() {
			ch.mu.Lock()
			ch.Busy = "answering"
			ch.mu.Unlock()
			taskLive.changed("chat_" + ch.ID)
		}
		var msg ChatMsg
		if pref == chatResearch {
			msg = c.answerResearch(ctx, ch, hist, started)
		} else {
			msg = c.answerChatRun(ctx, ch.ID, pref, hist, askMem, run, started)
		}
		ch.mu.Lock()
		msg.ID, msg.At, msg.Alts, msg.Ver = newID("m"), time.Now(), alts, ver
		ch.Messages = append(ch.Messages, msg)
		ch.Updated, ch.Busy, ch.RunOn, ch.cancel = time.Now(), "", "", nil
		ch.saveLocked()
		ch.mu.Unlock()
		taskLive.changed("chat_" + ch.ID)
	}()
}

// chatHistory turns the stored messages into the model's conversation.
// Failed answers are left out.
func chatHistory(msgs []ChatMsg) []ChatMessage { return chatHistoryWith(msgs, nil) }

// chatHistoryWith also attaches saved images (read through load) to the
// newest messages that have some, at most chatHistoryImages in all; older
// images are left out of the call but stay in the chat.
func chatHistoryWith(msgs []ChatMsg, load func(string) string) []ChatMessage {
	return chatHistoryDocs(msgs, load, nil)
}

// chatHistoryDocs also puts each message's documents (read through doc) after its text.
func chatHistoryDocs(msgs []ChatMsg, load func(string) string, doc func(DocInfo) string) []ChatMessage {
	out := []ChatMessage{{Role: "system", Content: chatBaseSystem}}
	left := chatHistoryImages
	imgOf := make([][]string, len(msgs))
	for i := len(msgs) - 1; i >= 0 && load != nil; i-- {
		if msgs[i].Error != "" {
			continue
		}
		for j := len(msgs[i].Images) - 1; j >= 0 && left > 0; j-- {
			if u := load(msgs[i].Images[j]); u != "" {
				imgOf[i] = append([]string{u}, imgOf[i]...)
				left--
			}
		}
	}
	for i, m := range msgs {
		if m.Error != "" || (m.Text == "" && len(m.Images) == 0) {
			continue
		}
		text := m.Text
		if text == "" {
			text = "Describe this image."
		}
		for _, d := range m.Docs {
			if doc != nil {
				text += "\n\n" + docBlock(d, doc(d))
			}
		}
		text += runsSummary(m.Runs)
		out = append(out, ChatMessage{Role: m.Role, Content: text, Images: imgOf[i]})
	}
	return out
}

// fitHistory keeps the system message and as many of the latest messages as
// fit about budget characters (the newest one always).
func fitHistory(msgs []ChatMessage, budget int) []ChatMessage {
	if len(msgs) <= 2 {
		return msgs
	}
	used := len(msgs[0].Content)
	start := len(msgs)
	for i := len(msgs) - 1; i >= 1; i-- {
		used += len(msgs[i].Content) + 16
		if used > budget && i < len(msgs)-1 {
			break
		}
		start = i
	}
	// Start with a user turn, as chat templates expect.
	for start < len(msgs)-1 && msgs[start].Role != "user" {
		start++
	}
	return append([]ChatMessage{msgs[0]}, msgs[start:]...)
}

// answerChat waits for a free node, runs the call and returns the assistant message.
func (c *Controller) answerChat(ctx context.Context, chatID, pref string, hist []ChatMessage, started func()) ChatMsg {
	return c.answerChatMem(ctx, chatID, pref, hist, false, started)
}

// answerChatMem is answerChat that, with askMem, also asks the same node for a
// fact to remember once the answer is in (memory.go).
func (c *Controller) answerChatMem(ctx context.Context, chatID, pref string, hist []ChatMessage, askMem bool, started func()) ChatMsg {
	return c.answerChatRun(ctx, chatID, pref, hist, askMem, nil, started)
}

// answerChatRun is answerChatMem that, with run set, lets the model run code (chat_run.go).
func (c *Controller) answerChatRun(ctx context.Context, chatID, pref string, hist []ChatMessage, askMem bool, run *codeRunner, started func()) ChatMsg {
	var want func(string) bool
	for _, m := range hist {
		if len(m.Images) > 0 {
			want = c.nodeVision // an image in the call needs a node that can read it
		}
	}
	node, err := c.acquireNodeWhere(ctx, pref, want, errNoVisionChat, chatWait)
	if err != nil {
		return ChatMsg{Role: "assistant", Error: err.Error()}
	}
	defer c.releaseNode(node)
	started()
	ctxTok := c.nodeCtx(node)
	maxOut := min(4096, ctxTok/2)
	name := c.nodeLabel(node)
	hist = append([]ChatMessage(nil), hist...)
	clipForContext(hist, max((ctxTok-maxOut)*3-len(hist[0].Content)-2000, 2000)) // a long document must still leave room
	infer := func(ctx context.Context, spec InferenceSpec) (*InferenceOutput, error) {
		if run != nil && run.onRun != nil {
			run.onRun("") // back to answering after a run
		}
		live := taskLive.begin("chat_"+chatID, "", "Chat", name, false)
		defer live.End()
		return c.runInferenceJobLive(ctx, node, spec, "Chat", chatTimeout, live)
	}
	var out *InferenceOutput
	var note string
	c.mu.Lock()
	loaded := ""
	if hb := c.lastHB[node]; hb != nil {
		loaded = hb.LoadedModel
	}
	c.mu.Unlock()
	ran := false
	if run != nil {
		out, note, ran, err = c.answerWithRun(ctx, infer, loaded, hist, ctxTok, maxOut, run)
	}
	switch {
	case ran:
	case c.chatToolsOn():
		var n2 string
		out, n2, err = c.answerWithWeb(ctx, infer, loaded, hist, ctxTok, maxOut)
		note = joinNotes(note, n2)
	default:
		brief, n2 := c.chatSearchBrief(ctx, chatID, hist)
		note = joinNotes(note, n2)
		hist = withSearchBrief(hist, brief)
		out, err = infer(ctx, InferenceSpec{Messages: fitHistory(hist, (ctxTok-maxOut)*3), Params: InferenceParams{MaxTokens: maxOut}})
	}
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return ChatMsg{Role: "assistant", Node: name, Error: "stopped", Runs: run.done()}
	case err != nil:
		return ChatMsg{Role: "assistant", Node: name, Error: err.Error(), Note: note, Runs: run.done()}
	}
	text := out.Text
	if out.FinishReason == "length" {
		text += "\n\n…(cut off at the length limit)"
	}
	msg := ChatMsg{Role: "assistant", Text: text, Node: name, Model: out.Model, TPS: out.TokensPerSec, Note: note}
	if run != nil {
		msg.Runs = run.done()
	}
	if askMem {
		var owner []string
		for _, m := range hist {
			if m.Role == "user" {
				owner = append(owner, m.Content)
			}
		}
		plain := func(ctx context.Context, spec InferenceSpec) (*InferenceOutput, error) {
			return c.runInferenceJob(ctx, node, spec, "Chat: memory", 2*time.Minute)
		}
		msg.Suggest = suggestMemory(ctx, plain, owner, c.mem().forScope(""))
	}
	return msg
}

// chatModelPref is the start of a chat's node choice that means "this model, on
// any free node that has it loaded" (the chat picker's grouped cards).
const chatModelPref = "model:"

// acquireModelNode reserves a free slot on a ready node: pref "auto" (or "")
// takes the strongest free node, a node id waits for that node, and model
// (when set) only accepts nodes with that model loaded. It gives up after wait.
func (c *Controller) acquireModelNode(ctx context.Context, pref, model string, wait time.Duration) (string, error) {
	var want func(string) bool
	if model != "" {
		want = func(id string) bool {
			c.mu.Lock()
			defer c.mu.Unlock()
			hb := c.lastHB[id]
			return hb != nil && hb.LoadedModel == model
		}
	}
	return c.acquireNodeWhere(ctx, pref, want, fmt.Errorf("no ready node has %s loaded", model), wait)
}

// acquireNodeWhere is acquireModelNode with any test on the node (nil takes
// every ready node); noneErr is what it says when no ready node passes it.
func (c *Controller) acquireNodeWhere(ctx context.Context, pref string, want func(string) bool, noneErr error, wait time.Duration) (string, error) {
	if model, ok := strings.CutPrefix(pref, chatModelPref); ok {
		// "model:<name>": any free node that has this model loaded.
		pref, noneErr = "auto", fmt.Errorf("no ready node has %s loaded", model)
		inner := want
		want = func(id string) bool {
			c.mu.Lock()
			hb := c.lastHB[id]
			ok := hb != nil && hb.LoadedModel == model
			c.mu.Unlock()
			return ok && (inner == nil || inner(id))
		}
	}
	deadline := time.Now().Add(wait)
	for {
		ready := c.workerNodes()
		var cands []string
		for _, id := range ready {
			if pref != "" && pref != "auto" && id != pref {
				continue
			}
			if want != nil && !want(id) {
				continue
			}
			cands = append(cands, id)
		}
		for _, id := range cands {
			if c.tryReserve(id) {
				return id, nil
			}
		}
		if time.Now().After(deadline) {
			switch {
			case len(cands) == 0 && want != nil:
				return "", noneErr
			case len(cands) == 0 && pref != "" && pref != "auto":
				return "", errors.New("that node is offline, paused or has no model ready")
			case len(cands) == 0:
				return "", errors.New("no node has a model ready; load one on the Nodes page")
			}
			return "", fmt.Errorf("every node was busy for %s; try again later", wait.Round(time.Second))
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
