package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// waitChatIdle waits until the chat is not answering and has n messages.
func waitChatIdle(t *testing.T, ch *Chat, n int) {
	t.Helper()
	for i := 0; i < 500; i++ {
		ch.mu.Lock()
		busy, got := ch.Busy, len(ch.Messages)
		ch.mu.Unlock()
		if busy == "" && got == n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("chat %s never reached %d idle messages", ch.ID, n)
}

func chatDo(m *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func chatMux(c *Controller) *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/chats", c.handleListChats)
	m.HandleFunc("POST /api/chats", c.handleNewChat)
	m.HandleFunc("POST /api/chats/{id}/messages", c.handleChatSend)
	m.HandleFunc("POST /api/chats/{id}/meta", c.handleChatMeta)
	return m
}

func newChatID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var created struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &created)
	if rec.Code != 200 || created.ID == "" {
		t.Fatalf("new chat %d %s", rec.Code, rec.Body)
	}
	return created.ID
}

// A plain chat builds the request it built before chats could belong to a project: the same system
// message (base text, the chat's instructions, memory, the offer paragraph), the same history, no
// tools, no project text. Prompt cache reuse on the nodes depends on this prefix staying put.
func TestPlainChatRequestUnchanged(t *testing.T) {
	c := chatController(t)
	c.projects = map[string]*Project{}
	var specs []InferenceSpec
	var mu sync.Mutex
	fakeInferNode(t, c, "n", &specs, &mu)
	m := chatMux(c)
	id := newChatID(t, chatDo(m, "POST", "/api/chats", `{"text":"hello there","instructions":"Be brief."}`))
	ch := c.getChat(id)
	waitChatIdle(t, ch, 2)
	if rec := chatDo(m, "POST", "/api/chats/"+id+"/messages", `{"text":"and again"}`); rec.Code != 200 {
		t.Fatalf("send %d %s", rec.Code, rec.Body)
	}
	waitChatIdle(t, ch, 4)
	mu.Lock()
	defer mu.Unlock()
	if len(specs) != 2 {
		t.Fatalf("%d model calls, want 2", len(specs))
	}
	// The system message, written out: base text, instructions, then whatever the offer paragraph says.
	wantSys := "You are a helpful, concise assistant. Format code in fenced blocks." +
		"\n\nFollow these instructions from the user for this chat:\nBe brief." +
		c.offerPrompt(offerScope{chat: ch})
	for i, s := range specs {
		if s.Messages[0].Role != "system" || s.Messages[0].Content != wantSys {
			t.Fatalf("call %d system message:\n%q\nwant\n%q", i, s.Messages[0].Content, wantSys)
		}
		if s.Tools != nil {
			t.Fatalf("call %d offered tools: %v", i, s.Tools)
		}
	}
	if strings.Contains(wantSys, "software project") || strings.Contains(wantSys, "[start task]") {
		t.Fatal("project text in a plain chat")
	}
	got := []ChatMessage{}
	for _, m := range specs[1].Messages {
		got = append(got, ChatMessage{Role: m.Role, Content: m.Content})
	}
	want := []ChatMessage{{Role: "system", Content: wantSys}, {Role: "user", Content: "hello there"}, {Role: "assistant", Content: "echo: hello there"}, {Role: "user", Content: "and again"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("history of call 2:\n%+v\nwant\n%+v", got, want)
	}
	// Turn 2 starts with turn 1's prompt (the cache prefix).
	if specs[1].Messages[1].Content != specs[0].Messages[1].Content {
		t.Fatal("history prefix changed")
	}
	if n := ch.Node; n != "auto" || ch.ProjectID != "" {
		t.Fatalf("node %q project %q", n, ch.ProjectID)
	}
	// "planner" is for project conversations only.
	if rec := chatDo(m, "POST", "/api/chats", `{"text":"x","node":"planner"}`); rec.Code != 400 {
		t.Fatalf("plain chat took the planner: %d", rec.Code)
	}
	if rec := chatDo(m, "POST", "/api/chats/"+id+"/messages", `{"text":"x","node":"planner"}`); rec.Code != 400 {
		t.Fatalf("plain chat took the planner on send: %d", rec.Code)
	}
}

// With local web search on, a plain chat's tools and system text are the old ones too.
func TestPlainChatToolsUnchanged(t *testing.T) {
	c := chatController(t)
	c.projects = map[string]*Project{}
	c.settings.ChatWebSearch, c.settings.ChatSearchWith = true, "local"
	var specs []InferenceSpec
	var mu sync.Mutex
	fakeInferNode(t, c, "n", &specs, &mu)
	m := chatMux(c)
	id := newChatID(t, chatDo(m, "POST", "/api/chats", `{"text":"hello","instructions":""}`))
	waitChatIdle(t, c.getChat(id), 2)
	mu.Lock()
	defer mu.Unlock()
	first := specs[0]
	var names []string
	for _, tl := range first.Tools {
		names = append(names, tl.Function.Name)
	}
	if strings.Join(names, ",") != "web_search,read_page" {
		t.Fatalf("tools %v", names)
	}
	wantSys := "You are a helpful, concise assistant. Format code in fenced blocks." + c.offerPrompt(offerScope{chat: c.getChat(id)}) + chatToolSystem
	if first.Messages[0].Content != wantSys {
		t.Fatalf("system:\n%q\nwant\n%q", first.Messages[0].Content, wantSys)
	}
}

func testProjectIn(t *testing.T, c *Controller, id, name string) *Project {
	t.Helper()
	p := &Project{Schema: 1, ID: id, Name: name, Created: time.Now().Add(-time.Hour), slot: make(chan struct{}, 1)}
	p.dir = filepath.Join(c.projectsDir(), p.ID)
	os.MkdirAll(p.repoDir(), 0o755)
	if c.projects == nil {
		c.projects = map[string]*Project{}
	}
	if c.tasks == nil {
		c.tasks = map[string]*Task{}
	}
	c.projects[id] = p
	return p
}

// A project conversation puts the project's guidance, instructions and memory in the system message,
// which stays the same while the project's tasks change; the changing state follows the newest message.
func TestProjectChatPrefixStable(t *testing.T) {
	c := chatController(t)
	p := testProjectIn(t, c, "proj_a", "dice")
	p.Instructions, p.TestCommand = "Use pytest.", "pytest -q"
	os.WriteFile(filepath.Join(p.repoDir(), "roll.py"), []byte("def roll(): pass\n"), 0o644)
	var specs []InferenceSpec
	var mu sync.Mutex
	fakeInferNode(t, c, "n", &specs, &mu)
	m := chatMux(c)
	id := newChatID(t, chatDo(m, "POST", "/api/chats", `{"text":"what is here?","project_id":"proj_a","instructions":""}`))
	ch := c.getChat(id)
	if ch.ProjectID != "proj_a" {
		t.Fatalf("project %q", ch.ProjectID)
	}
	waitChatIdle(t, ch, 2)
	// Work happens in the project between the turns.
	c.mu.Lock()
	c.tasks["task_a"] = &Task{ID: "task_a", Title: "Add roll", ProjectID: "proj_a", Status: "working", Created: time.Now(), dir: t.TempDir(),
		Messages: []TaskMessage{{Role: "user", Kind: "text", Text: "add a roll function"}}}
	c.mu.Unlock()
	if rec := chatDo(m, "POST", "/api/chats/"+id+"/messages", `{"text":"and now?"}`); rec.Code != 200 {
		t.Fatalf("send %d %s", rec.Code, rec.Body)
	}
	waitChatIdle(t, ch, 4)
	mu.Lock()
	defer mu.Unlock()
	var firsts []InferenceSpec // the first call of each turn (the one with tools)
	for _, text := range []string{"what is here?", "and now?"} {
		for _, s := range specs {
			if last := s.Messages[len(s.Messages)-1]; s.Tools != nil && last.Role == "user" && strings.HasSuffix(last.Content, text) {
				firsts = append(firsts, s)
				break
			}
		}
	}
	if len(firsts) != 2 {
		t.Fatalf("%d turns with tools among %d calls", len(firsts), len(specs))
	}
	sys := firsts[0].Messages[0].Content
	for _, want := range []string{"helpful, concise assistant", `"dice"`, "[start task]", "Use pytest.", "pytest -q", "read-only tools", "web search is off"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system message lacks %q:\n%s", want, sys)
		}
	}
	if strings.Contains(sys, "roll.py") || strings.Contains(sys, "Add roll") {
		t.Errorf("changing project state is in the system message:\n%s", sys)
	}
	if firsts[1].Messages[0].Content != sys {
		t.Fatalf("system message changed between turns:\n%q\n%q", sys, firsts[1].Messages[0].Content)
	}
	// The earlier turns are in the history as the owner typed them, without the state of that time.
	// (the fake node echoes what it was sent, so the stored answer repeats turn 1's state)
	if h := firsts[1].Messages; h[1].Content != "what is here?" || h[2].Role != "assistant" {
		t.Fatalf("history changed: %+v", firsts[1].Messages)
	}
	var names []string
	for _, tl := range firsts[0].Tools {
		names = append(names, tl.Function.Name)
	}
	if strings.Join(names, ",") != "list_files,read_file,search,outline" {
		t.Fatalf("project tools %v", names)
	}
	u0, u1 := firsts[0].Messages[1].Content, firsts[1].Messages[len(firsts[1].Messages)-1].Content
	if !strings.Contains(u0, "roll.py") || strings.Contains(u0, "Add roll") || !strings.HasSuffix(u0, "what is here?") {
		t.Errorf("turn 1 state:\n%s", u0)
	}
	if !strings.Contains(u1, "Add roll (task_a) [working]") || !strings.HasSuffix(u1, "and now?") {
		t.Errorf("turn 2 state:\n%s", u1)
	}
	// The chat keeps what the owner typed, not the state.
	if ch.Messages[0].Text != "what is here?" || ch.Messages[2].Text != "and now?" {
		t.Fatalf("stored messages %+v", ch.Messages)
	}
}

