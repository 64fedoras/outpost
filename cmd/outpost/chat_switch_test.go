package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClassifyNeed(t *testing.T) {
	user := func(s string) []ChatMessage {
		return []ChatMessage{{Role: "system", Content: "x"}, {Role: "user", Content: s}}
	}
	for text, want := range map[string]string{
		"why does this Go function leak a goroutine?": needCoding,
		"fix the bug in main.go":                      needCoding,
		"```py\nprint(1)\n```":                        needCoding,
		"write a haiku about autumn":                  needGeneral,
		"what is the capital of France":               needGeneral,
		"summarise this email for me":                 needGeneral,
	} {
		if got := classifyNeed(user(text)); got != want {
			t.Errorf("%q: %s, want %s", text, got, want)
		}
	}
	img := user("what is this?")
	img[1].Images = []string{"data:image/png;base64,AA=="}
	if classifyNeed(img) != needVision {
		t.Errorf("an image did not mean vision")
	}
	// Only the last user message counts for coding.
	hist := []ChatMessage{{Role: "user", Content: "fix main.go"}, {Role: "assistant", Content: "ok"}, {Role: "user", Content: "thanks, now a poem"}}
	if classifyNeed(hist) != needGeneral {
		t.Errorf("an old coding message still decided the need")
	}
}

func ec(name, kind string, gbs int64, restorable bool) evictCand {
	return evictCand{Name: name, Kind: kind, Bytes: gbs * gb, Restorable: restorable}
}

func names(rm []evictCand) string {
	var s []string
	for _, r := range rm {
		s = append(s, r.Name)
	}
	return strings.Join(s, ",")
}

func TestPickEvictions(t *testing.T) {
	// Two coding and one general, room needed for a vision model: a coding one goes, not the general.
	cands := []evictCand{ec("code-big", needCoding, 18, false), ec("code-small", needCoding, 5, false), ec("general", needGeneral, 9, false)}
	rm, ok := pickEvictions(cands, 7*gb, 2*gb, 2)
	if !ok || len(rm) != 1 || rm[0].Name != "code-big" && rm[0].Name != "code-small" {
		t.Fatalf("got %v ok=%v", names(rm), ok)
	}
	// Nothing to do when it already fits.
	if rm, ok := pickEvictions(cands, 1*gb, 2*gb, 2); !ok || len(rm) != 0 {
		t.Errorf("fits already: %v %v", names(rm), ok)
	}
	// One of each type: nothing may be removed.
	one := []evictCand{ec("c", needCoding, 18, false), ec("g", needGeneral, 9, false), ec("v", needVision, 7, false)}
	if rm, ok := pickEvictions(one, 5*gb, 0, 2); ok || rm != nil {
		t.Errorf("removed the last of a type: %v", names(rm))
	}
	// Protected and unknown models are never removed, even as duplicates.
	prot := []evictCand{ec("a", needCoding, 10, false), ec("b", needCoding, 10, false), ec("u", "", 10, false)}
	prot[0].Protected, prot[1].Protected = true, true
	if _, ok := pickEvictions(prot, 5*gb, 0, 2); ok {
		t.Errorf("removed a protected model")
	}
	// Two removals are the most; three would be needed here.
	three := []evictCand{ec("a", needCoding, 4, false), ec("b", needCoding, 4, false), ec("c", needCoding, 4, false), ec("d", needCoding, 4, false)}
	if rm, ok := pickEvictions(three, 12*gb, 0, 2); ok {
		t.Errorf("took more than the limit: %v", names(rm))
	}
	if rm, ok := pickEvictions(three, 8*gb, 0, 2); !ok || len(rm) != 2 {
		t.Errorf("two removals should do: %v %v", names(rm), ok)
	}
	// Most models first, then copies the controller can restore, then the least recently used.
	mix := []evictCand{ec("code-a", needCoding, 5, false), ec("code-b", needCoding, 5, true), ec("gen-a", needGeneral, 5, true), ec("gen-b", needGeneral, 5, true), ec("gen-c", needGeneral, 5, true)}
	rm, _ = pickEvictions(mix, 5*gb, 0, 1)
	if names(rm) != "gen-a" {
		t.Errorf("most models first: %v", names(rm))
	}
	lru := []evictCand{ec("old", needCoding, 5, false), ec("new", needCoding, 5, false)}
	lru[0].Last, lru[1].Last = time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)
	if rm, _ = pickEvictions(lru, 5*gb, 0, 1); names(rm) != "old" {
		t.Errorf("least recently used first: %v", names(rm))
	}
}

