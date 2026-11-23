package main

// Context-aware model switching for chat ("Auto" in the chat picker).
//
// For each message, chat works out what it needs (coding, vision or general).
// It uses a loaded model that suits it. If none does, and a node allows it
// (NodeRec.ChatSwitch), it loads one: first a model already on that node's
// disk, then a download, and only when space is short it removes models, one
// of each type kept. A download and any removal are put to the user first, with
// a countdown (chatPrompt). Removals and big downloads never go ahead on a
// timeout alone; chat then answers with whatever is loaded.
//
// planChatLoad, pickEvictions and classifyNeed are pure or nearly so, so they
// can be tested without a cluster.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"sync"
	"time"
)

// What a message needs from the model.
const (
	needGeneral = "general"
	needCoding  = "coding"
	needVision  = "vision"
)

// Limits, fixed (not settings) in the first version; tests shorten the timings.
var (
	chatAskDownload   = 10 * time.Second // download only, nothing removed: goes ahead when nobody answers
	chatAskOther      = 30 * time.Second // anything else: falls back to the loaded model when nobody answers
	chatSwitchGuard   = 24 * time.Hour   // a model used or fetched this recently is never removed
	chatSwitchCool    = 10 * time.Minute // a node whose model chat switched is left alone this long
	chatLoadTimeout   = 40 * time.Minute // download and load
	chatRemoveTimeout = 2 * time.Minute
)

const (
	chatAutoGoBytes   = 20 << 30 // a download this big or bigger always needs a click
	chatMaxRemovals   = 2        // models removed for one message
	chatRemovalsToday = 3        // removals per node per day
)

var (
	needCodeRe = regexp.MustCompile("(?i)```|\\b(func|def|class|import|package|return|const|let|var|struct|interface|select\\s+.+\\s+from|#include|stack trace|traceback|segfault|compile[rd]?|refactor|unit tests?|regex|goroutine|null pointer|exception|syntax error|bug|debug|function|script|algorithm|api endpoint|dockerfile|git (rebase|merge|commit))\\b|\\.(go|py|js|ts|tsx|jsx|rs|java|kt|c|cc|cpp|h|hpp|cs|rb|php|sh|sql|html|css|json|ya?ml|toml)\\b")
)

// classifyNeed says what the last user message needs: vision when an image is
// in the conversation, coding when it looks like code work, otherwise general.
func classifyNeed(hist []ChatMessage) string {
	for _, m := range hist {
		if len(m.Images) > 0 {
			return needVision
		}
	}
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Role == "user" {
			if needCodeRe.MatchString(hist[i].Content) {
				return needCoding
			}
			break
		}
	}
	return needGeneral
}

func validNeed(s string) bool { return s == needGeneral || s == needCoding || s == needVision }

// modelSuitsLocked says whether a model can answer a need. A model that is both
// coding and vision (Qwen3.6) suits both. A model of unknown kind (a custom
// upload) is trusted for coding and general work, never for vision.
// c.mu must be held.
func (c *Controller) modelSuitsLocked(need, name string) bool {
	vision := false
	if m := c.models[name]; m != nil && m.hasVision() {
		vision = true
	}
	ce := catalogEntry(name)
	if ce != nil && ce.MMProj != "" {
		vision = true
	}
	switch need {
	case needVision:
		return vision
	case needCoding:
		return ce == nil || ce.Coding
	}
	return true
}

// modelKindLocked is a model's type: vision, coding, general, or "" when the
// controller cannot tell. c.mu must be held.
func (c *Controller) modelKindLocked(name string) string {
	if m := c.models[name]; m != nil && m.hasVision() {
		return needVision
	}
	if ce := catalogEntry(name); ce != nil {
		switch {
		case ce.MMProj != "":
			return needVision
		case ce.Coding:
			return needCoding
		}
		return needGeneral
	}
	return ""
}

// chatSwitchState is what switching remembers between messages (in memory only).
type chatSwitchState struct {
	mu       sync.Mutex
	used     map[string]time.Time   // node|model: last use or load
	removals map[string][]time.Time // node: removals made for chat
	switched map[string]time.Time   // node: last switch made for chat
	prompts  map[string]chan string // chat id: the user's answer to the open prompt
}

