package main

// Sidebar groups: named, coloured folders that hold tasks and chats. A task or
// chat is in at most one group (its GroupID); the group list and its order live
// on the controller in data/groups.json so every browser and phone sees the
// same sidebar. Deleting a group keeps its tasks and chats and just ungroups them.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type Group struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"` // one of groupColors
}

const (
	maxGroups    = 100
	maxGroupName = 40
)

var groupColors = []string{"blue", "green", "purple", "amber", "red", "teal"}

func validGroupColor(s string) bool {
	for _, c := range groupColors {
		if c == s {
			return true
		}
	}
	return false
}

func (c *Controller) groupsFile() string { return filepath.Join(c.dataDir, "groups.json") }

// loadGroups reads groups.json (or its backup copy). Must run before the server starts.
func (c *Controller) loadGroups() {
	c.groupMu.Lock()
	defer c.groupMu.Unlock()
	c.groups = []Group{}
	usedBak, err := readWithBackup(c.groupsFile(), func(b []byte) error {
		var g []Group
		if err := json.Unmarshal(b, &g); err != nil {
			return err
		}
		c.groups = g
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		c.noteRecovered("sidebar groups: groups.json could not be read (" + err.Error() + ")")
	} else if usedBak {
		c.noteRecovered("sidebar groups: groups.json was damaged; loaded the previous copy")
	}
}

// saveGroupsLocked writes the list; the caller holds groupMu.
func (c *Controller) saveGroupsLocked() error {
	b, _ := json.MarshalIndent(c.groups, "", "  ")
	return writeFileAtomic(c.groupsFile(), b, 0o600)
}

func (c *Controller) groupExists(id string) bool {
	c.groupMu.Lock()
	defer c.groupMu.Unlock()
	for _, g := range c.groups {
		if g.ID == id {
			return true
		}
	}
	return false
}

func cleanGroupName(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxGroupName {
		s = string(r[:maxGroupName])
	}
	return s
}

func (c *Controller) groupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/groups", c.auth(c.handleListGroups))
	mux.HandleFunc("POST /api/groups", c.auth(c.handleCreateGroup))
	mux.HandleFunc("POST /api/groups/{id}", c.auth(c.handleUpdateGroup))
	mux.HandleFunc("DELETE /api/groups/{id}", c.auth(c.handleDeleteGroup))
	mux.HandleFunc("POST /api/chats/{id}/meta", c.auth(c.handleChatMeta))
}

func (c *Controller) handleListGroups(w http.ResponseWriter, r *http.Request) {
	c.groupMu.Lock()
	out := append([]Group{}, c.groups...)
	c.groupMu.Unlock()
	writeJSON(w, 200, out)
}

func (c *Controller) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	name := cleanGroupName(req.Name)
	if name == "" {
		writeJSON(w, 400, map[string]string{"error": "the group needs a name"})
		return
	}
	c.groupMu.Lock()
	defer c.groupMu.Unlock()
	if len(c.groups) >= maxGroups {
		writeJSON(w, 400, map[string]string{"error": "too many groups; delete some first"})
		return
	}
	for _, g := range c.groups {
		if strings.EqualFold(g.Name, name) {
			writeJSON(w, 400, map[string]string{"error": "a group with that name already exists"})
			return
		}
	}
	color := req.Color
	if !validGroupColor(color) {
		color = groupColors[len(c.groups)%len(groupColors)]
	}
	g := Group{ID: newID("grp"), Name: name, Color: color}
	c.groups = append(c.groups, g)
	if err := c.saveGroupsLocked(); err != nil {
		c.groups = c.groups[:len(c.groups)-1]
		writeJSON(w, 500, map[string]string{"error": "could not save: " + err.Error()})
		return
	}
	writeJSON(w, 200, g)
}