func switchCtl(h *Hello, loaded string) *Controller {
	c := loadOptsCtl(map[string]*Hello{"n": h})
	c.lastHB = map[string]*Heartbeat{"n": {LoadedModel: loaded, InferenceRdy: true}}
	c.catShapes = fakeCatalogShapes()
	c.nodes["n"].ChatSwitch = bp(true)
	return c
}

func libModel(c *Controller, id string, direct bool) {
	ce := catalogEntry(id)
	m := modelFromCatalog(ce, "direct")
	if !direct {
		m.Storage = "controller"
	}
	m.Status, m.BytesTotal = "ready", ce.Bytes
	c.models[id] = m
}

func bigCPU() *Hello {
	return &Hello{OS: "linux", Arch: "amd64", MemBWGBs: 60, RAMBytes: 128 * tgb, ModelsFreeBytes: 500 * tgb}
}

func TestPlanChatLoad(t *testing.T) {
	// A coding model is wanted, the node has none: download the best fit.
	c := switchCtl(bigCPU(), "")
	p, why := c.planChatLoad(needCoding)
	if p == nil || !p.Download || len(p.Removals) != 0 || p.Node != "n" {
		t.Fatalf("download plan: %+v (%s)", p, why)
	}
	// Not allowed on the node: no plan, with a reason.
	c.nodes["n"].ChatSwitch = bp(false)
	if p, why := c.planChatLoad(needCoding); p != nil || why == "" {
		t.Errorf("planned on a node that did not opt in: %+v", p)
	}
	c.nodes["n"].ChatSwitch = bp(true)
	// A coding model already on the disk is used, with no download.
	id := "qwen2.5-coder-7b-q4km"
	libModel(c, id, true)
	c.nodes["n"].Hello.Models = []ModelInfo{{Name: id, Bytes: catalogEntry(id).Bytes}}
	if p, _ := c.planChatLoad(needCoding); p == nil || p.Download || p.Model != id {
		t.Errorf("on-disk plan: %+v", p)
	}
	// Busy nodes and nodes chat switched a moment ago are left alone.
	c.reserved = map[string]int{"n": 1}
	if p, _ := c.planChatLoad(needCoding); p != nil {
		t.Errorf("planned on a busy node")
	}
	c.reserved = nil
	c.lastHB["n"].LoadedModel = "something"
	c.chatSw.noteSwitch("n")
	if p, _ := c.planChatLoad(needCoding); p != nil {
		t.Errorf("planned inside the cooldown")
	}
}

func TestPlanChatLoadMakesRoomByType(t *testing.T) {
	var vis *CatalogEntry
	cat := catalogNow()
	for i := range cat {
		if cat[i].MMProj != "" && (vis == nil || cat[i].Bytes < vis.Bytes) {
			vis = &cat[i]
		}
	}
	if vis == nil {
		t.Skip("no vision model in the catalog")
	}
	h := bigCPU()
	h.ModelsFreeBytes = uint64(vis.Bytes - 1<<20) // just too little
	c := switchCtl(h, "")
	var on []ModelInfo
	for _, id := range []string{"qwen2.5-coder-3b-q4km", "qwen2.5-coder-7b-q4km"} {
		libModel(c, id, false)
		on = append(on, ModelInfo{Name: id, Bytes: catalogEntry(id).Bytes})
	}
	general := ""
	for _, e := range cat {
		if !e.Coding && e.MMProj == "" {
			general = e.ID
			break
		}
	}
	if general != "" {
		libModel(c, general, false)
		on = append(on, ModelInfo{Name: general, Bytes: catalogEntry(general).Bytes})
	}
	c.nodes["n"].Hello.Models = on
	p, why := c.planChatLoad(needVision)
	if p == nil || p.Model == "" || !p.Download {
		t.Fatalf("no plan: %s", why)
	}
	if len(p.Removals) == 0 {
		t.Errorf("expected a removal to make room: %+v", p)
	}
	for _, r := range p.Removals {
		if r.Name == general {
			t.Errorf("removed the only general model: %v", names(p.Removals))
		}
		if r.Kind != needCoding {
			t.Errorf("removed a %s model", r.Kind)
		}
	}
	// A recently used model is protected.
	for _, m := range on {
		c.chatSw.touch("n", m.Name)
	}
	p, _ = c.planChatLoad(needVision)
	if p != nil && len(p.Removals) > 0 {
		t.Errorf("removed a model used a moment ago: %v", names(p.Removals))
	}
}