func (s *chatSwitchState) touch(node, model string) {
	if model == "" {
		return
	}
	s.mu.Lock()
	if s.used == nil {
		s.used = map[string]time.Time{}
	}
	s.used[node+"|"+model] = time.Now()
	s.mu.Unlock()
}

func (s *chatSwitchState) lastUsed(node, model string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used[node+"|"+model]
}

func (s *chatSwitchState) removalsSince(node string, d time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, t := range s.removals[node] {
		if time.Since(t) < d {
			n++
		}
	}
	return n
}

func (s *chatSwitchState) noteRemoval(node string) {
	s.mu.Lock()
	if s.removals == nil {
		s.removals = map[string][]time.Time{}
	}
	s.removals[node] = append(s.removals[node], time.Now())
	s.mu.Unlock()
}

func (s *chatSwitchState) noteSwitch(node string) {
	s.mu.Lock()
	if s.switched == nil {
		s.switched = map[string]time.Time{}
	}
	s.switched[node] = time.Now()
	s.mu.Unlock()
}

func (s *chatSwitchState) switchedAt(node string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.switched[node]
}

// ---- eviction ----

// evictCand is a model on a node that might be removed to make room.
type evictCand struct {
	Name        string    `json:"name"`
	Label       string    `json:"label,omitempty"`
	Kind        string    `json:"kind"`
	Bytes       int64     `json:"bytes"`
	Restorable  bool      `json:"restorable,omitempty"` // the controller holds a copy: cheap to copy back
	Protected   bool      `json:"-"`                    // loaded, used lately, in use, or of unknown type
	Last        time.Time `json:"-"`
	CountedOnly bool      `json:"-"` // counts toward "one of each type" but is never offered
}

// pickEvictions chooses the models to remove so that need bytes fit in free.
// It takes from the type with the most models first, never the last of a type,
// never a protected model, and at most maxRm models. Among equals it prefers
// models the controller can copy back, then the least recently used. ok is false
// when that is not enough (nothing is then to be removed).
func pickEvictions(cands []evictCand, need, free int64, maxRm int) (rm []evictCand, ok bool) {
	short := need - free
	if short <= 0 {
		return nil, true
	}
	count := map[string]int{}
	for _, c := range cands {
		count[c.Kind]++
	}
	taken := map[string]bool{}
	for len(rm) < maxRm && short > 0 {
		best := -1
		for i, c := range cands {
			if taken[c.Name] || c.Protected || c.CountedOnly || c.Kind == "" || count[c.Kind] < 2 {
				continue
			}
			if best < 0 || evictBefore(c, cands[best], count) {
				best = i
			}
		}
		if best < 0 {
			return nil, false
		}
		c := cands[best]
		taken[c.Name] = true
		count[c.Kind]--
		short -= c.Bytes
		rm = append(rm, c)
	}
	if short > 0 {
		return nil, false
	}
	return rm, true
}

func evictBefore(a, b evictCand, count map[string]int) bool {
	if count[a.Kind] != count[b.Kind] {
		return count[a.Kind] > count[b.Kind]
	}
	if a.Restorable != b.Restorable {
		return a.Restorable
	}
	if !a.Last.Equal(b.Last) {
		return a.Last.Before(b.Last)
	}
	if a.Bytes != b.Bytes {
		return a.Bytes > b.Bytes
	}
	return a.Name < b.Name
}

// ---- planning ----

// chatLoadPlan is what chat would do on one node to get a model for a need.
type chatLoadPlan struct {
	Node     string      `json:"node"`
	NodeName string      `json:"node_name"`
	Model    string      `json:"model"`
	Label    string      `json:"label"`
	Bytes    int64       `json:"bytes"`
	Download bool        `json:"download"` // false: already on the node's disk
	Removals []evictCand `json:"removals,omitempty"`
	Prev     string      `json:"-"` // the model loaded now, replaced by the load
	class    int         // 0 on disk, 1 download, 2 download with removals
}

