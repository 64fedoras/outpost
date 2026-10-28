package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OpenRouter (openrouter.ai) is a second cloud provider next to the Claude
// API: one key reaches hundreds of hosted models through an OpenAI-style chat
// API. It can be picked as the planner, so it does everything Claude does
// today (planning, writing subtasks the nodes could not finish, fix rounds).
// Spend uses the cost OpenRouter reports for each call, so the live meter and
// the per-task cap work the same way as for Claude.

// orModel is one entry of OpenRouter's model list. Prices are USD per token.
type orModel struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Ctx      int     `json:"context_length"`
	In       float64 `json:"in"`  // per million tokens, for the UI
	Out      float64 `json:"out"` // per million tokens, for the UI
	inTok    float64
	outTok   float64
	cacheTok float64
	maxOut   int
}

type orCatalog struct {
	mu      sync.Mutex
	models  map[string]*orModel
	order   []string
	fetched time.Time
	err     error
}

var openRouterModels = &orCatalog{}

func openRouterBaseURL() string {
	if u := os.Getenv("OUTPOST_OPENROUTER_URL"); u != "" { // for testing against a stub
		return strings.TrimRight(u, "/")
	}
	return "https://openrouter.ai/api"
}

func parsePrice(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < 0 { // "-1" marks a router whose price depends on the model it picks
		return 0
	}
	return v
}

