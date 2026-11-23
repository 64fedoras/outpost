package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
)

func TestChatOutCap(t *testing.T) {
	for _, c := range []struct {
		ctx  int
		tps  float64
		want int
	}{
		{8192, 0, 4096}, {4096, 0, 2048}, {32768, 0, 8192}, {131072, 0, 16384},
		{32768, 10, 6000}, {32768, 1, 1024}, {65536, 100, 16384},
	} {
		if got := chatOutCap(c.ctx, c.tps); got != c.want {
			t.Errorf("chatOutCap(%d, %v) = %d, want %d", c.ctx, c.tps, got, c.want)
		}
	}
}

func TestWithThinkBudgetAsksAgainWithoutThinking(t *testing.T) {
	var specs []InferenceSpec
	infer := func(_ context.Context, s InferenceSpec) (*InferenceOutput, error) {
		specs = append(specs, s)
		if s.Params.NoThink {
			return &InferenceOutput{Text: "answer", FinishReason: "stop"}, nil
		}
		return &InferenceOutput{FinishReason: "think_limit"}, nil
	}
	var note string
	out, err := withThinkBudget(infer, 4096, &note)(context.Background(), InferenceSpec{Params: InferenceParams{MaxTokens: 4096}})
	if err != nil || out.Text != "answer" || len(specs) != 2 || specs[0].Params.ThinkBudget != 2252 || !specs[1].Params.NoThink || specs[1].Params.ThinkBudget != 0 || note == "" {
		t.Fatalf("%v %+v %+v note %q", err, out, specs, note)
	}
	// A call that is not a chat answer (small limit) is left alone.
	specs, note = nil, ""
	withThinkBudget(infer, 4096, &note)(context.Background(), InferenceSpec{Params: InferenceParams{MaxTokens: 300}})
	if len(specs) != 1 || specs[0].Params.ThinkBudget != 0 {
		t.Fatalf("%+v", specs)
	}
}

func TestLlamaThinkBudgetStopsAndNoThinkIsSent(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(s string) { fmt.Fprintf(w, "data: %s\n\n", s); w.(http.Flusher).Flush() }
		if body["chat_template_kwargs"] != nil {
			send(`{"choices":[{"delta":{"content":"direct"}}]}`)
			send(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`)
		} else {
			for i := 0; i < 50 && r.Context().Err() == nil; i++ {
				send(`{"choices":[{"delta":{"reasoning_content":"hmm "}}]}`)
			}
		}
		send(`[DONE]`)
	}))
	defer srv.Close()
	var port int
	fmt.Sscanf(srv.URL, "http://127.0.0.1:%d", &port)
	l := newLlamaMgr(nil)
	l.cmd, l.ready, l.port, l.model = &exec.Cmd{}, true, port, "m"
	spec := InferenceSpec{Messages: []ChatMessage{{Role: "user", Content: "hi"}}, Params: InferenceParams{MaxTokens: 100, ThinkBudget: 10}}
	out, e := l.chat(context.Background(), spec, func(string) {})
	if e != nil || out == nil || out.FinishReason != "think_limit" || out.Text != "" {
		t.Fatalf("%v %+v", e, out)
	}
	spec.Params.ThinkBudget, spec.Params.NoThink = 0, true
	out, e = l.chat(context.Background(), spec, func(string) {})
	if e != nil || out.Text != "direct" || body["chat_template_kwargs"] == nil {
		t.Fatalf("%v %+v %v", e, out, body)
	}
}

func TestWithAutoContinueJoinsCutAnswers(t *testing.T) {
	var specs []InferenceSpec
	infer := func(_ context.Context, s InferenceSpec) (*InferenceOutput, error) {
		specs = append(specs, s)
		if len(specs) == 1 {
			return &InferenceOutput{Text: "one ", FinishReason: "length", OutputTokens: 10, Reasoning: "hm", ThinkSecs: 3}, nil
		}
		return &InferenceOutput{Text: "two", FinishReason: "stop", OutputTokens: 5}, nil
	}
	var note string
	spec := InferenceSpec{Messages: []ChatMessage{{Role: "user", Content: "q"}}, Params: InferenceParams{MaxTokens: 4096}}
	out, err := withAutoContinue(infer, 32768, 4096, &note)(context.Background(), spec)
	if err != nil || out.Text != "one two" || out.FinishReason != "stop" || out.OutputTokens != 15 || out.ThinkSecs != 3 || note == "" {
		t.Fatalf("%v %+v note %q", err, out, note)
	}
	m := specs[1].Messages
	if len(specs) != 2 || len(m) != 3 || m[1].Role != "assistant" || m[1].Content != "one " || m[1].Reasoning != "hm" || m[2].Role != "user" {
		t.Fatalf("continuation asked wrong: %+v", specs)
	}
}

func TestWithAutoContinueLimits(t *testing.T) {
	calls := 0
	always := func(_ context.Context, s InferenceSpec) (*InferenceOutput, error) {
		calls++
		return &InferenceOutput{Text: "x", FinishReason: "length", OutputTokens: 4096}, nil
	}
	var note string
	spec := InferenceSpec{Params: InferenceParams{MaxTokens: 4096}}
	// A model that never stops is asked a bounded number of times and the answer stays cut.
	out, _ := withAutoContinue(always, 1<<20, 4096, &note)(context.Background(), spec)
	if calls != 1+chatAutoContinues || out.FinishReason != "length" {
		t.Fatalf("calls %d out %+v", calls, out)
	}
	// No room left in the context: no continuation.
	calls = 0
	withAutoContinue(always, 6000, 4096, &note)(context.Background(), spec)
	if calls != 1 {
		t.Fatalf("calls %d with no room", calls)
	}
	// A smaller call (not a chat answer) is left alone, and so is a cancelled chat.
	calls = 0
	withAutoContinue(always, 1<<20, 4096, &note)(context.Background(), InferenceSpec{Params: InferenceParams{MaxTokens: 300}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	withAutoContinue(always, 1<<20, 4096, &note)(ctx, spec)
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
}