func TestProjectChatNeedsAProject(t *testing.T) {
	c := chatController(t)
	m := chatMux(c)
	if rec := chatDo(m, "POST", "/api/chats", `{"text":"x","project_id":"proj_none"}`); rec.Code != 404 {
		t.Fatalf("unknown project: %d", rec.Code)
	}
}

// chat.json of a project becomes a chat once; the file is never touched and a second start changes nothing.
func TestMigrateProjectChats(t *testing.T) {
	c := chatController(t)
	p := testProjectIn(t, c, "proj_m", "old")
	q := testProjectIn(t, c, "proj_empty", "none yet")
	old := `{
  "messages": [
    {"id": "m1", "role": "user", "text": "what is here?", "time": "2026-11-01T10:00:00Z"},
    {"id": "m2", "role": "assistant", "text": "Started:", "time": "2026-11-01T10:00:05Z", "tasks": ["task_1"], "who": "Claude · Sonnet", "cost_usd": 0.02, "reads": ["Listed the project"], "suggest": "Uses pytest.", "offer": {"key": "web_search"}},
    {"id": "m3", "role": "assistant", "who": "Research", "question": "how?", "text": "report", "saved": "Research - how.md", "note": "3 lanes", "time": "2026-11-01T10:05:00Z", "research": {"stage": "done"}}
  ],
  "mem_tries": 1,
  "offers_declined": ["memory"]
}`
	file := filepath.Join(p.dir, "chat.json")
	os.WriteFile(file, []byte(old), 0o600)
	c.migrateProjectChats()
	if q.ConvChatID != "" {
		t.Fatalf("a project with no conversation got a chat: %q", q.ConvChatID)
	}
	if p.ConvChatID == "" {
		t.Fatal("no ConvChatID")
	}
	ch := c.getChat(p.ConvChatID)
	if ch == nil || ch.ProjectID != "proj_m" || ch.Title != "Project conversation" || len(ch.Messages) != 3 || ch.MemTries != 1 || len(ch.OffersDeclined) != 1 {
		t.Fatalf("chat %+v", ch)
	}
	a := ch.Messages[1]
	if a.Role != "assistant" || a.Node != "Claude · Sonnet" || a.Cost != 0.02 || a.Tasks[0] != "task_1" || a.Reads[0] != "Listed the project" || a.Suggest != "Uses pytest." || a.Offer == nil || a.At.IsZero() {
		t.Fatalf("assistant message %+v", a)
	}
	if r := ch.Messages[2]; r.Question != "how?" || r.Saved != "Research - how.md" || r.Research == nil || r.Note != "3 lanes" {
		t.Fatalf("research message %+v", r)
	}
	if ch.Node != "planner" {
		t.Fatalf("node %q: the copy keeps answering with the project's planner", ch.Node)
	}
	// The chat is on disk, the project points at it, the old file is as it was.
	if _, err := os.Stat(filepath.Join(c.chatsDir(), ch.ID+".json")); err != nil {
		t.Fatal(err)
	}
	var onDisk Project
	b, _ := os.ReadFile(filepath.Join(p.dir, "project.json"))
	json.Unmarshal(b, &onDisk)
	if onDisk.ConvChatID != ch.ID {
		t.Fatalf("project.json: %s", b)
	}
	if got, _ := os.ReadFile(file); string(got) != old {
		t.Fatalf("chat.json changed:\n%s", got)
	}
	// The page's view is the same conversation in its old shape.
	v := c.projectChatViewFor(p)
	if len(v.Messages) != 3 || v.Messages[1].Who != "Claude · Sonnet" || v.Messages[1].Tasks[0] != "task_1" || v.Messages[0].Time.IsZero() || v.Busy {
		t.Fatalf("view %+v", v)
	}
	// Starting again (also after the chat changed) migrates nothing a second time.
	ch.mu.Lock()
	ch.Messages = append(ch.Messages, ChatMsg{ID: "m4", Role: "user", Text: "later", At: time.Now()})
	ch.mu.Unlock()
	c2 := chatController(t)
	c2.dataDir = c.dataDir
	c2.projects = map[string]*Project{}
	c2.loadProjects()
	c2.loadChats()
	ch.mu.Lock()
	ch.saveLocked()
	ch.mu.Unlock()
	c2.loadChats()
	c2.migrateProjectChats()
	c2.migrateProjectChats()
	if len(c2.chats) != 1 || len(c2.getChat(ch.ID).Messages) != 4 || c2.projects["proj_m"].ConvChatID != ch.ID {
		t.Fatalf("second start: %d chats, %+v", len(c2.chats), c2.projects["proj_m"])
	}
	if got, _ := os.ReadFile(file); string(got) != old {
		t.Fatal("chat.json changed by the second start")
	}
}

