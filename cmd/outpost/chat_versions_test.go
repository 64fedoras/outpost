package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChatVersions(t *testing.T) {
	c := chatController(t)
	var specs []InferenceSpec
	var mu sync.Mutex
	fakeInferNode(t, c, "n", &specs, &mu)
	m := http.NewServeMux() // the raw handlers, without the sign-in check
	m.HandleFunc("POST /api/chats", c.handleNewChat)
	m.HandleFunc("POST /api/chats/{id}/messages", c.handleChatSend)
	m.HandleFunc("POST /api/chats/{id}/edit", c.handleChatEdit)
	m.HandleFunc("POST /api/chats/{id}/regenerate", c.handleChatRegenerate)
	m.HandleFunc("POST /api/chats/{id}/version", c.handleChatVersion)
	m.HandleFunc("POST /api/chats/{id}/branch", c.handleChatBranch)
	m.HandleFunc("POST /api/chats/{id}/instructions", c.handleChatInstructions)
	do := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		return rec
	}
	var created struct{ ID string }
	json.Unmarshal(do("/api/chats", `{"text":"one","instructions":"Answer in Go."}`).Body.Bytes(), &created)
	id := created.ID
	wait := func() *Chat {
		for i := 0; i < 300; i++ {
			ch := c.getChat(id)
			ch.mu.Lock()
			busy := ch.Busy
			ch.mu.Unlock()
			if busy == "" {
				return ch
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("no answer")
		return nil
	}
	texts := func(ch *Chat) string {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		var s []string
		for _, m := range ch.Messages {
			s = append(s, m.Text)
		}
		return strings.Join(s, "|")
	}
	ch := wait()
	mu.Lock()
	if sys := specs[0].Messages[0].Content; !strings.Contains(sys, "Answer in Go.") {
		t.Fatalf("instructions not in system prompt: %q", sys)
	}
	mu.Unlock()
	do("/api/chats/"+id+"/messages", `{"text":"two"}`)
	ch = wait()
	if got := texts(ch); got != "one|echo: one|two|echo: two" {
		t.Fatal(got)
	}
	// Edit the first message: the tail becomes version 0, the new one version 1.
	first := ch.Messages[0].ID
	do("/api/chats/"+id+"/edit", `{"msg_id":"`+first+`","text":"uno"}`)
	ch = wait()
	if got := texts(ch); got != "uno|echo: uno" || ch.Messages[0].Ver != 1 || len(ch.Messages[0].Alts) != 1 {
		t.Fatalf("after edit %s ver %d", got, ch.Messages[0].Ver)
	}
	// Regenerate the answer: two versions of the answer.
	do("/api/chats/"+id+"/regenerate", `{"msg_id":"`+ch.Messages[1].ID+`"}`)
	ch = wait()
	if len(ch.Messages) != 2 || ch.Messages[1].Ver != 1 || len(ch.Messages[1].Alts) != 1 {
		t.Fatalf("after regenerate %+v", ch.Messages)
	}
	// Back to the original first version brings the whole old tail back.
	do("/api/chats/"+id+"/version", `{"msg_id":"`+ch.Messages[0].ID+`","ver":0}`)
	ch = wait()
	if got := texts(ch); got != "one|echo: one|two|echo: two" || ch.Messages[0].Ver != 0 || len(ch.Messages[0].Alts) != 1 {
		t.Fatalf("after switch %s", got)
	}
	// And forward again keeps the regenerated answer's versions.
	do("/api/chats/"+id+"/version", `{"msg_id":"`+ch.Messages[0].ID+`","ver":1}`)
	ch = wait()
	if got := texts(ch); got != "uno|echo: uno" || len(ch.Messages[1].Alts) != 1 {
		t.Fatalf("after switch back %s", got)
	}
	// Branch from the first message.
	var br struct{ ID string }
	json.Unmarshal(do("/api/chats/"+id+"/branch", `{"msg_id":"`+ch.Messages[0].ID+`"}`).Body.Bytes(), &br)
	nb := c.getChat(br.ID)
	if nb == nil || len(nb.Messages) != 1 || nb.Messages[0].Text != "uno" || len(nb.Messages[0].Alts) != 0 || nb.Instructions != "Answer in Go." {
		t.Fatalf("branch %+v", nb)
	}
	// Instructions change, and become the default for new chats.
	do("/api/chats/"+id+"/instructions", `{"instructions":"Be brief.","default":true}`)
	if c.getChat(id).Instructions != "Be brief." || c.settings.ChatInstructions != "Be brief." {
		t.Fatal("instructions not saved")
	}
	json.Unmarshal(do("/api/chats", `{"text":"x"}`).Body.Bytes(), &created)
	if c.getChat(created.ID).Instructions != "Be brief." {
		t.Fatal("default instructions not used")
	}
	id = created.ID
	wait()
}

func TestCutTailCapsVersions(t *testing.T) {
	ch := &Chat{Messages: []ChatMsg{{ID: "u", Role: "user", Text: "x"}}}
	for i := 0; i < maxChatVersions+5; i++ {
		all := ch.cutTail(0) // every edit of the only message adds a version
		if len(all) > maxChatVersions-1 {
			t.Fatalf("kept %d versions", len(all))
		}
		ch.Messages = []ChatMsg{{ID: "u", Role: "user", Alts: all, Ver: len(all)}}
	}
}
