package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ImageTokenChars is a rough cost estimate per image, in prompt characters.
const ImageTokenChars = 4500

type Tool struct {
	Name        string
	Description string
	Params      map[string]any // JSON schema
}

type ToolCall struct {
	ID   string
	Name string
	Args string // JSON object as text
}

// Msg is one turn of the loop. Role is user, assistant or tool.
type Msg struct {
	Role   string
	Text   string
	Calls  []ToolCall // assistant: the tools it asked for
	CallID string     // tool: which call this answers
}

// Image is an image on its way to a model.
type Image struct {
	Media string // image/png, image/jpeg, image/webp or image/gif
	B64   string
}

func (i Image) DataURI() string { return "data:" + i.Media + ";base64," + i.B64 }

// claudeMessages converts neutral messages to Messages API content blocks.
// Consecutive tool results share one user turn, as the API requires. The last
// block is marked for caching so a long loop does not pay for the whole
// conversation again on every step.
func claudeMessages(msgs []Msg) []map[string]any {
	var out []map[string]any
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			var blocks []map[string]any
			if strings.TrimSpace(m.Text) != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Text})
			}
			for _, c := range m.Calls {
				var in json.RawMessage = json.RawMessage(c.Args)
				if !json.Valid(in) {
					in = json.RawMessage("{}")
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": in})
			}
			out = append(out, map[string]any{"role": "assistant", "content": blocks})
		case "tool":
			blk := map[string]any{"type": "tool_result", "tool_use_id": m.CallID, "content": m.Text}
			if n := len(out); n > 0 && out[n-1]["role"] == "user" {
				if prev, ok := out[n-1]["content"].([]map[string]any); ok && len(prev) > 0 && prev[0]["type"] == "tool_result" {
					out[n-1]["content"] = append(prev, blk)
					continue
				}
			}
			out = append(out, map[string]any{"role": "user", "content": []map[string]any{blk}})
		default:
			out = append(out, map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": m.Text}}})
		}
	}
	if n := len(out); n > 0 {
		if blocks, ok := out[n-1]["content"].([]map[string]any); ok && len(blocks) > 0 {
			blocks[len(blocks)-1]["cache_control"] = map[string]string{"type": "ephemeral"}
		}
	}
	return out
}

func claudeTools(tools []Tool) []map[string]any {
	var out []map[string]any
	for _, t := range tools {
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Params})
	}
	return out
}

// size is the request's input length in characters, for cost estimates.
func (r Request) Size() int {
	n := len(r.System) + len(r.User) + ImageTokenChars*len(r.Images)
	for _, m := range r.Msgs {
		n += len(m.Text)
		for _, c := range m.Calls {
			n += len(c.Args) + len(c.Name)
		}
	}
	return n
}

// Sink receives text as a cloud model streams it. The controller's live view
// implements it; Reset clears the text when a failed request is retried.
type Sink interface {
	Write(text string)
	Reset()
}

// Web search pricing: one Claude web search costs $10 per 1,000, and Claude
// may make a few in one planning call.
const (
	WebSearchUSD  = 0.01
	WebSearchUses = 3
)

// ToolDef, WireToolCall are the OpenAI shapes used between the controller, the
// node and llama-server.
type ToolDef struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type WireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func ToolDefs(tools []Tool) []ToolDef {
	out := make([]ToolDef, len(tools))
	for i, t := range tools {
		out[i].Type = "function"
		out[i].Function.Name, out[i].Function.Description, out[i].Function.Parameters = t.Name, t.Description, t.Params
	}
	return out
}

func WireCalls(calls []ToolCall) []WireToolCall {
	out := make([]WireToolCall, len(calls))
	for i, c := range calls {
		out[i].ID, out[i].Type = c.ID, "function"
		out[i].Function.Name, out[i].Function.Arguments = c.Name, c.Args
	}
	return out
}

func FromWireCalls(in []WireToolCall) []ToolCall {
	var out []ToolCall
	for i, w := range in {
		id := w.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		args := strings.TrimSpace(w.Function.Arguments)
		if args == "" {
			args = "{}"
		}
		out = append(out, ToolCall{ID: id, Name: w.Function.Name, Args: args})
	}
	return out
}

// openAIMessages converts neutral messages to the OpenAI chat shape.
func openAIMessages(msgs []Msg) []map[string]any {
	var out []map[string]any
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			msg := map[string]any{"role": "assistant", "content": m.Text}
			if len(m.Calls) > 0 {
				msg["tool_calls"] = WireCalls(m.Calls)
			}
			out = append(out, msg)
		case "tool":
			out = append(out, map[string]any{"role": "tool", "content": m.Text, "tool_call_id": m.CallID})
		default:
			out = append(out, map[string]any{"role": "user", "content": m.Text})
		}
	}
	return out
}

type discardSink struct{}

func (discardSink) Write(string) {}
func (discardSink) Reset()       {}

// orDiscard makes a nil Sink safe to call.
func orDiscard(s Sink) Sink {
	if s == nil {
		return discardSink{}
	}
	return s
}
