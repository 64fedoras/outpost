package main

// Setting offers: when a chat or the project conversation cannot do what the
// owner asked only because a setting is off, the answer carries a small card
// ("Web search is off. [Turn on web search] [Not now]") instead of just
// explaining where the setting lives. Nothing changes until the owner clicks;
// a click flips that one setting and answers the same message again.
//
// Two ways an answer gets an offer:
//   - The model is told which offerable features are off here and ends its
//     answer with a line like [[offer: web_search]] when it is blocked by one
//     (offerPrompt). The line is taken out of the text (takeOffer).
//   - Small models often just say "I can't browse the internet", so a few plain
//     phrases also count (offerPhrases), but only for a feature that is off.
//
// The registry (settingOffers) is the one place to add another case. Each
// entry says whether it changes Settings for everyone (admin) or only this
// chat; only entries that can actually be changed by one click are listed
// (a missing API key, a node's "allow commands" switch or memory pooling need
// a choice the card cannot make for the owner).

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// SettingOffer is the card under an answer.
type SettingOffer struct {
	Key   string `json:"key"`             // registry key
	Why   string `json:"why"`             // one sentence: what was blocked
	Label string `json:"label"`           // the button
	Hint  string `json:"hint,omitempty"`  // what the change does, cost included
	Where string `json:"where,omitempty"` // where the setting lives, to change it back
	Admin bool   `json:"admin,omitempty"` // changes Settings for the whole cluster (an admin's call once there are other users)
}

// offerScope is where an answer was written: a chat or a project conversation.
type offerScope struct {
	chat    *Chat
	project *Project
	// declinedFrom: the project's conversation chat, whose "Not now" answers count here (chat_project.go).
	declinedFrom *Chat
}

type offerDef struct {
	key    string
	about  string // for the model: what the feature does
	why    string
	label  string
	where  string
	admin  bool
	chatOK bool // offered in chats
	projOK bool // offered in project conversations
	// off reports whether the feature is off here and one click could turn it on.
	off func(c *Controller, s offerScope) bool
	// hint describes what turning it on does (may depend on other settings).
	hint func(c *Controller) string
	// apply turns it on.
	apply func(c *Controller, s offerScope) error
	// phrases are what a model that ignores the marker tends to say instead.
	phrases *regexp.Regexp
}

var settingOffers = []offerDef{
	{
		key:    "web_search",
		about:  "searching the web for current events, prices, versions, or names you do not know",
		why:    "Web search is off, so this could not be looked up.",
		label:  "Turn on web search",
		where:  "Settings → Tasks → Planner → Let chats search the web",
		admin:  true,
		chatOK: true, projOK: true,
		off: func(c *Controller, _ offerScope) bool {
			c.mu.Lock()
			defer c.mu.Unlock()
			return !c.settings.ChatWebSearch
		},
		hint: func(c *Controller) string {
			switch webSearchWithOnApply(c) {
			case "":
				return "Chats and project conversations may search the web. Searches use Claude (about $0.01 each, counted in Spending)."
			case "openrouter":
				return "Chats and project conversations may search the web. Searches use OpenRouter (a few cents at most, counted in Spending)."
			}
			return "Chats and project conversations may search the web with the built-in free search."
		},
		apply: func(c *Controller, _ offerScope) error {
			with := webSearchWithOnApply(c)
			c.mu.Lock()
			defer c.mu.Unlock()
			c.settings.ChatWebSearch, c.settings.ChatSearchWith = true, with
			return c.saveSettingsLocked()
		},
		phrases: regexp.MustCompile(`(?i)\bI(?:'m| am)?\s+(?:currently\s+)?(?:can(?:no|')t|unable to|not able to|don't have|do not have)\b[^.\n]{0,40}\b(?:brows(?:e|ing)|internet access|access (?:to )?the (?:internet|web)|web access|real[- ]time (?:data|information|access)|search(?:ing)? the (?:web|internet)|look (?:it|that|this|them) up online)`),
	},
	{
		key:    "run_code",
		about:  "running code (Python, Go or a shell script) on a node in a sandbox to check or compute something",
		why:    "Run code is off in this chat, so nothing could be run.",
		label:  "Turn on Run code",
		where:  "the Run code switch above the message box",
		chatOK: true,
		off: func(c *Controller, s offerScope) bool {
			if s.chat == nil {
				return false
			}
			s.chat.mu.Lock()
			on := s.chat.RunCode
			s.chat.mu.Unlock()
			return !on && len(c.execNodes()) > 0 // with no node that allows commands, the switch would not help
		},
		hint: func(*Controller) string {
			return "This chat may run code on a node that allows commands, in the same sandbox as task tests."
		},
		apply: func(_ *Controller, s offerScope) error {
			s.chat.mu.Lock()
			s.chat.RunCode = true
			s.chat.saveLocked()
			s.chat.mu.Unlock()
			return nil
		},
		phrases: regexp.MustCompile(`(?i)\bI(?:'m| am)?\s+(?:can(?:no|')t|unable to|not able to|don't have the ability to|do not have the ability to)\s+(?:actually\s+)?(?:run|execute)\s+(?:code|it|this|that|the (?:code|script|program)|programs?|scripts?)\b`),
	},
	{
		key:    "memory",
		about:  "remembering facts about the owner across conversations",
		why:    "Memory is off, so this will not be remembered.",
		label:  "Turn on memory",
		where:  "Settings → Memory",
		admin:  true,
		chatOK: true, projOK: true,
		off: func(c *Controller, _ offerScope) bool { return !c.memoryOn() },
		hint: func(*Controller) string {
			return "Remembered facts go into chats and project conversations, and answers may suggest new ones."
		},
		apply: func(c *Controller, _ offerScope) error {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.settings.MemoryOff = false
			return c.saveSettingsLocked()
		},
	},
}