func TestAskChatTimersAndAnswers(t *testing.T) {
	c := &Controller{chats: map[string]*Chat{}}
	c.chats["c1"] = &Chat{ID: "c1"}
	ctx := context.Background()
	// Nobody answers: a plain download goes ahead, anything else falls back.
	if got := c.askChat(ctx, "c1", &ChatPrompt{AutoGo: true}, 20*time.Millisecond); got != "go" {
		t.Errorf("download-only timeout: %s", got)
	}
	if got := c.askChat(ctx, "c1", &ChatPrompt{AutoGo: false}, 20*time.Millisecond); got != "loaded" {
		t.Errorf("removal timeout: %s", got)
	}
	if c.chats["c1"].Prompt == nil {
		t.Errorf("the prompt was not shown")
	}
	// An answer from the page ends the wait.
	m := http.NewServeMux()
	m.HandleFunc("POST /api/chats/{id}/prompt", c.handleChatPrompt)
	done := make(chan string)
	go func() { done <- c.askChat(ctx, "c1", &ChatPrompt{}, 5*time.Second) }()
	for i := 0; i < 100; i++ {
		c.chatSw.mu.Lock()
		n := len(c.chatSw.prompts)
		c.chatSw.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	post := func(body string) int {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest("POST", "/api/chats/c1/prompt", strings.NewReader(body)))
		return rec.Code
	}
	if post(`{"action":"bogus"}`) != 400 {
		t.Errorf("a bad action was accepted")
	}
	if post(`{"action":"cancel"}`) != 200 {
		t.Errorf("cancel refused")
	}
	if got := <-done; got != "cancel" {
		t.Errorf("answer: %s", got)
	}
	if post(`{"action":"go"}`) != 409 {
		t.Errorf("an answer with no open prompt was accepted")
	}
	// Stopping the chat ends the wait.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if got := c.askChat(cctx, "c1", &ChatPrompt{}, time.Minute); got != "ctx" {
		t.Errorf("stopped: %s", got)
	}
}

// With removals in the plan nobody answering means "use what is loaded": nothing is
// removed or downloaded, and the answer says why.
func TestChatSwitchFallsBackWhenNobodyAnswers(t *testing.T) {
	var vis *CatalogEntry
	cat := catalogNow()
	for i := range cat {
		if cat[i].MMProj != "" && (vis == nil || cat[i].Bytes < vis.Bytes) {
			vis = &cat[i]
		}
	}
	h := bigCPU()
	h.ModelsFreeBytes = uint64(vis.Bytes - 1<<20)
	c := switchCtl(h, "")
	c.chats = map[string]*Chat{"c1": {ID: "c1"}}
	var on []ModelInfo
	for _, id := range []string{"qwen2.5-coder-3b-q4km", "qwen2.5-coder-7b-q4km"} {
		libModel(c, id, false)
		on = append(on, ModelInfo{Name: id, Bytes: catalogEntry(id).Bytes})
	}
	c.nodes["n"].Hello.Models = on
	old := chatAskOther
	chatAskOther = 30 * time.Millisecond
	defer func() { chatAskOther = old }()
	hist := []ChatMessage{{Role: "system"}, {Role: "user", Content: "what is in this picture?", Images: []string{"data:image/png;base64,AA=="}}}
	need, note, abort := c.chatSwitch(context.Background(), "c1", "auto", hist)
	if need != needVision || abort != nil || !strings.Contains(note, "needs your OK") {
		t.Fatalf("need %s abort %v note %q", need, abort, note)
	}
	if c.chats["c1"].Prompt != nil {
		t.Errorf("the prompt stayed up")
	}
	// A pinned node never switches.
	if need, note, _ := c.chatSwitch(context.Background(), "c1", "n", hist); need != "" || note != "" {
		t.Errorf("pinned node switched: %q %q", need, note)
	}
	// The override from the "chosen for" menu wins over the rules.
	c.chats["c1"].needOverride = needGeneral
	if need, _, _ := c.chatSwitch(context.Background(), "c1", "auto", hist); need != needGeneral {
		t.Errorf("override ignored: %s", need)
	}
}

