package main

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTakeOffer(t *testing.T) {
	c := chatController(t)
	ch := &Chat{ID: "chat_o", dir: c.chatsDir()}
	s := offerScope{chat: ch}

	// A marked feature that is off: the marker goes, the card comes.
	text, o := c.takeOffer("I could not check that.\n\n[[offer: web_search]]", s)
	if text != "I could not check that." || o == nil || o.Key != "web_search" || !o.Admin || o.Label == "" {
		t.Fatalf("marked: %q %+v", text, o)
	}
	// No Claude key: turning it on uses the free built-in search, and the card says so.
	if !strings.Contains(o.Hint, "free") {
		t.Fatalf("hint %q", o.Hint)
	}
	// A plain "I can't browse" counts too.
	if _, o := c.takeOffer("As an AI, I don't have the ability to browse the internet.", s); o == nil || o.Key != "web_search" {
		t.Fatalf("phrase: %+v", o)
	}
	// Talking about someone else's internet access does not.
	if _, o := c.takeOffer("The node has no internet access, so install the package offline.", s); o != nil {
		t.Fatalf("false phrase: %+v", o)
	}
	// Unknown keys are removed but offer nothing; run_code needs a node that allows commands.
	if text, o := c.takeOffer("ok [[offer: nuke_it]] [[offer: run_code]]", s); text != "ok" || o != nil {
		t.Fatalf("unknown: %q %+v", text, o)
	}
	// Already on: nothing to offer.
	c.settings.ChatWebSearch = true
	if _, o := c.takeOffer("[[offer: web_search]]", s); o != nil {
		t.Fatalf("on: %+v", o)
	}
	c.settings.ChatWebSearch = false
	// Not now in this chat: not offered again here.
	ch.OffersDeclined = []string{"web_search"}
	if _, o := c.takeOffer("[[offer: web_search]]", s); o != nil {
		t.Fatalf("declined: %+v", o)
	}
	if p := c.offerPrompt(s); p != "" {
		t.Fatalf("prompt after decline: %q", p)
	}
	// Projects: no run_code, ever.
	if _, o := c.takeOffer("[[offer: run_code]]", offerScope{project: &Project{dir: t.TempDir()}}); o != nil {
		t.Fatalf("project run_code: %+v", o)
	}
}

func TestOfferPrompt(t *testing.T) {
	c := chatController(t)
	ch := &Chat{ID: "chat_p", dir: c.chatsDir()}
	p := c.offerPrompt(offerScope{chat: ch})
	if !strings.Contains(p, "- web_search:") || strings.Contains(p, "run_code") || strings.Contains(p, "- memory:") || !strings.Contains(p, "[[offer: web_search]]") {
		t.Fatalf("prompt %q", p)
	}
	c.settings.ChatWebSearch = true
	if p := c.offerPrompt(offerScope{chat: ch}); p != "" {
		t.Fatalf("nothing off, prompt %q", p)
	}
	// Run code is offered once a node allows commands.
	c.conns["n"] = &nodeConn{}
	c.nodes["n"].ExecEnabled = true
	if p := c.offerPrompt(offerScope{chat: ch}); !strings.Contains(p, "- run_code:") {
		t.Fatalf("run_code missing: %q", p)
	}
}

