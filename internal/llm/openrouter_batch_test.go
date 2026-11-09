package llm

import "testing"

func TestOpenRouterBaseModel(t *testing.T) {
	for in, want := range map[string]string{
		"anthropic/claude-opus-5-5:batch": "anthropic/claude-opus-5-5",
		"anthropic/claude-opus-5-5":       "anthropic/claude-opus-5-5",
		"meta/llama:free":                 "meta/llama:free",
		"":                                "",
	} {
		got, changed := OpenRouterBaseModel(in)
		if got != want || changed != (got != in) {
			t.Errorf("%q: got %q changed=%v", in, got, changed)
		}
	}
}