// A copy interrupted before the project was saved is found again, not made twice.
func TestMigrateProjectChatsResumes(t *testing.T) {
	c := chatController(t)
	p := testProjectIn(t, c, "proj_r", "r")
	os.WriteFile(filepath.Join(p.dir, "chat.json"), []byte(`{"messages":[{"id":"m1","role":"user","text":"hi","time":"2026-11-01T10:00:00Z"}]}`), 0o600)
	c.migrateProjectChats()
	id := p.ConvChatID
	p.mu.Lock()
	p.ConvChatID = "" // the crash: chat written, project not
	p.mu.Unlock()
	c.migrateProjectChats()
	if p.ConvChatID != id || len(c.chats) != 1 || len(c.getChat(id).Messages) != 1 {
		t.Fatalf("%q vs %q, %d chats", p.ConvChatID, id, len(c.chats))
	}
}

// Files written by the version before this one still load, and what this version writes can be read
// by that version (it ignores the new fields), so a rollback loses nothing.
func TestProjectChatFilesReadableBothWays(t *testing.T) {
	dir := t.TempDir()
	// Old shapes in.
	oldChat := `{"id":"chat_old","title":"hi","node":"auto","created":"2026-11-01T10:00:00Z","updated":"2026-11-01T10:00:00Z","messages":[{"id":"m1","role":"user","text":"hello","at":"2026-11-01T10:00:00Z"}]}`
	os.MkdirAll(filepath.Join(dir, "chats"), 0o700)
	os.WriteFile(filepath.Join(dir, "chats", "chat_old.json"), []byte(oldChat), 0o600)
	pdir := filepath.Join(dir, "projects", "proj_o")
	os.MkdirAll(pdir, 0o700)
	os.WriteFile(filepath.Join(pdir, "project.json"), []byte(`{"schema":1,"id":"proj_o","name":"o","created":"2026-11-01T10:00:00Z"}`), 0o600)
	c := &Controller{dataDir: dir, projects: map[string]*Project{}}
	c.loadProjects()
	c.loadChats()
	ch := c.getChat("chat_old")
	if ch == nil || ch.ProjectID != "" || len(ch.Messages) != 1 || c.projects["proj_o"] == nil || c.projects["proj_o"].ConvChatID != "" {
		t.Fatalf("old files: %+v %+v", ch, c.projects)
	}
	if sums := c.chatSummaries(); len(sums) != 1 {
		t.Fatalf("summaries %v", sums)
	}
	// New shapes out, read by a struct shaped like the old Chat, ChatMsg and Project.
	p := c.projects["proj_o"]
	conv := c.projectConvChat(p, true)
	conv.mu.Lock()
	conv.Messages = append(conv.Messages, ChatMsg{ID: "m9", Role: "assistant", Text: "Started:", At: time.Now(), Tasks: []string{"t1"}, Reads: []string{"Read a.go"}, Saved: "r.md", Question: "q", Cost: 0.5})
	conv.saveLocked()
	conv.mu.Unlock()
	b, _ := os.ReadFile(filepath.Join(dir, "chats", conv.ID+".json"))
	var oldShape struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Node     string `json:"node"`
		Messages []struct {
			ID   string `json:"id"`
			Role string `json:"role"`
			Text string `json:"text"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(b, &oldShape); err != nil || oldShape.ID != conv.ID || len(oldShape.Messages) != 1 || oldShape.Messages[0].Text != "Started:" {
		t.Fatalf("old reader: %v %+v\n%s", err, oldShape, b)
	}
	pb, _ := os.ReadFile(filepath.Join(pdir, "project.json"))
	var oldProj struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(pb, &oldProj); err != nil || oldProj.Name != "o" {
		t.Fatalf("old project reader: %v %s", err, pb)
	}
	// A plain chat's file has no project field.
	if strings.Contains(string(mustRead(t, filepath.Join(dir, "chats", "chat_old.json"))), "project_id") {
		t.Fatal("plain chat gained a project field")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The list filters by project, the sidebar list stays plain chats, a chat can be moved, and deleting
// the project deletes its conversations.
func TestChatProjectFilterAndMove(t *testing.T) {
	c := chatController(t)
	testProjectIn(t, c, "proj_f", "f")
	var specs []InferenceSpec
	var mu sync.Mutex
	fakeInferNode(t, c, "n", &specs, &mu)
	m := chatMux(c)
	plain := newChatID(t, chatDo(m, "POST", "/api/chats", `{"text":"plain"}`))
	inProj := newChatID(t, chatDo(m, "POST", "/api/chats", `{"text":"in project","project_id":"proj_f"}`))
	waitChatIdle(t, c.getChat(plain), 2)
	waitChatIdle(t, c.getChat(inProj), 2)
	ids := func(q string) string {
		var list []map[string]any
		json.Unmarshal(chatDo(m, "GET", "/api/chats"+q, "").Body.Bytes(), &list)
		var out []string
		for _, e := range list {
			out = append(out, e["id"].(string))
		}
		return strings.Join(out, ",")
	}
	if got := ids(""); got != plain {
		t.Fatalf("default list %q", got)
	}
	if got := ids("?project=none"); got != plain {
		t.Fatalf("none list %q", got)
	}
	if got := ids("?project=proj_f"); got != inProj {
		t.Fatalf("project list %q", got)
	}
	if got := ids("?project=all"); !strings.Contains(got, plain) || !strings.Contains(got, inProj) {
		t.Fatalf("all list %q", got)
	}
	if len(c.chatSummaries()) != 1 {
		t.Fatalf("sidebar list %v", c.chatSummaries())
	}
	// Move the plain chat into the project and back out.
	if rec := chatDo(m, "POST", "/api/chats/"+plain+"/meta", `{"project_id":"proj_nope"}`); rec.Code != 404 {
		t.Fatalf("move to unknown project %d", rec.Code)
	}
	if rec := chatDo(m, "POST", "/api/chats/"+plain+"/meta", `{"project_id":"proj_f"}`); rec.Code != 200 || c.getChat(plain).ProjectID != "proj_f" {
		t.Fatalf("move in %d %s", rec.Code, rec.Body)
	}
	c.getChat(plain).Node = chatPlanner
	if rec := chatDo(m, "POST", "/api/chats/"+plain+"/meta", `{"project_id":""}`); rec.Code != 200 || c.getChat(plain).ProjectID != "" || c.getChat(plain).Node != "auto" {
		t.Fatalf("move out %d %s node %q", rec.Code, rec.Body, c.getChat(plain).Node)
	}
	// Deleting the project takes its conversations along; a chat of a missing project counts as plain.
	c.getChat(plain).ProjectID = "proj_gone"
	if got := ids(""); got != plain {
		t.Fatalf("orphan not plain: %q", got)
	}
	c.removeProjectChats("proj_f")
	if c.getChat(inProj) != nil || c.getChat(plain) == nil {
		t.Fatal("removeProjectChats")
	}
}

// A project conversation set to "planner" answers with the project's cloud planner through the old
// project-conversation code: tasks it starts carry the chat, the chips and cost are kept, and the
// compat endpoint shows the same conversation.
func TestProjectChatCloudPlanner(t *testing.T) {
	reply := fmt.Sprintf(claudeText, `Starting it.\n[start task]\nAdd a roll function to roll.py.\n[/start task]`)
	c, p, _, bodies := projectWebRig(t, false, reply)
	if p.ConvChatID != "" {
		t.Fatal("test rig started with a chat")
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/projects/"+p.ID+"/chat", strings.NewReader(`{"text":"add a roll function"}`))
	r.SetPathValue("id", p.ID)
	c.handleProjectChat(w, r)
	if w.Code != 200 {
		t.Fatalf("send %d %s", w.Code, w.Body)
	}
	ch := c.projectConvChat(p, false)
	if ch == nil || ch.Node != chatPlanner {
		t.Fatalf("conversation chat %+v", ch)
	}
	// The rig's chat.json held one question; it is the first message of the copy, ahead of ours.
	waitChatIdle(t, ch, 3)
	ch.mu.Lock()
	a := ch.Messages[2]
	ch.mu.Unlock()
	if a.Text != "Starting it." || len(a.Tasks) != 1 || !strings.HasPrefix(a.Node, "Claude") || a.Error != "" {
		t.Fatalf("answer %+v", a)
	}
	c.mu.Lock()
	tk := c.tasks[a.Tasks[0]]
	c.mu.Unlock()
	if tk == nil || tk.ChatID != ch.ID || tk.ProjectID != p.ID || !tk.FromChat {
		t.Fatalf("task %+v", tk)
	}
	if len(bodies()) < 1 || !strings.Contains(systemText(bodies()[0]), "[start task]") {
		t.Fatal("the cloud planner did not get the project text")
	}
	v := c.projectChatViewFor(p)
	if len(v.Messages) != 3 || v.Messages[2].Tasks[0] != tk.ID || v.Busy {
		t.Fatalf("view %+v", v)
	}
	// Wait for the task's own planning calls to settle before the temp folder goes.
	for i := 0; i < 500; i++ {
		tk.mu.Lock()
		st := tk.Status
		tk.mu.Unlock()
		if st != "planning" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The compat endpoints work on the conversation chat: "Start as task" adds the message with its task,
// Stop and Clear act on it.
func TestProjectChatCompatTaskStopClear(t *testing.T) {
	c := chatController(t)
	c.tasks = map[string]*Task{}
	p := testProjectIn(t, c, "proj_c", "c")
	call := func(h http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.SetPathValue("id", p.ID)
		h(w, r)
		return w
	}
	// No planner is set up, so starting a task is refused and nothing is added.
	if w := call(c.handleProjectChat, "POST", "/x", `{"text":"build it","mode":"task"}`); w.Code == 200 {
		t.Fatalf("task without a planner: %s", w.Body)
	}
	ch := c.projectConvChat(p, false)
	if ch == nil || len(ch.Messages) != 0 {
		t.Fatalf("chat %+v", ch)
	}
	// Stop with nothing running, and Clear on a chat with messages.
	if w := call(c.handleProjectChatStop, "POST", "/x", ""); w.Code != 200 {
		t.Fatalf("stop %d", w.Code)
	}
	ch.mu.Lock()
	ch.Messages = []ChatMsg{{ID: "m1", Role: "user", Text: "hi", At: time.Now()}}
	ch.Busy = "answering"
	ch.mu.Unlock()
	if w := call(c.handleProjectChatClear, "DELETE", "/x", ""); w.Code != 409 {
		t.Fatalf("clear while busy %d", w.Code)
	}
	if w := call(c.handleProjectChat, "POST", "/x", `{"text":"again"}`); w.Code != 409 {
		t.Fatalf("send while busy %d", w.Code)
	}
	canceled := false
	ch.mu.Lock()
	ch.Busy, ch.cancel = "answering", func() { canceled = true }
	ch.mu.Unlock()
	call(c.handleProjectChatStop, "POST", "/x", "")
	if !canceled {
		t.Fatal("stop did not cancel the answer")
	}
	ch.mu.Lock()
	ch.Busy = ""
	ch.mu.Unlock()
	if w := call(c.handleProjectChatClear, "DELETE", "/x", ""); w.Code != 200 || len(c.projectChatViewFor(p).Messages) != 0 {
		t.Fatalf("clear %d", w.Code)
	}
}
