package main

// Full-text search for the sidebar filter: GET /api/search?q=... looks through
// every task's title, prompt, plan and conversation, and every chat's title and
// messages, all of which the controller already holds in memory. Every word of
// the query must appear somewhere in the item (case-insensitive); the result
// says where the first word was found and gives a short snippet around it.

import (
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	searchMaxHits = 100
	searchSnippet = 70 // characters of context on each side of the match
)

type searchHit struct {
	Kind    string    `json:"kind"` // task | chat
	ID      string    `json:"id"`
	Where   string    `json:"where"` // title | prompt | plan | message
	Snippet string    `json:"snippet,omitempty"`
	Updated time.Time `json:"updated"`
}

type searchField struct{ where, text string }

// searchWords splits a query into lower-case words; quotes are ignored.
func searchWords(q string) []string {
	q = strings.ToLower(strings.ReplaceAll(q, `"`, " "))
	return strings.Fields(q)
}

// matchFields returns the hit for fields that contain every word, or ok=false.
// The snippet comes from the first field (in order) that holds the first word,
// unless that is the title, where the row already shows the text.
func matchFields(words []string, fields []searchField) (where, snippet string, ok bool) {
	if len(words) == 0 {
		return "", "", false
	}
	lower := make([]string, len(fields))
	for i, f := range fields {
		lower[i] = strings.ToLower(f.text)
	}
	for _, w := range words {
		found := false
		for _, l := range lower {
			if strings.Contains(l, w) {
				found = true
				break
			}
		}
		if !found {
			return "", "", false
		}
	}
	for i, l := range lower {
		if at := strings.Index(l, words[0]); at >= 0 {
			if fields[i].where == "title" {
				return "title", "", true
			}
			return fields[i].where, snippetAround(fields[i].text, l, at, len(words[0])), true
		}
	}
	return "", "", true // unreachable: the first word was found above
}

// snippetAround cuts text (and its lower-case copy, same byte offsets for ASCII
// and for nearly all text) around a match, on whitespace, with ellipses.
func snippetAround(text, lower string, at, n int) string {
	if len(lower) != len(text) { // case folding changed byte lengths, so offsets do not line up
		return firstLine(text, 2*searchSnippet)
	}
	start, end := max(0, at-searchSnippet), min(len(text), at+n+searchSnippet)
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	s := text[start:end]
	if start > 0 {
		if i := strings.IndexAny(s, " \n\t"); i >= 0 && i < searchSnippet/2 {
			s = s[i+1:]
		}
		s = "…" + s
	}
	if end < len(text) {
		if i := strings.LastIndexAny(s, " \n\t"); i > len(s)-searchSnippet/2 {
			s = s[:i]
		}
		s += "…"
	}
	return strings.Join(strings.Fields(s), " ")
}

func taskSearchFields(t *Task) []searchField {
	f := []searchField{{"title", t.Title}, {"prompt", t.Prompt}}
	if t.Plan != nil {
		var b strings.Builder
		b.WriteString(t.Plan.Title + "\n" + t.Plan.Summary)
		for _, s := range t.Plan.Subtasks {
			b.WriteString("\n" + s.Title + "\n" + strings.Join(s.Files, " "))
		}
		f = append(f, searchField{"plan", b.String()})
	}
	for _, m := range t.Messages {
		if m.Text != "" {
			f = append(f, searchField{"message", m.Text})
		}
		for _, q := range m.Questions {
			f = append(f, searchField{"message", q.Question})
		}
	}
	return f
}

func (c *Controller) search(q string) []searchHit {
	words := searchWords(q)
	if len(words) == 0 {
		return []searchHit{}
	}
	c.mu.Lock()
	tasks := make([]*Task, 0, len(c.tasks))
	for _, t := range c.tasks {
		tasks = append(tasks, t)
	}
	c.mu.Unlock()
	hits := []searchHit{}
	for _, t := range tasks {
		t.mu.Lock()
		where, snip, ok := matchFields(words, taskSearchFields(t))
		upd := t.Created
		if n := len(t.Messages); n > 0 {
			upd = t.Messages[n-1].Time
		}
		id := t.ID
		t.mu.Unlock()
		if ok {
			hits = append(hits, searchHit{Kind: "task", ID: id, Where: where, Snippet: snip, Updated: upd})
		}
	}
	c.chatMu.Lock()
	chats := make([]*Chat, 0, len(c.chats))
	for _, ch := range c.chats {
		chats = append(chats, ch)
	}
	c.chatMu.Unlock()
	for _, ch := range chats {
		ch.mu.Lock()
		f := []searchField{{"title", ch.Title}}
		for _, m := range ch.Messages {
			f = append(f, searchField{"message", m.Text})
		}
		where, snip, ok := matchFields(words, f)
		id, upd := ch.ID, ch.Updated
		ch.mu.Unlock()
		if ok {
			hits = append(hits, searchHit{Kind: "chat", ID: id, Where: where, Snippet: snip, Updated: upd})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Updated.After(hits[j].Updated) })
	if len(hits) > searchMaxHits {
		hits = hits[:searchMaxHits]
	}
	return hits
}

func (c *Controller) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if len(q) > 200 {
		q = q[:200]
	}
	writeJSON(w, 200, c.search(q))
}
