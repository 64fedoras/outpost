package main

// Memory: short facts the owner wants every chat and project conversation to
// know ("Prefers Go and plain JavaScript"). Each is "everywhere" or for one
// project. They are added by hand in Settings → Memory or from a suggestion
// under an answer; nothing is saved without the owner's click. A suggestion is
// one small extra call on the node that just answered (never on a paid
// planner), at most a few tries per conversation until one is offered. The list
// lives in memory.json on the controller and is part of backups.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Memory struct {
	ID      string    `json:"id"`
	Text    string    `json:"text"`
	Project string    `json:"project,omitempty"` // "" = everywhere
	Source  string    `json:"source,omitempty"`  // where it came from: "added by hand", a chat's title, "project chat"
	Created time.Time `json:"created"`
}

const (
	maxMemories      = 100
	maxMemoryChars   = 300
	maxMemoryTries   = 3 // answers per conversation that may ask for a suggestion
	memorySuggestOut = 80
)

type memoryStore struct {
	mu   sync.Mutex
	path string
	list []Memory
	done bool // loaded
}

var memories memoryStore

func (c *Controller) mem() *memoryStore {
	m := &memories
	m.mu.Lock()
	defer m.mu.Unlock()
	p := filepath.Join(c.dataDir, "memory.json")
	if !m.done || m.path != p { // tests use several data folders
		m.path, m.list, m.done = p, nil, true
		if b, err := os.ReadFile(p); err == nil {
			json.Unmarshal(b, &m.list)
		}
	}
	return m
}

func (m *memoryStore) saveLocked() error {
	b, _ := json.MarshalIndent(m.list, "", "  ")
	return writeFileAtomic(m.path, b, 0o600)
}

func (m *memoryStore) all() []Memory {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Memory{}, m.list...)
}

// forScope returns the facts for a conversation: everywhere, plus the project's.
func (m *memoryStore) forScope(project string) []string {
	var out []string
	for _, x := range m.all() {
		if x.Project == "" || (project != "" && x.Project == project) {
			out = append(out, x.Text)
		}
	}
	return out
}

func cleanMemory(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, `"'“”`)
	if len(s) > maxMemoryChars {
		s = strings.ToValidUTF8(s[:maxMemoryChars], "")
	}
	return s
}

func (c *Controller) memoryOn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.settings.MemoryOff
}

func (c *Controller) memorySuggestOn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.settings.MemoryOff && !c.settings.MemorySuggestOff
}

// memoryPrompt is the block added to a system prompt, or "" when memory is
// off or empty.
func (c *Controller) memoryPrompt(project string) string {
	if !c.memoryOn() {
		return ""
	}
	facts := c.mem().forScope(project)
	if len(facts) == 0 {
		return ""
	}
	return "\n\nThings the owner asked you to remember (use them when they matter; do not repeat them back):\n- " + strings.Join(facts, "\n- ")
}

// taskMemory is the memory block for a task's planner: only tasks in a project
// get it (the facts for everywhere plus the project's own), the same block the
// project conversation starts with, so the project's Context tab is what its
// tasks are planned with.
func (c *Controller) taskMemory(t *Task) string {
	t.mu.Lock()
	project := t.ProjectID
	t.mu.Unlock()
	if project == "" {
		return ""
	}
	return c.memoryPrompt(project)
}

const memorySuggestSystem = `You help an assistant remember durable facts about its owner across conversations. Read the owner's messages below. If they state one lasting fact about themselves, their setup, their projects or how they want answers (for example "I use Go 1.26", "the home lab runs Gitea", "answer briefly"), reply with that fact as one short sentence in the third person ("Uses Go 1.26."). Do not suggest anything about this one question, temporary details, secrets, passwords, keys, or anything already in the list of known facts. If there is nothing worth remembering, reply with exactly NONE.`

