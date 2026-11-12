package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenRouterWebSearchPlugin(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"cost":0.01}}`))
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_OPENROUTER_URL", srv.URL)
	for _, search := range []bool{false, true} {
		got = nil
		if _, err := CallOpenRouter(context.Background(), "k", "anthropic/claude-opus-5-5", Request{System: "s", User: "u", MaxTokens: 10, WebSearch: search}); err != nil {
			t.Fatal(err)
		}
		_, has := got["plugins"]
		if has != search {
			t.Fatalf("search=%v but plugins present=%v", search, has)
		}
	}
}
