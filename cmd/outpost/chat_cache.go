package main

// Prompt cache reuse for chat. llama-server keeps the processed start of the
// last prompt on a slot and skips it when the next request begins with the
// same text, so a follow-up answers sooner if it goes to the node that
// answered before. That is only ever a speed-up: the controller holds the
// whole chat and any node can rebuild the prompt from it, so everything here
// is a preference that gives way to availability, never a requirement.

import (
	"context"
	"strings"
	"time"
)

// chatAff is where a chat was last answered.
type chatAff struct {
	node, model string
	prompt      int // tokens in that prompt: a short chat gains little from waiting
}

var (
	chatAffinityWait   = 6 * time.Second // how long a follow-up waits for a busy preferred node
	chatAffinityMinTok = 1000            // below this prompt size it never waits
	nodeGoneGrace      = 8 * time.Second // offline this long mid-answer: give up on that node
	nodeGoneTick       = time.Second
)

const chatNodeRetries = 2 // other nodes tried when one drops out mid-answer

func (c *Controller) noteChatNode(chatID, node, model string, prompt int) {
	if ch := c.getChat(chatID); ch != nil {
		ch.mu.Lock()
		ch.aff = chatAff{node, model, prompt}
		ch.mu.Unlock()
	}
}

// acquireChatNode is acquireNodeWhere that first tries the node that answered
// this chat last (only for Auto or a model choice, never over a pinned node).
func (c *Controller) acquireChatNode(ctx context.Context, chatID, pref string, want func(string) bool, noneErr error, wait time.Duration) (string, error) {
	if pref == "" || pref == "auto" || strings.HasPrefix(pref, chatModelPref) {
		if id, ok := c.tryAffinity(ctx, chatID, pref, want); ok {
			return id, nil
		}
	}
	return c.acquireNodeWhere(ctx, pref, want, noneErr, wait)
}

// tryAffinity reserves the chat's last node if it is ready with the same
// model loaded, waiting a few seconds when it is busy and the prompt is big.
func (c *Controller) tryAffinity(ctx context.Context, chatID, pref string, want func(string) bool) (string, bool) {
	ch := c.getChat(chatID)
	if ch == nil {
		return "", false
	}
	ch.mu.Lock()
	aff := ch.aff
	ch.mu.Unlock()
	if aff.node == "" || (want != nil && !want(aff.node)) {
		return "", false
	}
	if m, ok := strings.CutPrefix(pref, chatModelPref); ok && m != aff.model {
		return "", false
	}
	wait := time.Duration(0)
	if aff.prompt >= chatAffinityMinTok {
		wait = chatAffinityWait
	}
	deadline := time.Now().Add(wait)
	for {
		if !c.affinityReady(aff) {
			return "", false
		}
		if c.tryReserve(aff.node) {
			return aff.node, true
		}
		if !time.Now().Before(deadline) {
			return "", false
		}
		select {
		case <-ctx.Done():
			return "", false
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// affinityReady: the node is connected, ready, and still has that model.
func (c *Controller) affinityReady(a chatAff) bool {
	for _, id := range c.workerNodes() {
		if id != a.node {
			continue
		}
		c.mu.Lock()
		hb := c.lastHB[id]
		ok := hb != nil && hb.LoadedModel == a.model
		c.mu.Unlock()
		return ok
	}
	return false
}

// runChatJob runs one chat call on a node and stops waiting for it once the
// node has been offline for nodeGoneGrace, so a dropped node costs seconds
// instead of the minute the job sweeper needs to declare its job lost.
func (c *Controller) runChatJob(ctx context.Context, node, name string, spec InferenceSpec, live *liveHandle) (*InferenceOutput, error) {
	ictx, cancel := context.WithCancel(ctx)
	defer cancel()
	gone := make(chan struct{})
	go c.watchNodeGone(ictx, node, func() { close(gone); cancel() })
	out, err := c.runInferenceJobLive(ictx, node, spec, "Chat", chatTimeout, live)
	select {
	case <-gone:
		if ctx.Err() == nil {
			return nil, errf("lost", true, "%s went offline while answering", name)
		}
	default:
	}
	return out, err
}

// watchNodeGone calls gone once the node has had no connection for nodeGoneGrace.
func (c *Controller) watchNodeGone(ctx context.Context, node string, gone func()) {
	var since time.Time
	t := time.NewTicker(nodeGoneTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			c.mu.Lock()
			_, up := c.conns[node]
			c.mu.Unlock()
			switch {
			case up:
				since = time.Time{}
			case since.IsZero():
				since = now
			case now.Sub(since) >= nodeGoneGrace:
				gone()
				return
			}
		}
	}
}