// A node that has not opted in is offered "Allow": answering allow_node turns chat switching on for that node
// only; nobody answering keeps the loaded model and changes nothing.
func TestChatSwitchAllowPrompt(t *testing.T) {
	c := switchCtl(bigCPU(), "")
	c.dataDir = t.TempDir() // setChatSwitch saves the node list
	c.nodes["n"].ChatSwitch = bp(false)
	c.chats = map[string]*Chat{"c1": {ID: "c1"}}
	old := chatAskOther
	chatAskOther = 40 * time.Millisecond
	defer func() { chatAskOther = old }()
	hist := []ChatMessage{{Role: "system"}, {Role: "user", Content: "what is in this picture?", Images: []string{"data:image/png;base64,AA=="}}}
	need, note, abort := c.chatSwitch(context.Background(), "c1", "auto", hist)
	if need != needVision || abort != nil || !strings.Contains(note, "may not load one yet") {
		t.Fatalf("need %s abort %v note %q", need, abort, note)
	}
	if c.nodes["n"].chatSwitchOn() {
		t.Fatal("switching turned on without an answer")
	}
	// Answering allow_node applies to the node in the offer.
	offer, _ := c.planChatLoadWith(needVision, true)
	if offer == nil {
		t.Fatal("no offer")
	}
	chatAskOther = 5 * time.Second
	done := make(chan string)
	go func() { done <- c.askAllow(context.Background(), "c1", needVision, offer) }()
	for i := 0; i < 100; i++ {
		c.chatSw.mu.Lock()
		n := len(c.chatSw.prompts)
		c.chatSw.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ch := c.chats["c1"]
	ch.mu.Lock()
	p := ch.Prompt
	ch.mu.Unlock()
	if p == nil || p.State != "allow" || p.NodeName == "" {
		t.Fatalf("prompt: %+v", p)
	}
	c.chatSw.mu.Lock()
	reply := c.chatSw.prompts["c1"]
	c.chatSw.mu.Unlock()
	reply <- "allow_node"
	got := <-done
	c.mu.Lock()
	on := c.nodes["n"].chatSwitchOn()
	c.mu.Unlock()
	if got != "allow_node" || !on {
		t.Fatalf("answer %s, switch %v", got, on)
	}
}

func bp(b bool) *bool { return &b }

// Node settings that were never saved are on; explicit values are kept.
func TestNodeSettingDefaultsOn(t *testing.T) {
	var n NodeRec
	if !n.chatSwitchOn() || !n.taskModelsOn() || !n.yieldOn() {
		t.Fatal("unset settings should default to on")
	}
	var old NodeRec
	if err := json.Unmarshal([]byte(`{"id":"x","name":"x"}`), &old); err != nil {
		t.Fatal(err)
	}
	if !old.chatSwitchOn() || !old.taskModelsOn() || !old.yieldOn() {
		t.Fatal("a node saved without the settings should default to on")
	}
	n = NodeRec{ChatSwitch: bp(false), TaskModels: bp(false), YieldGPU: bp(false)}
	b, _ := json.Marshal(n)
	var back NodeRec
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.chatSwitchOn() || back.taskModelsOn() || back.yieldOn() {
		t.Fatalf("explicit off lost in a save/load round trip: %s", b)
	}
}
