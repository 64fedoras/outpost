package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenRouterCall(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-or-test" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"code":401,"message":"No auth"}}`))
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			w.Write([]byte(`{"data":[{"id":"vendor/m1","name":"M One","context_length":32000,"pricing":{"prompt":"0.000002","completion":"0.00001"},"top_provider":{"max_completion_tokens":4000}},{"id":"openrouter/auto","name":"Auto","pricing":{"prompt":"-1","completion":"-1"}}]}`))
		case "/v1/chat/completions":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.Write([]byte(`{"model":"vendor/m1","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":1000,"completion_tokens":200,"cost":0.0042,"prompt_tokens_details":{"cached_tokens":100}}}`))
		}
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_OPENROUTER_URL", srv.URL)
	ctx := context.Background()

	cat := &orCatalog{}
	if err := cat.refresh(ctx, "sk-or-test", true); err != nil {
		t.Fatal(err)
	}
	openRouterModels = cat
	m := cat.get("vendor/m1")
	if m == nil || m.In != 2 || m.Out != 10 || m.Ctx != 32000 {
		t.Fatalf("model parsed wrong: %+v", m)
	}
	if a := cat.get("openrouter/auto"); a == nil || a.In != 0 {
		t.Errorf("router price should be 0: %+v", a)
	}

	r, err := callOpenRouter(ctx, "sk-or-test", "vendor/m1", llmRequest{System: "s", User: "u", MaxTokens: 16000})
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "hi" || r.CostUSD != 0.0042 || r.InTokens != 900 || r.CacheRead != 100 || r.OutTokens != 200 {
		t.Errorf("response: %+v", r)
	}
	if gotBody["max_tokens"].(float64) != 4000 {
		t.Errorf("max_tokens not clamped to the model limit: %v", gotBody["max_tokens"])
	}
	if u, _ := gotBody["usage"].(map[string]any); u == nil || u["include"] != true {
		t.Errorf("usage accounting not requested: %v", gotBody["usage"])
	}

	_, err = callOpenRouter(ctx, "wrong", "vendor/m1", llmRequest{System: "s", User: "u", MaxTokens: 10})
	if err == nil || !strings.Contains(err.Error(), "OpenRouter 401") {
		t.Errorf("want a 401 error without retries, got %v", err)
	}
	if _, err := callOpenRouter(ctx, "", "vendor/m1", llmRequest{}); err == nil {
		t.Error("missing key should fail")
	}
	if e := openRouterEstimate("vendor/m1", 3000, 3000); e <= 0 {
		t.Errorf("estimate %v", e)
	}
}
