package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSearchTasksAndChats(t *testing.T) {
	now := time.Now()
	c := &Controller{tasks: map[string]*Task{
		"a": {ID: "a", Title: "Port scanner", Prompt: "Write a port scanner in Go", Created: now.Add(-3 * time.Hour)},
		"b": {ID: "b", Title: "Recipe API", Prompt: "A small REST API", Created: now.Add(-2 * time.Hour),
			Messages: []TaskMessage{{Role: "user", Text: "Please also add pagination to the recipe list endpoint, with a page size of twenty.", Time: now.Add(-time.Hour)}},
			Plan:     &Plan{Subtasks: []PlanSubtask{{Title: "Handlers", Files: []string{"server/handlers.go"}}}}},
	}, chats: map[string]*Chat{
		"c1": {ID: "c1", Title: "Go generics", Updated: now, Messages: []ChatMsg{{Role: "user", Text: "How do I add pagination with generics?"}}},
	}}

	hits := c.search("PAGINATION")
	if len(hits) != 2 || hits[0].Kind != "chat" || hits[1].ID != "b" || hits[1].Where != "message" {
		t.Fatalf("pagination: %+v", hits)
	}
	if !strings.Contains(hits[1].Snippet, "pagination to the recipe") {
		t.Fatalf("snippet %q", hits[1].Snippet)
	}
	// Every word must match, across fields.
	if h := c.search("recipe handlers.go"); len(h) != 1 || h[0].ID != "b" || h[0].Where != "title" {
		t.Fatalf("multi-word: %+v", h)
	}
	if h := c.search("scanner rust"); len(h) != 0 {
		t.Fatalf("should not match: %+v", h)
	}
	if h := c.search("   "); len(h) != 0 {
		t.Fatalf("empty query: %+v", h)
	}
	if h := c.search(`"port scanner"`); len(h) != 1 { // quotes are ignored
		t.Fatalf("quoted: %+v", h)
	}

	rr := httptest.NewRecorder()
	c.handleSearch(rr, httptest.NewRequest("GET", "/api/search?q=scanner", nil))
	var out []searchHit
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || len(out) != 1 || out[0].ID != "a" {
		t.Fatalf("handler: %s", rr.Body)
	}
}

func TestSnippetAround(t *testing.T) {
	long := strings.Repeat("alpha beta ", 30) + "NEEDLE" + strings.Repeat(" gamma delta", 30)
	s := snippetAround(long, strings.ToLower(long), strings.Index(long, "NEEDLE"), 6)
	if !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") || !strings.Contains(s, "NEEDLE") || len(s) > 2*searchSnippet+20 {
		t.Fatalf("snippet %q", s)
	}
	if s := snippetAround("short text", "short text", 0, 5); s != "short text" {
		t.Fatalf("short %q", s)
	}
}