// freeForModelsLocked is how much more the node may store: the smaller of the
// disk's free space and what its storage limit leaves. c.mu must be held.
func (c *Controller) freeForModelsLocked(id string, n *NodeRec) int64 {
	free := int64(0)
	if n.Hello != nil {
		free = int64(n.Hello.ModelsFreeBytes)
	}
	if room, _, limited := c.storageRoom(id, n); limited {
		free = min(free, room)
	}
	return free
}

// planChatLoad finds the cheapest way to get a model for need onto an allowed,
// idle node, or says why it cannot. c.mu must not be held.
func (c *Controller) planChatLoad(need string) (*chatLoadPlan, string) {
	return c.planChatLoadWith(need, false)
}

// whyNoOptIn is planChatLoad's reason when no node has chat switching turned on.
const whyNoOptIn = "no node allows chat to switch models (turn on Let chat switch models here on the Nodes page)"

// planChatLoadWith is planChatLoad; ignoreOptIn also considers nodes that have not allowed switching,
// to show what turning it on would do (the "Allow" prompt). Nothing is changed by planning.
func (c *Controller) planChatLoadWith(need string, ignoreOptIn bool) (*chatLoadPlan, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	helpers := c.helperNodesLocked()
	var best *chatLoadPlan
	why := whyNoOptIn
	if ignoreOptIn {
		why = "no online node can load a model for this"
	}
	for id, n := range c.nodes {
		if (!n.chatSwitchOn() && !ignoreOptIn) || c.conns[id] == nil || n.Hello == nil {
			continue
		}
		hb := c.lastHB[id]
		busy := hb == nil || hb.Paused || c.pipelines[id] != nil || c.reserved[id] > 0 || len(n.Pool) > 0 || helpers[id] || !n.Hours.open(now)
		if busy {
			why = "the nodes that allow switching are busy"
			continue
		}
		if hb.LoadedModel != "" && now.Sub(c.chatSw.switchedAt(id)) < chatSwitchCool {
			why = "chat switched a model on the node that allows it a moment ago"
			continue
		}
		p, w := c.planOnNodeLocked(id, n, hb, need)
		if p == nil {
			why = w
			continue
		}
		if best == nil || p.class < best.class || (p.class == best.class && p.Bytes < best.Bytes) {
			best = p
		}
	}
	if best == nil {
		return nil, why
	}
	return best, ""
}

// planOnNodeLocked plans one node. c.mu must be held.
func (c *Controller) planOnNodeLocked(id string, n *NodeRec, hb *Heartbeat, need string) (*chatLoadPlan, string) {
	opts, buildOK := c.loadOptionsFor(id, 3)
	if !buildOK {
		return nil, "the node has no llama.cpp build yet"
	}
	var cands []LoadOption
	for _, o := range opts {
		if o.Fit == fitNo || !c.modelSuitsLocked(need, o.Name) || (need == needCoding && catalogEntry(o.Name) == nil) {
			continue
		}
		cands = append(cands, o)
	}
	if len(cands) == 0 {
		return nil, fmt.Sprintf("no %s model fits that node", need)
	}
	free := c.freeForModelsLocked(id, n)
	rank := func(o LoadOption) int {
		if o.Rank > 0 {
			return o.Rank
		}
		return 100
	}
	sort.SliceStable(cands, func(a, b int) bool {
		x, y := cands[a], cands[b]
		if x.OnNode != y.OnNode {
			return x.OnNode
		}
		fx, fy := x.OnNode || x.Bytes <= free, y.OnNode || y.Bytes <= free
		if fx != fy {
			return fx
		}
		if rank(x) != rank(y) {
			return rank(x) < rank(y)
		}
		return x.Bytes > y.Bytes
	})
	ev := c.evictCandsLocked(id, n, hb)
	removalsLeft := chatRemovalsToday - c.chatSw.removalsSince(id, 24*time.Hour)
	for _, o := range cands {
		p := &chatLoadPlan{Node: id, NodeName: c.nodeLabelLocked(id), Model: o.Name, Label: firstNonEmpty(o.Label, o.Name), Bytes: o.Bytes, Prev: hb.LoadedModel}
		switch {
		case o.OnNode:
			return p, ""
		case o.Bytes <= free:
			p.Download, p.class = true, 1
			return p, ""
		case removalsLeft <= 0:
			continue
		}
		rm, ok := pickEvictions(ev, o.Bytes, free, min(chatMaxRemovals, removalsLeft))
		if ok {
			p.Download, p.class, p.Removals = true, 2, rm
			return p, ""
		}
	}
	return nil, fmt.Sprintf("not enough room for a %s model, and nothing can be removed without losing the last of a type or a model in use", need)
}