func TestSettingOfferApplyRetriesChat(t *testing.T) {
	c := chatController(t)
	var specs []InferenceSpec
	var mu sync.Mutex
	fakeInferNode(t, c, "n", &specs, &mu)
	c.settings.ChatWebSearch = true // keep the prompt to the one feature under test
	c.settings.MemoryOff = true
	m := mux2(c)
	m.HandleFunc("POST /api/setting-offer", c.handleSettingOffer)
	do := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}
	// The fake node echoes the owner's message, so the marker comes back in the answer.
	rec := do("/api/chats", `{"text":"remember I use Go [[offer: memory]]"}`)
	if rec.Code != 200 {
		t.Fatalf("new chat %d %s", rec.Code, rec.Body)
	}
	id := strings.Split(strings.Split(rec.Body.String(), `"id":"`)[1], `"`)[0]
	ch := c.getChat(id)
	wait := func() {
		for i := 0; i < 300; i++ {
			ch.mu.Lock()
			done := ch.Busy == "" && len(ch.Messages) == 2
			ch.mu.Unlock()
			if done {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("no answer")
	}
	wait()
	mu.Lock()
	sys := specs[0].Messages[0].Content
	mu.Unlock()
	if !strings.Contains(sys, "- memory:") {
		t.Fatalf("system prompt lacks the offer list: %q", sys)
	}
	ans := ch.Messages[1]
	if ans.Offer == nil || ans.Offer.Key != "memory" || strings.Contains(ans.Text, "[[") {
		t.Fatalf("answer %+v", ans)
	}
	// Bad requests change nothing.
	if rec := do("/api/setting-offer", `{"chat":"`+id+`","msg":"`+ans.ID+`","key":"pooling","act":"apply"}`); rec.Code != 400 {
		t.Fatalf("unknown key %d", rec.Code)
	}
	if rec := do("/api/setting-offer", `{"chat":"`+id+`","msg":"`+ans.ID+`","key":"memory","act":"maybe"}`); rec.Code != 400 || !c.settings.MemoryOff {
		t.Fatalf("bad act %d", rec.Code)
	}
	// Apply: memory goes on, the answer is written again with the old one kept as a version.
	rec = do("/api/setting-offer", `{"chat":"`+id+`","msg":"`+ans.ID+`","key":"memory","act":"apply"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"retried":true`) {
		t.Fatalf("apply %d %s", rec.Code, rec.Body)
	}
	if c.settings.MemoryOff {
		t.Fatal("memory still off")
	}
	wait()
	ch.mu.Lock()
	again := ch.Messages[1]
	ch.mu.Unlock()
	if again.ID == ans.ID || again.Offer != nil || len(again.Alts) != 1 || again.Alts[0].Msgs[0].Offer != nil {
		t.Fatalf("retried answer %+v", again)
	}
}

func TestSettingOfferDismissAndProjectApply(t *testing.T) {
	c := chatController(t)
	c.projects = map[string]*Project{}
	ch := &Chat{ID: "chat_d", dir: c.chatsDir(), Messages: []ChatMsg{{ID: "u1", Role: "user", Text: "news?"}, {ID: "a1", Role: "assistant", Text: "off", Offer: &SettingOffer{Key: "web_search"}}}}
	c.chats[ch.ID] = ch
	call := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c.handleSettingOffer(rec, httptest.NewRequest("POST", "/api/setting-offer", strings.NewReader(body)))
		return rec
	}
	if rec := call(`{"chat":"chat_d","msg":"a1","key":"web_search","act":"dismiss"}`); rec.Code != 200 {
		t.Fatalf("dismiss %d %s", rec.Code, rec.Body)
	}
	if ch.Messages[1].Offer != nil || len(ch.OffersDeclined) != 1 || c.settings.ChatWebSearch {
		t.Fatalf("after dismiss %+v %v", ch.Messages[1], ch.OffersDeclined)
	}
	if rec := call(`{"chat":"nope","msg":"a1","key":"web_search","act":"dismiss"}`); rec.Code != 404 {
		t.Fatalf("no chat %d", rec.Code)
	}

	// A project answer that started a task is not replaced; the setting still goes on.
	p := &Project{ID: "proj_x", dir: t.TempDir()}
	c.projects[p.ID] = p
	pc := p.chatLog()
	pc.Messages = []ProjectChatMsg{{ID: "u", Role: "user", Text: "what is hermes?"}, {ID: "a", Role: "assistant", Text: "off", Tasks: []string{"t1"}, Offer: &SettingOffer{Key: "web_search"}}}
	if rec := call(`{"project":"proj_x","msg":"a","key":"run_code","act":"apply"}`); rec.Code != 400 {
		t.Fatalf("run_code in a project %d", rec.Code)
	}
	rec := call(`{"project":"proj_x","msg":"a","key":"web_search","act":"apply"}`)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"retried":true`) {
		t.Fatalf("project apply %d %s", rec.Code, rec.Body)
	}
	if !c.settings.ChatWebSearch || c.settings.ChatSearchWith != "local" || pc.Messages[1].Offer != nil || len(pc.Messages) != 2 {
		t.Fatalf("after project apply: %+v %q %+v", c.settings.ChatWebSearch, c.settings.ChatSearchWith, pc.Messages)
	}
}
