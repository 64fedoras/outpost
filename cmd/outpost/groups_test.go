package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func groupMux(c *Controller) *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/groups", c.handleListGroups)
	m.HandleFunc("POST /api/groups", c.handleCreateGroup)
	m.HandleFunc("POST /api/groups/{id}", c.handleUpdateGroup)
	m.HandleFunc("DELETE /api/groups/{id}", c.handleDeleteGroup)
	m.HandleFunc("POST /api/chats/{id}/meta", c.handleChatMeta)
	m.HandleFunc("POST /api/tasks/{id}/meta", c.handleTaskMeta)
	return m
}

func gcall(t *testing.T, m *http.ServeMux, method, path, body string) (int, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec.Code, rec.Body.Bytes()
}

func TestGroups(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), tasks: map[string]*Task{}, chats: map[string]*Chat{}}
	m := groupMux(c)
	task := &Task{ID: "task_1", Title: "t", dir: t.TempDir()}
	c.tasks[task.ID] = task
	chat := &Chat{ID: "chat_1", Title: "c", dir: t.TempDir()}
	c.chats[chat.ID] = chat

	var a, b Group
	code, body := gcall(t, m, "POST", "/api/groups", `{"name":"  Dice   app "}`)
	if code != 200 || json.Unmarshal(body, &a) != nil || a.Name != "Dice app" || !validGroupColor(a.Color) {
		t.Fatalf("create: %d %s", code, body)
	}
	if code, _ = gcall(t, m, "POST", "/api/groups", `{"name":"dice APP"}`); code != 400 {
		t.Fatalf("duplicate name: %d", code)
	}
	if code, _ = gcall(t, m, "POST", "/api/groups", `{"name":" "}`); code != 400 {
		t.Fatalf("empty name: %d", code)
	}
	_, body = gcall(t, m, "POST", "/api/groups", `{"name":"Home lab","color":"teal"}`)
	json.Unmarshal(body, &b)

	// Move a task and a chat in; an unknown group is refused.
	if code, body = gcall(t, m, "POST", "/api/tasks/task_1/meta", `{"group":"`+a.ID+`"}`); code != 200 || !strings.Contains(string(body), `"group_id":"`+a.ID+`"`) {
		t.Fatalf("task move: %d %s", code, body)
	}
	if code, _ = gcall(t, m, "POST", "/api/tasks/task_1/meta", `{"group":"nope"}`); code != 400 {
		t.Fatalf("unknown group on task: %d", code)
	}
	if code, _ = gcall(t, m, "POST", "/api/chats/chat_1/meta", `{"group":"`+a.ID+`"}`); code != 200 || chat.GroupID != a.ID {
		t.Fatalf("chat move: %d %q", code, chat.GroupID)
	}
	if code, _ = gcall(t, m, "POST", "/api/chats/chat_1/meta", `{"group":"nope"}`); code != 400 {
		t.Fatalf("unknown group on chat: %d", code)
	}

	// Reorder: Home lab first. It survives a reload from disk.
	if code, _ = gcall(t, m, "POST", "/api/groups/"+b.ID, `{"position":0,"name":"Lab"}`); code != 200 {
		t.Fatalf("reorder: %d", code)
	}
	c2 := &Controller{dataDir: c.dataDir}
	c2.loadGroups()
	if len(c2.groups) != 2 || c2.groups[0].ID != b.ID || c2.groups[0].Name != "Lab" || c2.groups[1].ID != a.ID {
		t.Fatalf("after reload: %+v", c2.groups)
	}

	// Deleting a group keeps its items and ungroups them.
	if code, _ = gcall(t, m, "DELETE", "/api/groups/"+a.ID, ""); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if task.GroupID != "" || chat.GroupID != "" || len(c.groups) != 1 {
		t.Fatalf("after delete: task=%q chat=%q groups=%d", task.GroupID, chat.GroupID, len(c.groups))
	}
	if code, _ = gcall(t, m, "DELETE", "/api/groups/"+a.ID, ""); code != 404 {
		t.Fatalf("delete twice: %d", code)
	}
}