// webSearchWithOnApply is who searches once web search is turned on from a card:
// what Settings already says if that can work, else the free built-in search
// (no Claude key) so the click is never a step into "skipped: no API key".
func webSearchWithOnApply(c *Controller) string {
	c.mu.Lock()
	with, orModel := c.settings.ChatSearchWith, c.settings.OpenRouterModel
	c.mu.Unlock()
	switch with {
	case "local":
		return with
	case "openrouter":
		if c.openRouterKey() != "" && orModel != "" {
			return with
		}
	default:
		if c.anthropicKey() != "" {
			return ""
		}
	}
	return "local"
}

func offerDefFor(key string) *offerDef {
	for i := range settingOffers {
		if settingOffers[i].key == key {
			return &settingOffers[i]
		}
	}
	return nil
}

func (d *offerDef) fits(s offerScope) bool {
	return (s.chat != nil && d.chatOK) || (s.project != nil && d.projOK)
}

// offerOpen reports whether d may be offered here now: it fits, it is off, and
// the owner did not say Not now to it in this conversation.
func (c *Controller) offerOpen(d *offerDef, s offerScope, declined []string) bool {
	return d.fits(s) && !slices.Contains(declined, d.key) && d.off(c, s)
}

// declinedOffers is what the owner said Not now to in this conversation.
func declinedOffers(s offerScope) []string {
	switch {
	case s.chat != nil:
		s.chat.mu.Lock()
		defer s.chat.mu.Unlock()
		return append([]string(nil), s.chat.OffersDeclined...)
	case s.declinedFrom != nil:
		s.declinedFrom.mu.Lock()
		defer s.declinedFrom.mu.Unlock()
		return append([]string(nil), s.declinedFrom.OffersDeclined...)
	case s.project != nil:
		pc := s.project.chatLog()
		pc.mu.Lock()
		defer pc.mu.Unlock()
		return append([]string(nil), pc.OffersDeclined...)
	}
	return nil
}

