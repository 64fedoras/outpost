package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPNG(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestDecodeImages(t *testing.T) {
	good := testPNG(t)
	imgs, err := decodeImages([]imageUpload{{Name: "a.png", Data: good}, {Name: "b.png", Data: "data:image/png;base64," + good}})
	if err != nil || len(imgs) != 2 || imgs[0].Media != "image/png" {
		t.Fatalf("good images: %+v %v", imgs, err)
	}
	notImage := base64.StdEncoding.EncodeToString([]byte("just some text, not a picture"))
	if _, err := decodeImages([]imageUpload{{Name: "x.png", Data: notImage}}); err == nil || !strings.Contains(err.Error(), "only PNG") {
		t.Errorf("text posing as an image: %v", err)
	}
	if _, err := decodeImages([]imageUpload{{Name: "x.png", Data: "%%%"}}); err == nil {
		t.Error("bad base64 accepted")
	}
	many := make([]imageUpload, maxTaskImages+1)
	if _, err := decodeImages(many); err == nil {
		t.Error("too many images accepted")
	}
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, maxImageBytes)...)
	if _, err := decodeImages([]imageUpload{{Name: "big.png", Data: base64.StdEncoding.EncodeToString(big)}}); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("oversized image: %v", err)
	}
}

func TestSaveAndReloadRoundImages(t *testing.T) {
	task := &Task{ID: "t1", dir: t.TempDir(), Round: 2}
	imgs, _ := decodeImages([]imageUpload{{Name: "a.png", Data: testPNG(t)}})
	names, err := task.saveImages(imgs)
	if err != nil || len(names) != 1 || !strings.HasSuffix(names[0], ".png") {
		t.Fatalf("save: %v %v", names, err)
	}
	if _, err := os.Stat(filepath.Join(task.dir, "images", names[0])); err != nil {
		t.Fatal(err)
	}
	task.Messages = []TaskMessage{{Role: "user", Round: 1, Images: []string{"old.png"}}, {Role: "user", Round: 2, Images: names}}
	got := task.roundImages()
	if len(got) != 1 || got[0].B64 != imgs[0].B64 {
		t.Fatalf("round images: %+v", got)
	}
}

func TestClaudeSendsImageBlocks(t *testing.T) {
	var body struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_ANTHROPIC_URL", srv.URL)
	img := llmImage{Media: "image/png", B64: testPNG(t)}
	if _, err := callClaude(context.Background(), "k", "claude-sonnet-5-5", llmRequest{System: "s", User: "look", MaxTokens: 100, Images: []llmImage{img}}); err != nil {
		t.Fatal(err)
	}
	c := body.Messages[0].Content
	if len(c) != 2 || c[0]["type"] != "image" || c[1]["type"] != "text" || c[1]["text"] != "look" {
		t.Fatalf("content blocks: %+v", c)
	}
	src := c[0]["source"].(map[string]any)
	if src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] != img.B64 {
		t.Errorf("image source: %+v", src)
	}
}

func TestOpenRouterImagesAndVisionFlag(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Write([]byte(`{"data":[{"id":"v/eyes","name":"Eyes","architecture":{"input_modalities":["text","image"]},"pricing":{"prompt":"0","completion":"0"}},{"id":"v/blind","name":"Blind","architecture":{"input_modalities":["text"]},"pricing":{"prompt":"0","completion":"0"}}]}`))
		default:
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
		}
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_OPENROUTER_URL", srv.URL)
	cat := &orCatalog{}
	if err := cat.refresh(context.Background(), "k", true); err != nil {
		t.Fatal(err)
	}
	openRouterModels = cat
	if !cat.get("v/eyes").Vision || cat.get("v/blind").Vision {
		t.Fatal("vision flag not parsed from input_modalities")
	}
	c := &Controller{}
	if err := c.checkPlannerVision("openrouter", "v/eyes"); err != nil {
		t.Errorf("vision model refused: %v", err)
	}
	if err := c.checkPlannerVision("openrouter", "v/blind"); err == nil || !strings.Contains(err.Error(), "does not accept images") {
		t.Errorf("text-only model: %v", err)
	}
	img := llmImage{Media: "image/jpeg", B64: "QUJD"}
	if _, err := callOpenRouter(context.Background(), "k", "v/eyes", llmRequest{System: "s", User: "look", MaxTokens: 10, Images: []llmImage{img}}); err != nil {
		t.Fatal(err)
	}
	parts := got["messages"].([]any)[1].(map[string]any)["content"].([]any)
	url := parts[1].(map[string]any)["image_url"].(map[string]any)["url"]
	if len(parts) != 2 || url != "data:image/jpeg;base64,QUJD" {
		t.Errorf("parts: %+v", parts)
	}
}

func TestLocalPlannerNeedsVisionModel(t *testing.T) {
	c := &Controller{
		lastHB: map[string]*Heartbeat{"text": {LoadedModel: "coder"}, "eyes": {LoadedModel: "qwen3.6"}, "none": {}},
		models: map[string]*ModelRec{
			"coder":   {HFFiles: []string{"coder-Q4_K_M.gguf"}},
			"qwen3.6": {HFFiles: []string{"Qwen3.6-Q4_K_M.gguf", "mmproj-F16.gguf"}},
		},
	}
	if err := c.checkPlannerVision("node:text", ""); err != errNoVisionLocal {
		t.Errorf("text model: %v", err)
	}
	if err := c.checkPlannerVision("node:none", ""); err != errNoVisionLocal {
		t.Errorf("no model: %v", err)
	}
	if err := c.checkPlannerVision("node:eyes", ""); err != nil {
		t.Errorf("vision model: %v", err)
	}
	if err := c.checkPlannerVision("claude", ""); err != nil {
		t.Errorf("claude: %v", err)
	}
	// nodeCall must not send images to a node that cannot read them.
	_, err := c.nodeCall(context.Background(), &Task{}, "text", llmRequest{Images: []llmImage{{Media: "image/png", B64: "QQ=="}}}, false)
	if err != errNoVisionLocal {
		t.Errorf("nodeCall: %v", err)
	}
}

func TestChatPayload(t *testing.T) {
	out, _ := json.Marshal(chatPayload([]ChatMessage{{Role: "system", Content: "s"}, {Role: "user", Content: "u", Images: []string{"data:image/png;base64,QQ=="}}}))
	want := `[{"content":"s","role":"system"},{"content":[{"text":"u","type":"text"},{"image_url":{"url":"data:image/png;base64,QQ=="},"type":"image_url"}],"role":"user"}]`
	if string(out) != want {
		t.Errorf("payload:\n%s\nwant\n%s", out, want)
	}
}

func TestCatalogVisionModels(t *testing.T) {
	n := 0
	for _, ce := range modelCatalog {
		if ce.MMProj == "" {
			continue
		}
		n++
		if !isMMProj(ce.MMProj) {
			t.Errorf("%s: %q is not an mmproj name", ce.ID, ce.MMProj)
		}
		for _, f := range ce.Files {
			if isMMProj(f) {
				t.Errorf("%s: mmproj listed among model files", ce.ID)
			}
		}
	}
	if n != 3 {
		t.Errorf("want Qwen3.6 35B, Qwen3.6 27B and Gemma 4 wired for vision, got %d entries", n)
	}
	m := &ModelRec{HFFiles: []string{"a.gguf", "mmproj-F16.gguf"}}
	if !m.hasVision() || (&ModelRec{HFFiles: []string{"a.gguf"}}).hasVision() {
		t.Error("hasVision")
	}
}
