package main

// Shared core of the Anthropic Messages and Ollama APIs (anthropic_api.go,
// ollama_api.go): pick a node like model "auto" (or the node that has the
// named model), run one inference job and hand the streamed text to the
// caller. The OpenAI API (openai_api.go) does the same inline.

import (
	"context"
	"net/http"
	"strings"
)

type apiRun struct {
	node, model string // node id and the model it has loaded
	ctxTok      int
	release     func()
}

// apiAcquire reserves a free slot for model ("" or "auto" = the strongest
// free node). It returns an HTTP status with the error.
func (c *Controller) apiAcquire(ctx context.Context, model string) (*apiRun, int, error) {
	model = strings.TrimSpace(model)
	want := model
	if want == "auto" || want == "outpost" {
		want = ""
	}
	node, err := c.acquireModelNode(ctx, "auto", c.apiModelAlias(want), apiWait)
	if err != nil {
		code := 503
		if strings.Contains(err.Error(), "no ready node has") {
			code = 404
		}
		return nil, code, err
	}
	run := &apiRun{node: node, model: firstNonEmpty(want, "auto"), ctxTok: c.nodeCtx(node), release: func() { c.releaseNode(node) }}
	c.mu.Lock()
	if hb := c.lastHB[node]; hb != nil && hb.LoadedModel != "" {
		run.model = hb.LoadedModel
	}
	c.mu.Unlock()
	return run, 0, nil
}

// apiModelAlias maps names a client cannot change to "auto": Claude Code asks
// for claude-* models (its main and its small fast model) unless told otherwise,
// and Ollama clients often add ":latest".
func (c *Controller) apiModelAlias(m string) string {
	if strings.HasPrefix(m, "claude-") {
		return ""
	}
	m = strings.TrimSuffix(m, ":latest")
	return m
}

// infer runs the call on the reserved node; onDelta (may be nil) gets the
// streamed answer text, and reasoning text with reason=true.
func (c *Controller) apiInfer(ctx context.Context, run *apiRun, spec InferenceSpec, label string, onDelta func(text string, reason bool)) (*InferenceOutput, error) {
	if spec.Params.MaxTokens <= 0 || spec.Params.MaxTokens > run.ctxTok {
		spec.Params.MaxTokens = min(4096, run.ctxTok/2)
	}
	if onDelta == nil {
		return c.runInferenceJob(ctx, run.node, spec, label, chatTimeout)
	}
	key := "api_" + randHex(12)
	live := taskLive.begin(key, "", "API", c.nodeLabel(run.node), false)
	_, ch, cancel := taskLive.subscribe(key)
	defer cancel()
	type result struct {
		out *InferenceOutput
		err error
	}
	done := make(chan result, 1)
	ctx2, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		out, err := c.runInferenceJobLive(ctx2, run.node, spec, label, chatTimeout, live)
		live.End()
		done <- result{out, err}
	}()
	forward := func(ev liveEvent) {
		if ev.T == "delta" && ev.Text != "" {
			onDelta(ev.Text, ev.Reason)
		}
	}
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				ch = nil
				continue
			}
			forward(ev)
		case res := <-done:
			for n := len(ch); ch != nil && n > 0; n-- {
				forward(<-ch)
			}
			return res.out, res.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// apiCORS answers browser preflights and adds the CORS headers for origins
// listed in Settings (api_cors_origins). Requests from other origins get no
// CORS headers, so browsers keep blocking them as before.
func (c *Controller) apiCORS(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && c.corsAllowed(o) {
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Api-Key, Anthropic-Version, Anthropic-Beta, Anthropic-Dangerous-Direct-Browser-Access")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		h(w, r)
	}
}

func (c *Controller) corsAllowed(origin string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, o := range c.settings.APICORSOrigins {
		if strings.EqualFold(strings.TrimRight(o, "/"), strings.TrimRight(origin, "/")) {
			return true
		}
	}
	return false
}

// normCORSOrigins keeps http(s) origins only, without paths, at most 20.
func normCORSOrigins(in []string) []string {
	var out []string
	for _, o := range in {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if (strings.HasPrefix(o, "http://") || strings.HasPrefix(o, "https://")) && !strings.Contains(strings.SplitN(o, "://", 2)[1], "/") && len(out) < 20 {
			out = append(out, o)
		}
	}
	return out
}
