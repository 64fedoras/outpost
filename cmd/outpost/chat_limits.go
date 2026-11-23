package main

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
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

// chatAutoContinues is how many times a chat answer that stopped at the length
// limit is carried on automatically before it is left cut off with the Continue button.
const chatAutoContinues = 3

// withAutoContinue wraps infer so an answer that stopped at the length limit is
// carried on by asking the model to continue, up to chatAutoContinues times and
// never past chatTimeout in all. It stops when a round adds nothing, when the
// conversation would no longer fit the context, or when the call was a tool call.
// The pieces are joined into one answer; note says how many rounds were added.
func withAutoContinue(infer inferFn, ctxTok, maxOut int, note *string) inferFn {
	return func(ctx context.Context, spec InferenceSpec) (*InferenceOutput, error) {
		start := time.Now()
		out, err := infer(ctx, spec)
		if err != nil || out == nil || spec.Params.MaxTokens < maxOut {
			return out, err
		}
		rounds := 0
		for rounds < chatAutoContinues && out.FinishReason == "length" && strings.TrimSpace(out.Text) != "" && len(out.ToolCalls) == 0 {
			// Room left: the prompt so far, the answer so far and another full answer must fit.
			if ctx.Err() != nil || time.Since(start) > chatTimeout/2 || (ctxTok > 0 && out.PromptTokens+out.OutputTokens+maxOut > ctxTok) {
				break
			}
			next := spec
			next.Messages = append(append([]ChatMessage(nil), spec.Messages...),
				ChatMessage{Role: "assistant", Content: out.Text, Reasoning: tailBytes(out.Reasoning, 16<<10)},
				ChatMessage{Role: "user", Content: "Please continue from exactly where you stopped."})
			more, err := infer(ctx, next)
			if err != nil || more == nil || strings.TrimSpace(more.Text) == "" {
				break // keep what there is; the answer stays marked as cut off
			}
			joined := *out
			joined.Text += more.Text
			joined.FinishReason = more.FinishReason
			joined.OutputTokens += more.OutputTokens
			joined.Reasoning = strings.TrimSpace(joined.Reasoning + "\n\n" + more.Reasoning)
			joined.ThinkSecs += more.ThinkSecs
			joined.TokensPerSec, joined.Model = more.TokensPerSec, more.Model
			out = &joined
			rounds++
		}
		if rounds > 0 {
			*note = fmt.Sprintf("Carried on automatically %d time%s after hitting the length limit.", rounds, map[bool]string{true: "", false: "s"}[rounds == 1])
		}
		return out, nil
	}
}

// tailBytes is the last n bytes of s (from a character boundary).
func tailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}