// evictCandsLocked lists the models on a node for pickEvictions. c.mu must be held.
func (c *Controller) evictCandsLocked(id string, n *NodeRec, hb *Heartbeat) []evictCand {
	var out []evictCand
	for _, sm := range n.Hello.Models {
		m := c.models[sm.Name]
		kind := c.modelKindLocked(sm.Name)
		ec := evictCand{Name: sm.Name, Kind: kind, Bytes: sm.Bytes, Last: c.chatSw.lastUsed(id, sm.Name)}
		if m != nil {
			ec.Label = m.Label
			ec.Restorable = m.Storage != "direct" && m.Status == "ready"
		}
		switch {
		case m == nil, kind == "", hb != nil && sm.Name == hb.LoadedModel:
			ec.Protected = true // unknown to the controller, unknown type, or in use
		case time.Since(ec.Last) < chatSwitchGuard:
			ec.Protected = true
		}
		out = append(out, ec)
	}
	return out
}

// ---- the prompt ----

// ChatPrompt is the question or progress line chat shows while it gets a model.
type ChatPrompt struct {
	State    string      `json:"state"` // ask | allow | working
	Need     string      `json:"need"`
	Node     string      `json:"node"`
	NodeName string      `json:"node_name"`
	Model    string      `json:"model"`
	Label    string      `json:"label"`
	Bytes    int64       `json:"bytes"`
	Download bool        `json:"download"`
	Removals []evictCand `json:"removals,omitempty"`
	Seconds  int         `json:"seconds,omitempty"` // countdown length
	Deadline int64       `json:"deadline_ms,omitempty"`
	AutoGo   bool        `json:"auto_go,omitempty"` // goes ahead when the countdown ends (else falls back)
	Loaded   string      `json:"loaded,omitempty"`  // the loaded model that would answer instead
	Text     string      `json:"text,omitempty"`
}

func (c *Controller) setChatPrompt(chatID string, p *ChatPrompt) {
	ch := c.getChat(chatID)
	if ch == nil {
		return
	}
	ch.mu.Lock()
	ch.Prompt = p
	ch.mu.Unlock()
	taskLive.changed("chat_" + chatID)
}

// loadedFallback is the name of a model that is loaded and ready somewhere, or "".
func (c *Controller) loadedFallback() string {
	for _, id := range c.workerNodes() {
		c.mu.Lock()
		hb := c.lastHB[id]
		c.mu.Unlock()
		if hb != nil && hb.LoadedModel != "" {
			return hb.LoadedModel
		}
	}
	return ""
}

