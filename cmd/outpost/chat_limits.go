package main

import (
	"context"
	"time"
)

// How long one chat answer may take at the node's measured speed. It stays well
// inside chatTimeout so a slow node ends an answer itself instead of timing out.
const chatAnswerBudget = 10 * time.Minute

// chatOutCap is how many tokens one chat answer may use: about a quarter of the
// context (4K to 16K), never more than half of it (the prompt must fit), and
// never more than the node can write in chatAnswerBudget at tps tokens a second
// (0 means unknown). A thinking model's thinking counts against it.
func chatOutCap(ctxTok int, tps float64) int {
	n := min(max(ctxTok/4, 4096), 16384)
	if tps > 0 {
		n = min(n, max(int(tps*chatAnswerBudget.Seconds()), 1024))
	}
	return min(n, ctxTok/2)
}

// thinkBudgetOf is how much of an answer of maxOut tokens a thinking model may
// spend on thinking before it is asked to answer without: the rest is for the answer.
func thinkBudgetOf(maxOut int) int { return maxOut * 55 / 100 }

// nodeSpeed is the node's measured generation speed in tokens a second, 0 if unknown.
func (c *Controller) nodeSpeed(id string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := c.nodes[id]; n != nil && n.Bench != nil {
		if n.Bench.GenTPS > 0 {
			return n.Bench.GenTPS
		}
		return n.Bench.TokPerSec
	}
	return 0
}

// withThinkBudget wraps infer so a thinking model cannot spend a whole answer
// on thinking: past the budget the call is stopped and asked again without
// thinking. note is set when that happened.
func withThinkBudget(infer inferFn, maxOut int, note *string) inferFn {
	return func(ctx context.Context, spec InferenceSpec) (*InferenceOutput, error) {
		if spec.Params.MaxTokens < maxOut || spec.Params.ThinkBudget > 0 {
			return infer(ctx, spec)
		}
		spec.Params.ThinkBudget = thinkBudgetOf(maxOut)
		out, err := infer(ctx, spec)
		if err != nil || out == nil || out.FinishReason != "think_limit" {
			return out, err
		}
		spec.Params.ThinkBudget, spec.Params.NoThink = 0, true
		*note = "The model's thinking used up its share of the length limit, so it was asked again to answer directly."
		return infer(ctx, spec)
	}
}