// refresh downloads the model list at most once an hour (force skips the wait).
func (cat *orCatalog) refresh(ctx context.Context, key string, force bool) error {
	cat.mu.Lock()
	fresh := cat.models != nil && time.Since(cat.fetched) < time.Hour
	cat.mu.Unlock()
	if fresh && !force {
		return nil
	}
	hr, _ := http.NewRequestWithContext(ctx, "GET", openRouterBaseURL()+"/v1/models", nil)
	if key != "" {
		hr.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(hr)
	if err != nil {
		cat.setErr(err)
		return err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	resp.Body.Close()
	if resp.StatusCode != 200 {
		err := fmt.Errorf("OpenRouter model list: HTTP %d", resp.StatusCode)
		cat.setErr(err)
		return err
	}
	var out struct {
		Data []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Ctx     int    `json:"context_length"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
				CacheRead  string `json:"input_cache_read"`
			} `json:"pricing"`
			Top struct {
				MaxOut int `json:"max_completion_tokens"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		cat.setErr(err)
		return fmt.Errorf("OpenRouter model list: %v", err)
	}
	models := map[string]*orModel{}
	order := []string{}
	for _, d := range out.Data {
		if d.ID == "" {
			continue
		}
		m := &orModel{ID: d.ID, Name: d.Name, Ctx: d.Ctx, maxOut: d.Top.MaxOut,
			inTok: parsePrice(d.Pricing.Prompt), outTok: parsePrice(d.Pricing.Completion), cacheTok: parsePrice(d.Pricing.CacheRead)}
		if m.Name == "" {
			m.Name = d.ID
		}
		m.In, m.Out = m.inTok*1e6, m.outTok*1e6
		models[d.ID] = m
		order = append(order, d.ID)
	}
	sort.Strings(order)
	cat.mu.Lock()
	cat.models, cat.order, cat.fetched, cat.err = models, order, time.Now(), nil
	cat.mu.Unlock()
	return nil
}

func (cat *orCatalog) setErr(err error) {
	cat.mu.Lock()
	cat.err = err
	cat.mu.Unlock()
}

// get returns a copy of one model, or nil if the list has not got it.
func (cat *orCatalog) get(id string) *orModel {
	cat.mu.Lock()
	defer cat.mu.Unlock()
	if m := cat.models[id]; m != nil {
		cp := *m
		return &cp
	}
	return nil
}

func (cat *orCatalog) list() ([]orModel, error) {
	cat.mu.Lock()
	defer cat.mu.Unlock()
	out := make([]orModel, 0, len(cat.order))
	for _, id := range cat.order {
		out = append(out, *cat.models[id])
	}
	return out, cat.err
}

// openRouterEstimate gives an upper-bound guess for a call before it is made.
// Unknown prices (list not loaded yet) estimate as zero; the reported cost of
// the call still counts against the cap, so the next call pauses.
func openRouterEstimate(model string, inputChars, maxOut int) float64 {
	m := openRouterModels.get(model)
	if m == nil {
		return 0
	}
	return float64(inputChars/3)*m.inTok + float64(maxOut/3)*m.outTok
}

func openRouterLabel(model string) string {
	if m := openRouterModels.get(model); m != nil && m.Name != "" {
		return m.Name + " via OpenRouter"
	}
	if model == "" {
		return "OpenRouter"
	}
	return model + " via OpenRouter"
}

// callOpenRouter sends one chat completion (non-streaming), retrying rate
// limits and provider errors. Anthropic models get the system prompt cached
// the same way callClaude does it.
func callOpenRouter(ctx context.Context, key, model string, req llmRequest) (*llmResponse, error) {
	if key == "" {
		return nil, errors.New("no OpenRouter API key; add one in Settings")
	}
	if model == "" {
		return nil, errors.New("no OpenRouter model chosen; pick one in Settings")
	}
	var system any = req.System
	if strings.HasPrefix(model, "anthropic/") {
		system = []map[string]any{{"type": "text", "text": req.System, "cache_control": map[string]string{"type": "ephemeral"}}}
	}
	body := map[string]any{
		"model":      model,
		"max_tokens": req.MaxTokens,
		"messages":   []map[string]any{{"role": "system", "content": system}, {"role": "user", "content": req.User}},
		"usage":      map[string]bool{"include": true},
	}
	if m := openRouterModels.get(model); m != nil && m.maxOut > 0 && req.MaxTokens > m.maxOut {
		body["max_tokens"] = m.maxOut
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
		hr, _ := http.NewRequestWithContext(ctx, "POST", openRouterBaseURL()+"/v1/chat/completions", bytes.NewReader(b))
		hr.Header.Set("Authorization", "Bearer "+key)
		hr.Header.Set("Content-Type", "application/json")
		hr.Header.Set("HTTP-Referer", "https://github.com/64fedoras/outpost")
		hr.Header.Set("X-Title", "Outpost")
		resp, err := client.Do(hr)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
		} else {
			var raw []byte
			if resp.StatusCode == 200 && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
				raw = readOpenRouterStream(resp.Body, req.Live)
			} else {
				raw, _ = io.ReadAll(io.LimitReader(resp.Body, 32<<20))
			}
			resp.Body.Close()
			var out struct {
				Model   string `json:"model"`
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
				Usage *struct {
					In      int      `json:"prompt_tokens"`
					Out     int      `json:"completion_tokens"`
					Cost    *float64 `json:"cost"`
					Details struct {
						Cached int `json:"cached_tokens"`
					} `json:"prompt_tokens_details"`
				} `json:"usage"`
				Error *struct {
					Code    any    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(raw, &out)
			if resp.StatusCode == 200 && out.Error == nil && len(out.Choices) > 0 {
				r := &llmResponse{Text: out.Choices[0].Message.Content, Model: model, ElapsedSecond: time.Since(start).Seconds()}
				if u := out.Usage; u != nil {
					r.InTokens, r.OutTokens, r.CacheRead = u.In-u.Details.Cached, u.Out, u.Details.Cached
					if r.InTokens < 0 {
						r.InTokens = 0
					}
					if u.Cost != nil {
						r.CostUSD = *u.Cost
					} else if m := openRouterModels.get(model); m != nil {
						cache := m.cacheTok
						if cache == 0 {
							cache = m.inTok
						}
						r.CostUSD = float64(r.InTokens)*m.inTok + float64(r.CacheRead)*cache + float64(r.OutTokens)*m.outTok
					}
				}
				return r, nil
			}
			ce := &openRouterError{Status: resp.StatusCode}
			if out.Error != nil {
				ce.Message = out.Error.Message
				if n, ok := out.Error.Code.(float64); ok && resp.StatusCode == 200 {
					ce.Status = int(n)
				}
			}
			if ce.Message == "" {
				ce.Message = strings.TrimSpace(string(raw))
				if len(ce.Message) > 500 {
					ce.Message = ce.Message[:500]
				}
			}
			// 400 bad request, 401 bad key, 402 out of credits, 403 moderation, 404 no such model.
			if ce.Status != 429 && ce.Status < 500 && ce.Status != 200 {
				return nil, ce
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

type openRouterError struct {
	Status  int
	Message string
}

func (e *openRouterError) Error() string {
	return fmt.Sprintf("OpenRouter %d: %s", e.Status, e.Message)
}

// ---------- web UI ----------

func (c *Controller) openRouterKey() string {
	c.mu.Lock()
	enc := c.settings.OpenRouterKeyEnc
	c.mu.Unlock()
	k, _ := c.decrypt(enc)
	return k
}

// handleOpenRouterModels returns OpenRouter's model list with prices per
// million tokens. ?refresh=1 downloads it again.
func (c *Controller) handleOpenRouterModels(w http.ResponseWriter, r *http.Request) {
	err := openRouterModels.refresh(r.Context(), c.openRouterKey(), r.URL.Query().Get("refresh") == "1")
	list, _ := openRouterModels.list()
	if err != nil && len(list) == 0 {
		writeJSON(w, 502, map[string]string{"error": "could not load the OpenRouter model list: " + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"models": list})
}

// readOpenRouterStream reads a streamed chat completion, sending text to live,
// and returns the equivalent non-streamed JSON body so the caller parses one shape.
// A stream that ends without a finish reason yields an error body.
func readOpenRouterStream(body io.Reader, live *liveHandle) []byte {
	var text strings.Builder
	var usage json.RawMessage
	var finish string
	var errBody json.RawMessage
	_ = sseData(body, func(data string) bool {
		if data == "[DONE]" {
			return false
		}
		var ch struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage json.RawMessage `json:"usage"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ch) != nil {
			return true // keep-alive comments and the like
		}
		if len(ch.Error) > 0 && string(ch.Error) != "null" {
			errBody = ch.Error
			return false
		}
		for _, c := range ch.Choices {
			if c.Delta.Content != "" {
				text.WriteString(c.Delta.Content)
				live.Write(c.Delta.Content)
			}
			if c.FinishReason != nil {
				finish = *c.FinishReason
			}
		}
		if len(ch.Usage) > 0 && string(ch.Usage) != "null" {
			usage = ch.Usage
		}
		return true
	})
	if errBody != nil {
		b, _ := json.Marshal(map[string]any{"error": errBody})
		return b
	}
	if finish == "" {
		b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": 502, "message": "OpenRouter stream ended early"}})
		return b
	}
	out := map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": text.String()}, "finish_reason": finish}}}
	if usage != nil {
		out["usage"] = usage
	}
	b, _ := json.Marshal(out)
	return b
}
