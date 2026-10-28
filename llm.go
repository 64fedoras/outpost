package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// claudePrice is USD per million tokens. Source: platform.claude.com pricing,
// checked 2026-10-24. Haiku 5.5 costs more above 100k input tokens.
type claudePrice struct {
	Label                       string
	In, Out, CacheWrite, CacheR float64
	LongIn, LongOut             float64 // for prompts over 100k tokens (Haiku only); 0 = same
}

var claudePrices = map[string]claudePrice{
	"claude-sonnet-5-5": {Label: "Claude Sonnet 5.5", In: 2, Out: 10, CacheWrite: 2.5, CacheR: 0.10},
	"claude-opus-5-5":   {Label: "Claude Opus 5.5", In: 4, Out: 20, CacheWrite: 5, CacheR: 0.20},
	"claude-haiku-5-5":  {Label: "Claude Haiku 5.5", In: 0.10, Out: 0.50, CacheWrite: 0.125, CacheR: 0.01, LongIn: 0.50, LongOut: 2.50},
}

var claudeModelOrder = []string{"claude-sonnet-5-5", "claude-opus-5-5", "claude-haiku-5-5"}

type llmRequest struct {
	System      string
	User        string
	MaxTokens   int
	Temperature float64
	Label       string
	WebSearch   bool        // Claude only: allow the web search tool (priced per search)
	Sub         string      // subtask id, for the live view
	Live        *liveHandle // streamed text goes here (cloud planners)
	Images      []llmImage  // attached to the user turn (planner only)
}

type llmResponse struct {
	Text          string
	InTokens      int
	OutTokens     int
	CacheRead     int
	CacheWrite    int
	CostUSD       float64
	WebSearches   int
	Model         string
	TokensPerSec  float64
	ElapsedSecond float64
}

// usageCost prices one Claude response.
func usageCost(model string, in, out, cacheW, cacheR int) float64 {
	p := claudePrices[model]
	inP, outP := p.In, p.Out
	if p.LongIn > 0 && in+cacheW+cacheR > 100000 {
		inP, outP = p.LongIn, p.LongOut
	}
	return (float64(in)*inP + float64(out)*outP + float64(cacheW)*p.CacheWrite + float64(cacheR)*p.CacheR) / 1e6
}

// estimateCost gives an upper-bound guess for a call before it is made.
func estimateCost(model string, inputChars, maxOut int) float64 {
	return usageCost(model, inputChars/3, maxOut/3, 0, 0)
}

type claudeError struct {
	Status  int
	Message string
}

func (e *claudeError) Error() string { return fmt.Sprintf("Claude API %d: %s", e.Status, e.Message) }

func anthropicBaseURL() string {
	if u := os.Getenv("OUTPOST_ANTHROPIC_URL"); u != "" { // for testing against a stub
		return strings.TrimRight(u, "/")
	}
	return "https://api.anthropic.com"
}

// callClaude sends one Messages API request (non-streaming) with the system
// prompt cached, retrying overload and rate-limit errors.
func callClaude(ctx context.Context, key, model string, req llmRequest) (*llmResponse, error) {
	if key == "" {
		return nil, errors.New("no Claude API key; add one in Settings")
	}
	body := map[string]any{
		"model":      model,
		"max_tokens": req.MaxTokens,
		"system":     []map[string]any{{"type": "text", "text": req.System, "cache_control": map[string]string{"type": "ephemeral"}}},
		"messages":   []map[string]any{{"role": "user", "content": claudeUser(req)}},
	}
	if req.WebSearch {
		body["tools"] = []map[string]any{{"type": "web_search_20250305", "name": "web_search", "max_uses": webSearchUses}}
	}
	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}
	if req.Live != nil {
		body["stream"] = true
	}
	b, _ := json.Marshal(body)
	client := &http.Client{Timeout: 10 * time.Minute}
	start := time.Now()
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		req.Live.Reset()
		hr, _ := http.NewRequestWithContext(ctx, "POST", anthropicBaseURL()+"/v1/messages", bytes.NewReader(b))
		hr.Header.Set("x-api-key", key)
		hr.Header.Set("anthropic-version", "2023-06-01")
		hr.Header.Set("content-type", "application/json")
		resp, err := client.Do(hr)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
		} else {
			if resp.StatusCode == 200 && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
				r, serr := readClaudeStream(resp.Body, req.Live)
				resp.Body.Close()
				if serr == nil {
					r.Model, r.ElapsedSecond = model, time.Since(start).Seconds()
					r.CostUSD = usageCost(model, r.InTokens, r.OutTokens, r.CacheWrite, r.CacheRead)
					return r, nil
				}
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				lastErr = serr
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(2<<attempt) * time.Second):
				}
				continue
			}
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
			resp.Body.Close()
			if resp.StatusCode == 200 {
				var out struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
					Usage struct {
						In     int `json:"input_tokens"`
						Out    int `json:"output_tokens"`
						CacheW int `json:"cache_creation_input_tokens"`
						CacheR int `json:"cache_read_input_tokens"`
						Server struct {
							Searches int `json:"web_search_requests"`
						} `json:"server_tool_use"`
					} `json:"usage"`
				}
				if err := json.Unmarshal(raw, &out); err != nil {
					return nil, fmt.Errorf("Claude API: bad response: %v", err)
				}
				var text strings.Builder
				for _, c := range out.Content {
					if c.Type == "text" {
						text.WriteString(c.Text)
					}
				}
				r := &llmResponse{Text: text.String(), InTokens: out.Usage.In, OutTokens: out.Usage.Out,
					CacheRead: out.Usage.CacheR, CacheWrite: out.Usage.CacheW, Model: model,
					WebSearches: out.Usage.Server.Searches, ElapsedSecond: time.Since(start).Seconds()}
				r.CostUSD = usageCost(model, r.InTokens, r.OutTokens, r.CacheWrite, r.CacheRead) + float64(r.WebSearches)*webSearchUSD
				return r, nil
			}
			var e struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(raw, &e)
			ce := &claudeError{Status: resp.StatusCode, Message: e.Error.Message}
			if ce.Message == "" {
				ce.Message = strings.TrimSpace(string(raw))
			}
			if resp.StatusCode != 429 && resp.StatusCode != 529 && resp.StatusCode < 500 {
				return nil, ce // 400/401/403/404: retrying will not help
			}
			lastErr = ce
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(2<<attempt) * time.Second):
		}
	}
	return nil, lastErr
}