// memoryOnChat turns the memory question into a continuation of the chat just
// answered (the same messages, the answer, then the question) instead of a
// separate prompt. A node has one prompt cache per slot, so a separate prompt
// would replace the chat's cached start and the next message would be read from
// scratch; as a continuation, the next message still shares the chat's prompt.
func memoryOnChat(spec InferenceSpec, hist []ChatMessage, answer string) InferenceSpec {
	if len(spec.Messages) != 2 || spec.Messages[0].Content != memorySuggestSystem || len(hist) == 0 {
		return spec
	}
	msgs := append([]ChatMessage(nil), hist...)
	msgs = append(msgs, ChatMessage{Role: "assistant", Content: answer},
		ChatMessage{Role: "user", Content: spec.Messages[0].Content + "\n\n" + spec.Messages[1].Content})
	spec.Messages = msgs
	return spec
}

var memoryNoneRe = regexp.MustCompile(`(?i)^\W*none\W*$`)

// suggestMemory asks the model for one fact from the owner's messages; "" when none.
func suggestMemory(ctx context.Context, infer inferFn, ownerTexts []string, known []string) string {
	if len(ownerTexts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Known facts:\n")
	if len(known) == 0 {
		b.WriteString("(none)\n")
	}
	for _, k := range known {
		b.WriteString("- " + k + "\n")
	}
	b.WriteString("\nThe owner's messages (newest last):\n")
	start := max(0, len(ownerTexts)-6)
	for _, t := range ownerTexts[start:] {
		b.WriteString("- " + clip(strings.Join(strings.Fields(t), " "), 600) + "\n")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := infer(ctx, InferenceSpec{Messages: []ChatMessage{{Role: "system", Content: memorySuggestSystem}, {Role: "user", Content: b.String()}},
		Params: InferenceParams{MaxTokens: memorySuggestOut}})
	if err != nil || out == nil {
		return ""
	}
	s := cleanMemory(strings.SplitN(strings.TrimSpace(out.Text), "\n", 2)[0])
	s = strings.TrimSpace(strings.TrimPrefix(s, "- "))
	if len(s) < 8 || memoryNoneRe.MatchString(s) || strings.Contains(strings.ToLower(s), "nothing worth") {
		return ""
	}
	low := strings.ToLower(s)
	for _, k := range known {
		if strings.EqualFold(k, s) || strings.Contains(strings.ToLower(k), low) {
			return ""
		}
	}
	return s
}

// ---------- HTTP ----------

func (c *Controller) memoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/memory", c.auth(c.handleListMemory))
	mux.HandleFunc("POST /api/memory", c.auth(c.handleAddMemory))
	mux.HandleFunc("POST /api/memory/{id}", c.auth(c.handleEditMemory))
	mux.HandleFunc("DELETE /api/memory/{id}", c.auth(c.handleDeleteMemory))
	mux.HandleFunc("POST /api/memory/suggestion", c.auth(c.handleMemorySuggestion))
}

func (c *Controller) handleListMemory(w http.ResponseWriter, r *http.Request) {
	list := c.mem().all()
	chars := 0
	for _, m := range list {
		chars += len(m.Text) + 3
	}
	writeJSON(w, 200, map[string]any{"memories": list, "tokens": (chars + 3) / 4})
}

type memoryReq struct {
	Text    *string `json:"text"`
	Project *string `json:"project"`
	Source  string  `json:"source"`
}

func (c *Controller) readMemoryReq(w http.ResponseWriter, r *http.Request) (*memoryReq, bool) {
	var req memoryReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return nil, false
	}
	if req.Project != nil && *req.Project != "" && c.getProject(*req.Project) == nil {
		writeJSON(w, 400, map[string]string{"error": "no such project"})
		return nil, false
	}
	return &req, true
}

