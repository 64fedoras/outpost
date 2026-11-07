package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// Cloud planners stream (the live view), so tool calls must survive that path too.
func TestStreamedToolCalls(t *testing.T) {
	claude := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":40}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me look."}}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_a","name":"read_file","input":{}}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\": \"a."}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"go\"}"}}`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_b","name":"list_files","input":{}}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":33}}`,
		`data: {"type":"message_stop"}`, ``,
	}, "\n\n")
	r, err := readClaudeStream(strings.NewReader(claude), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "Let me look." || len(r.Calls) != 2 || r.Calls[0].ID != "toolu_a" || r.Calls[0].Args != `{"path": "a.go"}` || r.Calls[1].Name != "list_files" || r.Calls[1].Args != "{}" {
		t.Fatalf("claude stream: %+v", r)
	}
	if r.InTokens != 100 || r.OutTokens != 33 || r.CacheRead != 40 {
		t.Fatalf("usage %+v", r)
	}

	or := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","content":"","tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"search","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pattern\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":4}}`,
		`data: [DONE]`, ``,
	}, "\n\n")
	var out struct {
		Choices []struct {
			Message struct {
				ToolCalls []WireToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(readOpenRouterStream(strings.NewReader(or), nil), &out); err != nil {
		t.Fatal(err)
	}
	calls := FromWireCalls(out.Choices[0].Message.ToolCalls)
	if len(calls) != 1 || calls[0].ID != "c1" || calls[0].Name != "search" || calls[0].Args != `{"pattern":"x"}` {
		t.Fatalf("openrouter stream: %+v", calls)
	}
}