// chatSwitch decides, for one "Auto" message, whether to load a model first.
// It returns the need to tag the answer with, a note to add to it, and a
// finished answer when the user cancelled. It does nothing for a pinned node
// or model, or when the chat page is not the asker.
func (c *Controller) chatSwitch(ctx context.Context, chatID, pref string, hist []ChatMessage) (need, note string, abort *ChatMsg) {
	if pref != "" && pref != "auto" {
		return "", "", nil
	}
	need = classifyNeed(hist)
	ch := c.getChat(chatID)
	if ch == nil {
		return need, "", nil
	}
	ch.mu.Lock()
	if ch.needOverride != "" {
		need, ch.needOverride = ch.needOverride, ""
	}
	ch.mu.Unlock()
	if need == needGeneral {
		return need, "", nil
	}
	for _, id := range c.workerNodes() {
		c.mu.Lock()
		hb := c.lastHB[id]
		ok := hb != nil && hb.LoadedModel != "" && c.modelSuitsLocked(need, hb.LoadedModel)
		c.mu.Unlock()
		if ok {
			return need, "", nil // a suitable model is loaded: nothing to do
		}
	}
	plan, why := c.planChatLoad(need)
	if plan == nil && why == whyNoOptIn {
		// Nothing may switch yet: say what turning it on would do, and ask once instead of just refusing.
		if offer, _ := c.planChatLoadWith(need, true); offer != nil {
			switch c.askAllow(ctx, chatID, need, offer) {
			case "allow_node", "allow_all":
				plan, why = c.planChatLoad(need)
			case "cancel":
				c.setChatPrompt(chatID, nil)
				return need, "", &ChatMsg{Role: "assistant", Error: "Cancelled. Nothing was changed on the node."}
			case "ctx":
				c.setChatPrompt(chatID, nil)
				return need, "", &ChatMsg{Role: "assistant", Error: "stopped"}
			default:
				c.setChatPrompt(chatID, nil)
				return need, fmt.Sprintf("Wanted a %s model; chat may not load one yet (answered with the loaded model).", need), nil
			}
		}
	}
	if plan == nil {
		if why != "" {
			note = fmt.Sprintf("Wanted a %s model; none is loaded and %s.", need, why)
		}
		return need, note, nil
	}
	fallback := c.loadedFallback()
	if plan.Download {
		p := &ChatPrompt{State: "ask", Need: need, Node: plan.Node, NodeName: plan.NodeName, Model: plan.Model, Label: plan.Label,
			Bytes: plan.Bytes, Download: true, Removals: plan.Removals, Loaded: fallback}
		p.AutoGo = len(plan.Removals) == 0 && plan.Bytes < chatAutoGoBytes
		wait := chatAskOther
		if p.AutoGo {
			wait = chatAskDownload
		}
		p.Seconds = int(wait / time.Second)
		p.Deadline = time.Now().Add(wait).UnixMilli()
		switch c.askChat(ctx, chatID, p, wait) {
		case "go":
		case "cancel":
			c.setChatPrompt(chatID, nil)
			return need, "", &ChatMsg{Role: "assistant", Error: "Cancelled. Nothing was changed on the node."}
		case "ctx":
			c.setChatPrompt(chatID, nil)
			return need, "", &ChatMsg{Role: "assistant", Error: "stopped"}
		default: // loaded
			c.setChatPrompt(chatID, nil)
			return need, fmt.Sprintf("Wanted %s (%s) for %s but it needs your OK; answered with the loaded model.", plan.Label, humanBytes(plan.Bytes), need), nil
		}
	}
	text := fmt.Sprintf("Loading %s on %s…", plan.Label, plan.NodeName)
	if plan.Download {
		text = fmt.Sprintf("Downloading %s (%s) to %s and loading it…", plan.Label, humanBytes(plan.Bytes), plan.NodeName)
	}
	c.setChatPrompt(chatID, &ChatPrompt{State: "working", Need: need, Node: plan.Node, NodeName: plan.NodeName, Model: plan.Model, Label: plan.Label,
		Bytes: plan.Bytes, Download: plan.Download, Removals: plan.Removals, Loaded: fallback, Text: text})
	err := c.runChatLoad(ctx, plan)
	c.setChatPrompt(chatID, nil)
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return need, "", &ChatMsg{Role: "assistant", Error: "stopped"}
	case err != nil:
		return need, fmt.Sprintf("Could not load %s on %s (%v); answered with the loaded model.", plan.Label, plan.NodeName, err), nil
	}
	return need, fmt.Sprintf("Loaded %s on %s for this %s message.", plan.Label, plan.NodeName, need), nil
}

// askChat shows the prompt and waits for go, loaded or cancel, or the timer.
func (c *Controller) askChat(ctx context.Context, chatID string, p *ChatPrompt, wait time.Duration) string {
	ans := make(chan string, 1)
	c.chatSw.mu.Lock()
	if c.chatSw.prompts == nil {
		c.chatSw.prompts = map[string]chan string{}
	}
	c.chatSw.prompts[chatID] = ans
	c.chatSw.mu.Unlock()
	defer func() {
		c.chatSw.mu.Lock()
		delete(c.chatSw.prompts, chatID)
		c.chatSw.mu.Unlock()
	}()
	c.setChatPrompt(chatID, p)
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case a := <-ans:
		return a
	case <-t.C:
		if p.AutoGo {
			return "go"
		}
		return "loaded"
	case <-ctx.Done():
		return "ctx"
	}
}

