package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func tinyPNG(t *testing.T) string {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}

func TestChatScreenshots(t *testing.T) {
	c := chatController(t)
	var specs []InferenceSpec
	var mu sync.Mutex
	fakeInferNode(t, c, "n", &specs, &mu)
	m := mux2(c)
	m.HandleFunc("GET /api/chats/{id}/image", c.handleChatImage)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	body, _ := json.Marshal(map[string]any{"text": "", "images": []map[string]string{{"name": "shot.png", "data": tinyPNG(t)}}})

	// The loaded model "m" reads text only: refused up front, nothing saved.
	if rec := do("POST", "/api/chats", string(body)); rec.Code != 400 || !strings.Contains(rec.Body.String(), "Vision model") {
		t.Fatalf("text-only model: %d %s", rec.Code, rec.Body)
	}
	if len(c.chats) != 0 {
		t.Fatal("a refused message made a chat")
	}
	// A bad file is refused too.
	bad, _ := json.Marshal(map[string]any{"text": "x", "images": []map[string]string{{"name": "a.txt", "data": base64.StdEncoding.EncodeToString([]byte("hello"))}}})
	c.models["m"] = &ModelRec{HFFiles: []string{"m-Q4_K_M.gguf", "mmproj-F16.gguf"}}
	if rec := do("POST", "/api/chats", string(bad)); rec.Code != 400 {
		t.Fatalf("text file accepted as image: %d", rec.Code)
	}

	// With a vision model: saved with the chat, sent to the node as a data URI.
	rec := do("POST", "/api/chats", string(body))
	if rec.Code != 200 {
		t.Fatalf("new chat %d %s", rec.Code, rec.Body)
	}
	var created struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &created)
	var ch *Chat
	for i := 0; i < 200; i++ {
		ch = c.getChat(created.ID)
		ch.mu.Lock()
		done := ch.Busy == "" && len(ch.Messages) == 2
		ch.mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ch.mu.Lock()
	user := ch.Messages[0]
	ch.mu.Unlock()
	if len(user.Images) != 1 || ch.Title != "Image" {
		t.Fatalf("user message %+v title %q", user, ch.Title)
	}
	mu.Lock()
	sent := specs[len(specs)-1].Messages
	mu.Unlock()
	last := sent[len(sent)-1]
	if len(last.Images) != 1 || !strings.HasPrefix(last.Images[0], "data:image/png;base64,") || last.Content != "Describe this image." {
		t.Fatalf("sent %+v", last)
	}
	img := do("GET", "/api/chats/"+created.ID+"/image?name="+user.Images[0], "")
	if img.Code != 200 || img.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("serve image %d %s", img.Code, img.Header())
	}
	if rec := do("GET", "/api/chats/"+created.ID+"/image?name=../"+created.ID+".json", ""); rec.Code != 404 {
		t.Fatalf("path escape served: %d", rec.Code)
	}
	// Reloaded: still listed; deleted: the images go too.
	c2 := &Controller{dataDir: c.dataDir}
	c2.loadChats()
	if got := c2.getChat(created.ID); got == nil || len(got.Messages[0].Images) != 1 {
		t.Fatal("images not kept with the saved chat")
	}
	dir := ch.imagesDir()
	if rec := do("DELETE", "/api/chats/"+created.ID, ""); rec.Code != 200 {
		t.Fatal("delete")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("images left after delete: %v", err)
	}
}

func TestChatHistoryKeepsNewestImages(t *testing.T) {
	msgs := []ChatMsg{
		{Role: "user", Text: "a", Images: []string{"1", "2"}},
		{Role: "assistant", Text: "ok"},
		{Role: "user", Text: "b", Images: []string{"3"}},
		{Role: "assistant", Error: "failed"},
		{Role: "user", Text: "c", Images: []string{"4"}},
	}
	h := chatHistoryWith(msgs, func(n string) string { return "data:" + n })
	got := [][]string{}
	for _, m := range h[1:] {
		got = append(got, m.Images)
	}
	// Three newest: "2", "3", "4"; "1" is left out of the call.
	if len(h) != 5 || len(got[0]) != 1 || got[0][0] != "data:2" || got[2][0] != "data:3" || got[3][0] != "data:4" {
		t.Fatalf("history %+v", h)
	}
	if plain := chatHistory(msgs); len(plain[1].Images) != 0 {
		t.Fatal("chatHistory without a loader attached images")
	}
}