// addMemory saves one fact; it is used by the API and by "Remember".
func (c *Controller) addMemory(text, project, source string) (Memory, int, error) {
	text = cleanMemory(text)
	if text == "" {
		return Memory{}, 400, fmt.Errorf("write the fact to remember")
	}
	m := c.mem()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.list) >= maxMemories {
		return Memory{}, 400, fmt.Errorf("memory is full (%d facts); forget some first", maxMemories)
	}
	for _, x := range m.list {
		if strings.EqualFold(x.Text, text) && x.Project == project {
			return x, 200, nil
		}
	}
	x := Memory{ID: newID("mem"), Text: text, Project: project, Source: clip(source, 80), Created: time.Now()}
	m.list = append(m.list, x)
	if err := m.saveLocked(); err != nil {
		m.list = m.list[:len(m.list)-1]
		return Memory{}, 500, err
	}
	return x, 200, nil
}

func (c *Controller) handleAddMemory(w http.ResponseWriter, r *http.Request) {
	req, ok := c.readMemoryReq(w, r)
	if !ok {
		return
	}
	text, project := "", ""
	if req.Text != nil {
		text = *req.Text
	}
	if req.Project != nil {
		project = *req.Project
	}
	x, code, err := c.addMemory(text, project, firstNonEmpty(req.Source, "added by hand"))
	if err != nil {
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, x)
}

func (c *Controller) handleEditMemory(w http.ResponseWriter, r *http.Request) {
	req, ok := c.readMemoryReq(w, r)
	if !ok {
		return
	}
	m := c.mem()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.list {
		if m.list[i].ID != r.PathValue("id") {
			continue
		}
		if req.Text != nil {
			t := cleanMemory(*req.Text)
			if t == "" {
				writeJSON(w, 400, map[string]string{"error": "the fact is empty; use Forget to remove it"})
				return
			}
			m.list[i].Text = t
		}
		if req.Project != nil {
			m.list[i].Project = *req.Project
		}
		if err := m.saveLocked(); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, m.list[i])
		return
	}
	writeJSON(w, 404, map[string]string{"error": "no such memory"})
}

func (c *Controller) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	m := c.mem()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.list {
		if m.list[i].ID == r.PathValue("id") {
			m.list = append(m.list[:i], m.list[i+1:]...)
			if err := m.saveLocked(); err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
	}
	writeJSON(w, 404, map[string]string{"error": "no such memory"})
}

// handleMemorySuggestion answers a suggestion under a chat or project answer:
// with text it is remembered (edited first or not), without it is dismissed.
// Either way the suggestion leaves the message.
func (c *Controller) handleMemorySuggestion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Chat    string `json:"chat"`
		Project string `json:"project"`
		Msg     string `json:"msg"`
		Text    string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	source := ""
	switch {
	case req.Chat != "":
		ch := c.getChat(req.Chat)
		if ch == nil {
			writeJSON(w, 404, map[string]string{"error": "no such chat"})
			return
		}
		ch.mu.Lock()
		found := false
		for i := range ch.Messages {
			if ch.Messages[i].ID == req.Msg {
				ch.Messages[i].Suggest, found = "", true
			}
		}
		source = "from \"" + ch.Title + "\""
		if found {
			ch.saveLocked()
		}
		ch.mu.Unlock()
		taskLive.changed("chat_" + ch.ID)
	case req.Project != "":
		p := c.getProject(req.Project)
		if p == nil {
			writeJSON(w, 404, map[string]string{"error": "no such project"})
			return
		}
		// The suggestion is on a message of the project's conversation chat.
		if ch := c.projectConvChat(p, false); ch != nil {
			ch.mu.Lock()
			for i := range ch.Messages {
				if ch.Messages[i].ID == req.Msg {
					ch.Messages[i].Suggest = ""
				}
			}
			ch.saveLocked()
			ch.mu.Unlock()
			taskLive.changed("chat_" + ch.ID)
		}
		source = "from the project chat"
	default:
		writeJSON(w, 400, map[string]string{"error": "name the chat or project"})
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	x, code, err := c.addMemory(req.Text, req.Project, source)
	if err != nil {
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("memory: remembered %q (%s)", clip(x.Text, 60), source)
	writeJSON(w, 200, x)
}