// offerPrompt is the system prompt paragraph naming the features that are off
// here, or "" when none is.
func (c *Controller) offerPrompt(s offerScope) string {
	declined := declinedOffers(s)
	var lines []string
	first := ""
	for i := range settingOffers {
		d := &settingOffers[i]
		if c.offerOpen(d, s, declined) {
			lines = append(lines, "- "+d.key+": "+d.about)
			if first == "" {
				first = d.key
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\nThese features are switched off here, and the owner can switch one on with a single click:\n" + strings.Join(lines, "\n") +
		"\nIf you cannot do what the owner asked only because one of them is off, say so in one short sentence, give the best answer you can without it, and end your answer with a line like [[offer: " + first + "]] naming that feature. Use it only when the feature is really needed, at most once per answer."
}

var offerMarkRe = regexp.MustCompile(`(?i)\[\[\s*offer\s*:\s*([a-z_]+)\s*\]\]`)

// takeOffer removes offer markers from an answer and returns the card to show,
// if any: the first marked feature that is open here, else the first open one
// whose plain phrases the answer uses.
func (c *Controller) takeOffer(text string, s offerScope) (string, *SettingOffer) {
	var marked []string
	for _, m := range offerMarkRe.FindAllStringSubmatch(text, -1) {
		marked = append(marked, strings.ToLower(m[1]))
	}
	if len(marked) > 0 {
		text = strings.TrimSpace(offerMarkRe.ReplaceAllString(text, ""))
	}
	declined := declinedOffers(s)
	pick := func(d *offerDef) *SettingOffer {
		return &SettingOffer{Key: d.key, Why: d.why, Label: d.label, Hint: d.hint(c), Where: d.where, Admin: d.admin}
	}
	for _, k := range marked {
		if d := offerDefFor(k); d != nil && c.offerOpen(d, s, declined) {
			return text, pick(d)
		}
	}
	for i := range settingOffers {
		d := &settingOffers[i]
		if d.phrases != nil && d.phrases.MatchString(text) && c.offerOpen(d, s, declined) {
			return text, pick(d)
		}
	}
	return text, nil
}

// mayChangeSetting is where a per-user permission check goes once Outpost has
// more than one user: today every signed-in session and full-access API token
// is the owner (scoped tokens never reach this handler, see authWith).
func (c *Controller) mayChangeSetting(_ *http.Request, _ *offerDef) bool { return true }

// handleSettingOffer answers a card: {"chat"|"project", "msg", "key", "act": "apply"|"dismiss"}.
// apply turns the setting on, takes the card off the message and, when that
// message is the latest answer, answers the owner's message again.
func (c *Controller) handleSettingOffer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Chat    string `json:"chat"`
		Project string `json:"project"`
		Msg     string `json:"msg"`
		Key     string `json:"key"`
		Act     string `json:"act"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil || (req.Act != "apply" && req.Act != "dismiss") {
		writeJSON(w, 400, map[string]string{"error": "act must be apply or dismiss"})
		return
	}
	d := offerDefFor(req.Key)
	if d == nil {
		writeJSON(w, 400, map[string]string{"error": "unknown setting"})
		return
	}
	var s offerScope
	switch {
	case req.Chat != "":
		if s.chat = c.getChat(req.Chat); s.chat == nil {
			writeJSON(w, 404, map[string]string{"error": "no such chat"})
			return
		}
	case req.Project != "":
		if s.project = c.getProject(req.Project); s.project == nil {
			writeJSON(w, 404, map[string]string{"error": "no such project"})
			return
		}
		s.chat = c.projectConvChat(s.project, true) // the card is on a message of the project's conversation chat
	default:
		writeJSON(w, 400, map[string]string{"error": "name the chat or project"})
		return
	}
	fit := s
	if req.Project != "" {
		fit = offerScope{project: s.project} // the card was offered under the project's rules: no Run code
	}
	if !d.fits(fit) {
		writeJSON(w, 400, map[string]string{"error": "that setting does not apply here"})
		return
	}
	if req.Act == "apply" && !c.mayChangeSetting(r, d) {
		writeJSON(w, 403, map[string]string{"error": "only an admin can change this setting"})
		return
	}
	if req.Act == "apply" && d.off(c, s) {
		if err := d.apply(c, s); err != nil {
			writeJSON(w, 500, map[string]string{"error": "could not save the setting: " + err.Error()})
			return
		}
	}
	retried := c.chatOfferDone(s.chat, req.Msg, d.key, req.Act == "apply")
	writeJSON(w, 200, map[string]bool{"ok": true, "retried": retried})
}

// chatOfferDone takes the card off the message, remembers a Not now, and after
// an apply regenerates that answer when it is the latest and the chat is idle.
func (c *Controller) chatOfferDone(ch *Chat, msgID, key string, applied bool) bool {
	ch.mu.Lock()
	i := ch.msgIndex(msgID)
	if i >= 0 {
		ch.Messages[i].Offer = nil
	}
	if !applied && !slices.Contains(ch.OffersDeclined, key) {
		ch.OffersDeclined = append(ch.OffersDeclined, key)
	}
	if applied && i >= 1 && i == len(ch.Messages)-1 && ch.Busy == "" && ch.Messages[i].Role == "assistant" && len(ch.Messages[i].Tasks) == 0 {
		all := ch.cutTail(i)
		c.beginAnswerLocked(ch, all, len(all)) // unlocks ch.mu
		return true
	}
	ch.saveLocked()
	ch.mu.Unlock()
	taskLive.changed("chat_" + ch.ID)
	return false
}
