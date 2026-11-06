package main

// Editing a file by hand between requests (idea 41). On a task that has
// stopped, the file browser can save a file. The first save starts a "your
// edits" round: the file's previous content is kept like any round's, so
// Changes shows the edit and Undo puts it back. Further saves before the next
// request join the same round. The next request plans on the edited files.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

func (c *Controller) handleSaveTaskFile(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRepoFileSize+64<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request (files over 1 MB cannot be edited here)"})
		return
	}
	rel := cleanRel(req.Path)
	if _, err := safeJoin(t.repoDir(), rel); err != nil || rel == "" || strings.HasPrefix(rel, ".git/") || rel == ".git" {
		writeJSON(w, 400, map[string]string{"error": "bad path"})
		return
	}
	if len(req.Content) > maxRepoFileSize || !utf8.ValidString(req.Content) {
		writeJSON(w, 400, map[string]string{"error": "only text files up to 1 MB can be edited here"})
		return
	}
	if !t.idle() {
		writeJSON(w, 409, map[string]string{"error": "wait until the task has stopped"})
		return
	}
	t.mu.Lock()
	if t.Status == "interrupted" {
		t.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "resume or finish the interrupted request first"})
		return
	}
	// Start a "your edits" round unless the latest message already is one.
	if n := len(t.Messages); n == 0 || t.Messages[n-1].Kind != "hand_edit" || t.Messages[n-1].Round != t.Round {
		t.Round++
		t.addMsgLocked(TaskMessage{Role: "user", Kind: "hand_edit", Text: "Your edits"})
	}
	msgID := t.Messages[len(t.Messages)-1].ID
	round := t.Round
	t.mu.Unlock()

	if err := t.writeFile(rel, req.Content); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	os.Remove(filepath.Join(t.dir, "result.zip")) // rebuilt on the next download
	t.mu.Lock()
	for i := range t.Messages {
		if m := &t.Messages[i]; m.ID == msgID {
			m.Files = appendUnique(m.Files, rel)
			m.Text = "You edited " + strconv.Itoa(len(m.Files)) + " file(s) by hand."
		}
	}
	t.Touched = appendUnique(t.Touched, rel)
	t.ResultBytes = 0
	t.saveLocked()
	t.mu.Unlock()
	t.logf("info", "You edited %s by hand (request %d).", rel, round)
	writeJSON(w, 200, map[string]any{"ok": true, "round": round})
}
