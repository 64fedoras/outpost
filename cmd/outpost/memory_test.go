package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMemoryCRUDAndPrompt(t *testing.T) {
	c := chatController(t)
	send := func(method, path, id, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if id != "" {
			r.SetPathValue("id", id)
		}
		switch {
		case method == "GET":
			c.handleListMemory(w, r)
		case method == "DELETE":
			c.handleDeleteMemory(w, r)
		case id != "":
			c.handleEditMemory(w, r)
		case strings.HasSuffix(path, "suggestion"):
			c.handleMemorySuggestion(w, r)
		default:
			c.handleAddMemory(w, r)
		}
		return w
	}
	if w := send("POST", "/api/memory", "", `{"text":"   "}`); w.Code != 400 {
		t.Fatalf("empty: %d", w.Code)
	}
	w := send("POST", "/api/memory", "", `{"text":"  Prefers   Go and plain JavaScript. "}`)
	var m Memory
	json.Unmarshal(w.Body.Bytes(), &m)
	if w.Code != 200 || m.Text != "Prefers Go and plain JavaScript." || m.Source != "added by hand" {
		t.Fatalf("add %d %s", w.Code, w.Body)
	}
	if w := send("POST", "/api/memory", "", `{"text":"x","project":"nope"}`); w.Code != 400 {
		t.Fatalf("unknown project: %d", w.Code)
	}
	if !strings.Contains(c.chatSystemPrompt(""), "- Prefers Go and plain JavaScript.") {
		t.Fatalf("chat prompt lacks memory: %s", c.chatSystemPrompt(""))
	}
	c.settings.MemoryOff = true
	if strings.Contains(c.chatSystemPrompt(""), "Prefers Go") {
		t.Fatal("memory off still in prompt")
	}
	c.settings.MemoryOff = false
	if w := send("POST", "/api/memory/"+m.ID, m.ID, `{"text":"Prefers Go."}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"Prefers Go."`) {
		t.Fatalf("edit %d %s", w.Code, w.Body)
	}
	// Saved to disk: a fresh load sees it.
	memories.done = false
	if got := c.mem().forScope(""); len(got) != 1 || got[0] != "Prefers Go." {
		t.Fatalf("reload %v", got)
	}
	// A suggestion under a chat answer: Remember saves it and clears the card.
	ch := &Chat{ID: "chat_m", Title: "Dates", dir: c.chatsDir(), Messages: []ChatMsg{{ID: "a1", Role: "assistant", Text: "ok", Suggest: "Uses Gitea for CI."}}}
	c.chats[ch.ID] = ch
	if w := send("POST", "/api/memory/suggestion", "", `{"chat":"chat_m","msg":"a1","text":"Uses Gitea for CI mirrors."}`); w.Code != 200 {
		t.Fatalf("remember %d %s", w.Code, w.Body)
	}
	if ch.Messages[0].Suggest != "" || len(c.mem().all()) != 2 || c.mem().all()[1].Source != `from "Dates"` {
		t.Fatalf("after remember: %+v %+v", ch.Messages[0], c.mem().all())
	}
	if w := send("GET", "/api/memory", "", ""); !strings.Contains(w.Body.String(), `"tokens":`) {
		t.Fatalf("list %s", w.Body)
	}
	if w := send("DELETE", "/api/memory/"+m.ID, m.ID, ""); w.Code != 200 || len(c.mem().all()) != 1 {
		t.Fatalf("forget %d", w.Code)
	}
}

func TestSuggestMemory(t *testing.T) {
	reply := ""
	var got []InferenceSpec
	infer := func(ctx context.Context, spec InferenceSpec) (*InferenceOutput, error) {
		got = append(got, spec)
		return &InferenceOutput{Text: reply}, nil
	}
	for _, tc := range []struct{ reply, want string }{
		{"NONE", ""}, {" none.", ""}, {"\"Uses Go 1.26 on every machine.\"\nBecause they said so.", "Uses Go 1.26 on every machine."},
		{"Prefers Go.", ""}, // already known
		{"ok", ""},
	} {
		reply = tc.reply
		if s := suggestMemory(context.Background(), infer, []string{"I use Go 1.26 everywhere"}, []string{"Prefers Go."}); s != tc.want {
			t.Errorf("%q: got %q want %q", tc.reply, s, tc.want)
		}
	}
	if !strings.Contains(got[0].Messages[1].Content, "- Prefers Go.") || !strings.Contains(got[0].Messages[1].Content, "I use Go 1.26") {
		t.Fatalf("prompt %s", got[0].Messages[1].Content)
	}
	if suggestMemory(context.Background(), infer, nil, nil) != "" || len(got) != 5 {
		t.Fatal("no owner text must not call the model")
	}
}
