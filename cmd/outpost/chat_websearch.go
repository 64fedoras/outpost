package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Web search in chats: chats are answered by a local model, which cannot search. When the setting
// "Let chats search the web" is on and a Claude API key is saved, the controller first asks Claude
// (with its web search tool) to look the latest question up and write a short sourced brief, then
// hands the brief to the local model as extra context. Paid per search, counted in the spending
// ledger and the monthly cap. A failure never blocks the chat; the answer carries a note instead.
const chatSearchTimeout = 2 * time.Minute

const chatSearchSystem = "You are a research helper for another assistant. Use web search to find current, factual information " +
	"for the user's latest question (use the earlier conversation only for context). Reply with a brief of at most 300 words: " +
	"key facts, dates and numbers, each followed by its source URL. If the question needs no web lookup, reply with exactly NONE."

// chatSearchBrief returns text to prepend to the question and a short note for the chat message.
func (c *Controller) chatSearchBrief(ctx context.Context, chatID string, hist []ChatMessage) (brief, note string) {
	c.mu.Lock()
	on, model := c.settings.ChatWebSearch, c.settings.ClaudeModel
	c.mu.Unlock()
	if !on {
		return "", ""
	}
	if c.anthropicKey() == "" {
		return "", "Web search skipped: no Claude API key is saved (Settings, API keys)."
	}
	var convo strings.Builder
	for _, m := range hist {
		if m.Role == "system" {
			continue
		}
		convo.WriteString(m.Role + ": " + searchClip(m.Content, 1500) + "\n\n")
	}
	req := llmRequest{System: chatSearchSystem, User: searchClip(convo.String(), 8000), MaxTokens: 1500, Temperature: 0.2, Label: "Chat web search", WebSearch: true}
	est := estimateCost(model, req.Size(), req.MaxTokens/2) + webSearchUses*webSearchUSD
	if limit, spent := c.monthlyCap(); limit > 0 && spent+est > limit {
		return "", "Web search skipped: the monthly cloud spending cap would be exceeded."
	}
	sctx, cancel := context.WithTimeout(ctx, chatSearchTimeout)
	defer cancel()
	resp, err := callClaude(sctx, c.anthropicKey(), model, req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return "", ""
		}
		return "", "Web search failed: " + searchClip(err.Error(), 200)
	}
	if serr := c.recordSpendFor("claude", "chat_"+chatID, "Chat web search", c.spendPlannerLabel("claude", model, ""), resp); serr != nil {
		_ = serr // the ledger is best effort here; the answer is still useful
	}
	text := strings.TrimSpace(resp.Text)
	if text == "" || strings.EqualFold(text, "NONE") {
		return "", ""
	}
	note = fmt.Sprintf("Searched the web (%d search(es), %s).", resp.WebSearches, fmtUSD(resp.CostUSD))
	return "Web search results (from a research helper, with sources; cite the URLs when you use them):\n" + text, note
}

// withSearchBrief puts the brief in front of the newest user message.
func withSearchBrief(hist []ChatMessage, brief string) []ChatMessage {
	if brief == "" || len(hist) == 0 {
		return hist
	}
	out := append([]ChatMessage(nil), hist...)
	last := len(out) - 1
	if out[last].Role == "user" {
		out[last].Content = brief + "\n\n---\nUser question:\n" + out[last].Content
	}
	return out
}

func searchClip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