// askAllow offers to turn on "Let chat switch models" for one node or all nodes and applies the answer
// (allow_node, allow_all); anything else is returned for chatSwitch to act on. Nobody answering counts as "loaded".
func (c *Controller) askAllow(ctx context.Context, chatID, need string, offer *chatLoadPlan) string {
	p := &ChatPrompt{State: "allow", Need: need, Node: offer.Node, NodeName: offer.NodeName, Model: offer.Model, Label: offer.Label,
		Bytes: offer.Bytes, Download: offer.Download, Loaded: c.loadedFallback(), Seconds: int(chatAskOther / time.Second)}
	p.Deadline = time.Now().Add(chatAskOther).UnixMilli()
	a := c.askChat(ctx, chatID, p, chatAskOther)
	switch a {
	case "allow_node":
		c.setChatSwitch(offer.Node)
	case "allow_all":
		c.setChatSwitch("")
	}
	if a == "allow_node" || a == "allow_all" {
		c.setChatPrompt(chatID, nil)
	}
	return a
}

// setChatSwitch turns Let chat switch models on for one node, or for every online node when id is "".
func (c *Controller) setChatSwitch(id string) {
	c.mu.Lock()
	var send []*nodeConn
	var settings []NodeSettings
	for nid, n := range c.nodes {
		if (id != "" && nid != id) || (id == "" && c.conns[nid] == nil) {
			continue
		}
		t := true
		n.ChatSwitch = &t
		if nc := c.conns[nid]; nc != nil {
			send = append(send, nc)
			settings = append(settings, c.nodeSettingsLocked(n))
		}
	}
	c.saveNodesLocked()
	c.mu.Unlock()
	for i, nc := range send {
		_ = nc.send("node.settings", settings[i])
	}
}

// runChatLoad removes the planned models, then downloads if needed and loads.
func (c *Controller) runChatLoad(ctx context.Context, plan *chatLoadPlan) error {
	for _, r := range plan.Removals {
		c.setNodeOp(plan.Node, OpStatus{Op: "model.remove", Target: r.Name, State: "removing"})
		c.mu.Lock()
		nc := c.conns[plan.Node]
		c.mu.Unlock()
		if nc == nil {
			return errors.New("node is offline")
		}
		if err := nc.send("model.remove", ModelRemove{Name: r.Name}); err != nil {
			return err
		}
		if err := c.waitNodeOp(ctx, plan.Node, "model.remove", r.Name, chatRemoveTimeout); err != nil {
			return fmt.Errorf("could not remove %s: %w", r.Name, err)
		}
		c.chatSw.noteRemoval(plan.Node)
	}
	lctx, cancel := context.WithTimeout(ctx, chatLoadTimeout)
	defer cancel()
	if err := c.loadForTask(lctx, plan.Node, plan.Model); err != nil {
		return err
	}
	c.chatSw.noteSwitch(plan.Node)
	c.chatSw.touch(plan.Node, plan.Model)
	return nil
}

// handleChatPrompt answers the open prompt: {"action": "go" | "loaded" | "cancel"}.
func (c *Controller) handleChatPrompt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
	}
	if err := readJSONBody(w, r, &req); err != nil || (req.Action != "go" && req.Action != "loaded" && req.Action != "cancel" && req.Action != "allow_node" && req.Action != "allow_all") {
		writeJSON(w, 400, map[string]string{"error": "action must be go, loaded, cancel, allow_node or allow_all"})
		return
	}
	id := r.PathValue("id")
	c.chatSw.mu.Lock()
	ans := c.chatSw.prompts[id]
	c.chatSw.mu.Unlock()
	if ans == nil {
		writeJSON(w, 409, map[string]string{"error": "nothing to answer (it may have timed out)"})
		return
	}
	select {
	case ans <- req.Action:
	default:
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// canOfferSwitch reports whether turning on chat switching somewhere would give a model for need.
func (c *Controller) canOfferSwitch(need string) bool {
	p, _ := c.planChatLoadWith(need, true)
	return p != nil
}

// anySwitchNode reports whether some online node lets chat switch models.
func (c *Controller) anySwitchNode() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, n := range c.nodes {
		if n.chatSwitchOn() && c.conns[id] != nil {
			return true
		}
	}
	return false
}