// handleUpdateGroup renames a group, recolours it, or moves it (position is the
// new 0-based index in the list). Any field may be left out.
func (c *Controller) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     *string `json:"name"`
		Color    *string `json:"color"`
		Position *int    `json:"position"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	c.groupMu.Lock()
	defer c.groupMu.Unlock()
	idx := -1
	for i, g := range c.groups {
		if g.ID == r.PathValue("id") {
			idx = i
		}
	}
	if idx < 0 {
		writeJSON(w, 404, map[string]string{"error": "no such group"})
		return
	}
	old := append([]Group{}, c.groups...)
	g := c.groups[idx]
	if req.Name != nil {
		name := cleanGroupName(*req.Name)
		if name == "" {
			writeJSON(w, 400, map[string]string{"error": "the group needs a name"})
			return
		}
		for i, o := range c.groups {
			if i != idx && strings.EqualFold(o.Name, name) {
				writeJSON(w, 400, map[string]string{"error": "a group with that name already exists"})
				return
			}
		}
		g.Name = name
	}
	if req.Color != nil {
		if !validGroupColor(*req.Color) {
			writeJSON(w, 400, map[string]string{"error": "unknown colour"})
			return
		}
		g.Color = *req.Color
	}
	c.groups[idx] = g
	if req.Position != nil {
		p := *req.Position
		if p < 0 {
			p = 0
		}
		if p > len(c.groups)-1 {
			p = len(c.groups) - 1
		}
		rest := append(append([]Group{}, c.groups[:idx]...), c.groups[idx+1:]...)
		c.groups = append(rest[:p], append([]Group{g}, rest[p:]...)...)
	}
	if err := c.saveGroupsLocked(); err != nil {
		c.groups = old
		writeJSON(w, 500, map[string]string{"error": "could not save: " + err.Error()})
		return
	}
	writeJSON(w, 200, g)
}

// handleDeleteGroup removes a group; its tasks and chats stay and become ungrouped.
func (c *Controller) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c.groupMu.Lock()
	idx := -1
	for i, g := range c.groups {
		if g.ID == id {
			idx = i
		}
	}
	if idx < 0 {
		c.groupMu.Unlock()
		writeJSON(w, 404, map[string]string{"error": "no such group"})
		return
	}
	old := append([]Group{}, c.groups...)
	c.groups = append(c.groups[:idx], c.groups[idx+1:]...)
	if err := c.saveGroupsLocked(); err != nil {
		c.groups = old
		c.groupMu.Unlock()
		writeJSON(w, 500, map[string]string{"error": "could not save: " + err.Error()})
		return
	}
	c.groupMu.Unlock()
	c.mu.Lock()
	tasks := make([]*Task, 0, len(c.tasks))
	for _, t := range c.tasks {
		tasks = append(tasks, t)
	}
	c.mu.Unlock()
	for _, t := range tasks {
		t.mu.Lock()
		if t.GroupID == id {
			t.GroupID = ""
			t.saveLocked()
		}
		t.mu.Unlock()
	}
	c.chatMu.Lock()
	chats := make([]*Chat, 0, len(c.chats))
	for _, ch := range c.chats {
		chats = append(chats, ch)
	}
	c.chatMu.Unlock()
	for _, ch := range chats {
		ch.mu.Lock()
		if ch.GroupID == id {
			ch.GroupID = ""
			ch.saveLocked()
		}
		ch.mu.Unlock()
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleChatMeta renames a chat or moves it into a group ("" = no group).
func (c *Controller) handleChatMeta(w http.ResponseWriter, r *http.Request) {
	ch := c.getChat(r.PathValue("id"))
	if ch == nil {
		writeJSON(w, 404, map[string]string{"error": "no such chat"})
		return
	}
	var req struct {
		Title *string `json:"title"`
		Group *string `json:"group"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	if req.Group != nil && *req.Group != "" && !c.groupExists(*req.Group) {
		writeJSON(w, 400, map[string]string{"error": "no such group"})
		return
	}
	title := ""
	if req.Title != nil {
		title = cleanGroupName(*req.Title)
		if title == "" {
			writeJSON(w, 400, map[string]string{"error": "the name cannot be empty"})
			return
		}
	}
	ch.mu.Lock()
	if req.Title != nil {
		ch.Title = title
	}
	if req.Group != nil {
		ch.GroupID = *req.Group
	}
	ch.saveLocked()
	out := map[string]any{"id": ch.ID, "title": ch.Title, "group": ch.GroupID}
	ch.mu.Unlock()
	writeJSON(w, 200, out)
}
