package main

// Documents in chat: the 📎 button takes PDF, DOCX, XLSX, ODT and CSV files
// besides images. The controller reads each into text when the message is sent
// (docextract.go) and keeps the text next to the chat (<chat>-docs/); the
// message shows a chip with what was read, and the text goes to the model with
// the message every time the conversation is answered.

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxChatDocs     = 5
	maxChatDocBytes = 25 << 20 // all documents of one message, as uploaded
)

type chatDoc struct {
	info DocInfo
	text string
}

// readChatDocs decodes and reads the documents sent with a message.
func readChatDocs(in []imageUpload) ([]chatDoc, error) {
	if len(in) > maxChatDocs {
		return nil, fmt.Errorf("at most %d documents per message", maxChatDocs)
	}
	var out []chatDoc
	total := 0
	for _, u := range in {
		name := filepath.Base(strings.ReplaceAll(strings.TrimSpace(u.Name), "\\", "/"))
		if docKind(name) == "" {
			return nil, fmt.Errorf("%s: attach PDF, Word (.docx), Excel (.xlsx), OpenDocument (.odt) or CSV files", name)
		}
		data := u.Data
		if strings.HasPrefix(data, "data:") {
			if i := strings.Index(data, ","); i >= 0 {
				data = data[i+1:]
			}
		}
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil || len(raw) == 0 {
			return nil, fmt.Errorf("%s could not be read", name)
		}
		if total += len(raw); total > maxChatDocBytes {
			return nil, fmt.Errorf("documents in one message are limited to %d MB", maxChatDocBytes>>20)
		}
		text, info, err := extractDocument(name, raw)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(text) == "" && info.Warning == "" {
			info.Warning = "No text was found in this file."
		}
		out = append(out, chatDoc{info, text})
	}
	return out, nil
}

func (ch *Chat) docsDir() string { return filepath.Join(ch.dir, ch.ID+"-docs") }

// saveDocs writes the documents' text for message id and returns their infos. Called with ch.mu held.
func (ch *Chat) saveDocs(id string, docs []chatDoc) []DocInfo {
	if len(docs) == 0 {
		return nil
	}
	os.MkdirAll(ch.docsDir(), 0o700)
	var out []DocInfo
	for i, d := range docs {
		d.info.File = fmt.Sprintf("%s-%d.txt", id, i+1)
		if err := writeFileAtomic(filepath.Join(ch.docsDir(), d.info.File), []byte(d.text), 0o600); err != nil {
			d.info.Warning = "The text could not be saved: " + err.Error()
			d.info.File = ""
		}
		out = append(out, d.info)
	}
	return out
}

// docText reads a document's saved text.
func (ch *Chat) docText(d DocInfo) string {
	if d.File == "" || strings.ContainsAny(d.File, `/\`) {
		return "(the text of this document is not available)"
	}
	b, err := os.ReadFile(filepath.Join(ch.docsDir(), d.File))
	if err != nil {
		return "(the text of this document is not available)"
	}
	return string(b)
}

// clipForContext cuts any message longer than limit characters (a long
// document) so the conversation fits the node's context; the newest message
// keeps the most.
func clipForContext(msgs []ChatMessage, limit int) {
	for i := range msgs {
		if i == 0 || len(msgs[i].Content) <= limit {
			continue
		}
		keep := limit
		if i < len(msgs)-1 {
			keep = limit / 4
		}
		msgs[i].Content = strings.ToValidUTF8(msgs[i].Content[:keep], "") + "\n…[cut here: the rest does not fit this model's context window. Say so if the answer needs the part that was cut.]"
	}
}

// handleChatDoc returns the text read from one document (the chip's "Text" link).
func (c *Controller) handleChatDoc(w http.ResponseWriter, r *http.Request) {
	ch := c.getChat(r.PathValue("id"))
	if ch == nil {
		writeJSON(w, 404, map[string]string{"error": "no such chat"})
		return
	}
	name := filepath.Base(r.URL.Query().Get("file"))
	if !strings.HasSuffix(name, ".txt") {
		writeJSON(w, 404, map[string]string{"error": "no such document"})
		return
	}
	raw, err := os.ReadFile(filepath.Join(ch.docsDir(), name))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "no such document"})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(raw)
}
