package main

import (
	"strings"
	"testing"
)

func TestCleanPersonas(t *testing.T) {
	long := strings.Repeat("x", 5000)
	got := cleanPersonas([]ChatPersona{
		{Name: "  Code   reviewer ", Text: "Be strict."},
		{Name: "code reviewer", Text: "duplicate name"},
		{Name: "", Text: "no name"},
		{Name: "Empty", Text: "  "},
		{ID: "keep", Name: "Long", Text: long},
		{ID: "keep", Name: "Same id", Text: "x"},
	})
	if len(got) != 3 || got[0].Name != "Code reviewer" || got[0].ID == "" {
		t.Fatalf("got %+v", got)
	}
	if len([]rune(got[1].Text)) != maxChatInstructions || got[1].ID != "keep" || got[2].ID == "keep" {
		t.Fatalf("limits: %d %q %q", len([]rune(got[1].Text)), got[1].ID, got[2].ID)
	}
	var many []ChatPersona
	for i := 0; i < 50; i++ {
		many = append(many, ChatPersona{Name: strings.Repeat("n", i+1), Text: "t"})
	}
	if n := len(cleanPersonas(many)); n != maxPersonas {
		t.Fatalf("kept %d", n)
	}
}