// ---------- file blocks exchanged with models ----------

// Models return files as:
//
//	<<<FILE path/to/file>>>
//	...content...
//	<<<END>>>
const fileFormatHelp = "Write each file in exactly this format, with nothing else between files:\n<<<FILE path/relative/to/project>>>\n(complete file content)\n<<<END>>>\nDo not wrap files in markdown code fences. Always output complete files, never diffs or placeholders like \"rest unchanged\"."

type fileBlock struct {
	Path    string
	Content string
}

func parseFileBlocks(text string) []fileBlock {
	var out []fileBlock
	rest := text
	for {
		i := strings.Index(rest, "<<<FILE ")
		if i < 0 {
			return out
		}
		rest = rest[i+len("<<<FILE "):]
		j := strings.Index(rest, ">>>")
		if j < 0 {
			return out
		}
		p := strings.TrimSpace(rest[:j])
		rest = rest[j+3:]
		rest = strings.TrimPrefix(strings.TrimPrefix(rest, "\r"), "\n")
		k := strings.Index(rest, "<<<END>>>")
		var content string
		if k < 0 { // truncated output: keep what is there only if it is the last block
			content, rest = rest, ""
		} else {
			content, rest = rest[:k], rest[k+len("<<<END>>>"):]
		}
		content = stripFence(content)
		if p != "" {
			out = append(out, fileBlock{Path: p, Content: content})
		}
		if k < 0 {
			return out
		}
	}
}

// stripFence removes a markdown fence that small models sometimes add anyway.
func stripFence(s string) string {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "```") && strings.HasSuffix(t, "```") && len(t) > 6 {
		if nl := strings.Index(t, "\n"); nl > 0 {
			t = strings.TrimSuffix(t[nl+1:], "```")
			return strings.TrimRight(t, " \t\r\n") + "\n"
		}
	}
	if !strings.HasSuffix(s, "\n") && s != "" {
		return s + "\n"
	}
	return s
}

// extractJSON returns the outermost JSON object in a model's answer.
func extractJSON(text string) string {
	i := strings.Index(text, "{")
	j := strings.LastIndex(text, "}")
	if i < 0 || j <= i {
		return ""
	}
	return text[i : j+1]
}

// sseData calls fn with the data payload of each server-sent event in r.
func sseData(r io.Reader, fn func(data string) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "data:") {
			if !fn(strings.TrimSpace(strings.TrimPrefix(line, "data:"))) {
				return nil
			}
		}
	}
	return sc.Err()
}

// readClaudeStream reads a streamed Messages response, sending text to live as it arrives.
func readClaudeStream(body io.Reader, live *liveHandle) (*llmResponse, error) {
	r := &llmResponse{}
	var text strings.Builder
	var streamErr error
	done := false
	err := sseData(body, func(data string) bool {
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			Message struct {
				Usage struct {
					In     int `json:"input_tokens"`
					CacheW int `json:"cache_creation_input_tokens"`
					CacheR int `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Usage struct {
				Out int `json:"output_tokens"`
			} `json:"usage"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return true
		}
		switch ev.Type {
		case "message_start":
			r.InTokens, r.CacheWrite, r.CacheRead = ev.Message.Usage.In, ev.Message.Usage.CacheW, ev.Message.Usage.CacheR
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				text.WriteString(ev.Delta.Text)
				live.Write(ev.Delta.Text)
			}
		case "message_delta":
			r.OutTokens = ev.Usage.Out
		case "message_stop":
			done = true
			return false
		case "error":
			streamErr = fmt.Errorf("Claude API stream: %s", ev.Error.Message)
			return false
		}
		return true
	})
	if streamErr != nil {
		return nil, streamErr
	}
	if err != nil {
		return nil, err
	}
	if !done {
		return nil, errors.New("Claude API stream ended early")
	}
	r.Text = text.String()
	return r, nil
}

// claudeUser is the user turn: plain text, or image blocks followed by the text.
func claudeUser(req llmRequest) any {
	if len(req.Images) == 0 {
		return req.User
	}
	blocks := make([]map[string]any, 0, len(req.Images)+1)
	for _, img := range req.Images {
		blocks = append(blocks, map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": img.Media, "data": img.B64}})
	}
	return append(blocks, map[string]any{"type": "text", "text": req.User})
}
