package main

// Screenshots in chat. Images are kept on the controller next to the chat
// (data/chats/<id>-images/), shown again when the chat is reopened and deleted
// with it. A node only gets the bytes inside the inference request, as image_url
// parts for a vision model, and keeps nothing. Only the newest few images go
// back to the model with later turns, so the context does not fill up.

import (
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"path/filepath"
)

const chatHistoryImages = 3 // images sent back to the model from earlier turns

var errNoVisionChat = errors.New("no node has a Vision model loaded, so nothing can read this image. Load Qwen3.6 or Gemma 4 from the Models page, or pick a node that has one")

func (ch *Chat) imagesDir() string { return filepath.Join(ch.dir, ch.ID+"-images") }

// saveImages writes validated images next to the chat and returns their file names.
func (ch *Chat) saveImages(imgs []llmImage) ([]string, error) {
	if len(imgs) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(ch.imagesDir(), 0o700); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(imgs))
	for _, img := range imgs {
		raw, _ := base64.StdEncoding.DecodeString(img.B64)
		name := newID("img") + imageExt[img.Media]
		if err := writeFileAtomic(filepath.Join(ch.imagesDir(), name), raw, 0o600); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

// imageURI reads a saved image back as a data: URI ("" if it is gone).
func (ch *Chat) imageURI(name string) string {
	raw, err := os.ReadFile(filepath.Join(ch.imagesDir(), filepath.Base(name)))
	if err != nil {
		return ""
	}
	media := http.DetectContentType(raw)
	if _, ok := imageExt[media]; !ok {
		return ""
	}
	return llmImage{Media: media, B64: base64.StdEncoding.EncodeToString(raw)}.DataURI()
}

// anyVisionNode reports whether a ready node (the chosen one, or any for
// "auto") has a vision model loaded.
func (c *Controller) anyVisionNode(pref string) bool {
	for _, id := range c.workerNodes() {
		if pref != "" && pref != "auto" && id != pref {
			continue
		}
		if c.nodeVision(id) {
			return true
		}
	}
	return false
}

func (c *Controller) handleChatImage(w http.ResponseWriter, r *http.Request) {
	ch := c.getChat(r.PathValue("id"))
	if ch == nil {
		writeJSON(w, 404, map[string]string{"error": "no such chat"})
		return
	}
	raw, err := os.ReadFile(filepath.Join(ch.imagesDir(), filepath.Base(r.URL.Query().Get("name"))))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "no such image"})
		return
	}
	media := http.DetectContentType(raw)
	if _, ok := imageExt[media]; !ok {
		writeJSON(w, 404, map[string]string{"error": "no such image"})
		return
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(raw)
}
