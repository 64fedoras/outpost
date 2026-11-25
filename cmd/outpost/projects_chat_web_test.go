package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// projectWebRig makes a project whose planner is a stubbed Claude, with the
// DuckDuckGo stub answering searches. replies are the Claude responses in order
// (the last one repeats); it returns the request bodies seen.
func projectWebRig(t *testing.T, webOn bool, replies ...string) (*Controller, *Project, *projectChat, func() []map[string]any) {
	t.Helper()
	useSearchStubs(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, ddgPage) }))
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		i := min(len(bodies), len(replies)-1)
		bodies = append(bodies, b)
		mu.Unlock()
		if strings.HasPrefix(replies[i], "!") {
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error":{"message":%q}}`, replies[i][1:])
			return
		}
		fmt.Fprint(w, replies[i])
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OUTPOST_ANTHROPIC_URL", srv.URL)
	c := testController(t)
	enc, err := c.encrypt("sk-test")
	if err != nil {
		t.Fatal(err)
	}
	c.settings.AnthropicKeyEnc = enc
	c.settings.DefaultPlanner = "claude"
	c.settings.ChatWebSearch = webOn
	w := httptest.NewRecorder()
	c.handleCreateProject(w, httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"web"}`)))
	var created struct{ ID string }
	json.Unmarshal(w.Body.Bytes(), &created)
	p := c.projects[created.ID]
	pc := p.chatLog()
	pc.add(ProjectChatMsg{Role: "user", Text: "build a replacement to hermes ai"})
	return c, p, pc, func() []map[string]any { mu.Lock(); defer mu.Unlock(); return bodies }
}

func toolNames(b map[string]any) []string {
	var out []string
	tools, _ := b["tools"].([]any)
	for _, t := range tools {
		out = append(out, fmt.Sprint(t.(map[string]any)["name"]))
	}
	return out
}

func systemText(b map[string]any) string {
	raw, _ := json.Marshal(b["system"])
	return string(raw)
}

const claudeText = `{"content":[{"type":"text","text":"%s"}],"usage":{"input_tokens":10,"output_tokens":5}}`

func TestProjectChatSearchesTheWebFirst(t *testing.T) {
	c, p, pc, bodies := projectWebRig(t, true,
		`{"content":[{"type":"tool_use","id":"t1","name":"web_search","input":{"query":"hermes ai"}}],"usage":{"input_tokens":10,"output_tokens":5}}`,
		fmt.Sprintf(claudeText, "Found it."))
	msg := c.answerProjectChat(context.Background(), p, pc)
	if msg.Error != "" || msg.Text != "Found it." {
		t.Fatalf("msg %+v", msg)
	}
	bs := bodies()
	if len(bs) != 2 {
		t.Fatalf("%d requests", len(bs))
	}
	if names := strings.Join(toolNames(bs[0]), ","); !strings.Contains(names, "web_search") || !strings.Contains(names, "read_page") || !strings.Contains(names, "read_file") {
		t.Fatalf("tools offered: %s", names)
	}
	if s := systemText(bs[0]); !strings.Contains(s, "look it up first") || !strings.Contains(s, "Never ask the owner what a public product") {
		t.Fatalf("system prompt lacks the look-up rule: %s", s)
	}
	raw, _ := json.Marshal(bs[1]["messages"])
	if !strings.Contains(string(raw), "go.dev/doc/devel/release") {
		t.Fatalf("search results not sent back: %s", raw)
	}
	if strings.Join(msg.Reads, ",") != `Searched the web for "hermes ai"` {
		t.Fatalf("reads %q", msg.Reads)
	}
}

func TestProjectChatWebOff(t *testing.T) {
	c, p, pc, bodies := projectWebRig(t, false, fmt.Sprintf(claudeText, "What is it?"))
	c.answerProjectChat(context.Background(), p, pc)
	bs := bodies()
	if names := strings.Join(toolNames(bs[0]), ","); strings.Contains(names, "web_search") {
		t.Fatalf("web tools offered with web search off: %s", names)
	}
	if s := systemText(bs[0]); !strings.Contains(s, "web search is off") {
		t.Fatalf("system prompt does not say web search is off: %s", s)
	}
	// A web_search call the model makes anyway is refused.
	pr := newProjectReads(t.TempDir())
	if out := pr.run(toolCall{ID: "1", Name: "web_search", Args: `{"query":"x"}`}, 1000); !strings.Contains(out, "no such tool") {
		t.Fatalf("web_search ran with web off: %s", out)
	}
}

func TestProjectChatSearchOnceWithoutTools(t *testing.T) {
	c, p, pc, bodies := projectWebRig(t, true, "!tools are not supported by this model", fmt.Sprintf(claudeText, "Hermes is a model family."))
	msg := c.answerProjectChat(context.Background(), p, pc)
	if msg.Text != "Hermes is a model family." {
		t.Fatalf("msg %+v", msg)
	}
	bs := bodies()
	if len(bs) != 2 || bs[1]["tools"] != nil {
		t.Fatalf("fallback request: %d %v", len(bs), bs[len(bs)-1]["tools"])
	}
	raw, _ := json.Marshal(bs[1]["messages"])
	if !strings.Contains(string(raw), "Web search results for the owner") || !strings.Contains(string(raw), "go.dev/doc/devel/release") {
		t.Fatalf("no search results in front of the model: %s", raw)
	}
	if len(msg.Reads) != 1 || !strings.HasPrefix(msg.Reads[0], "Searched the web once for") {
		t.Fatalf("reads %q", msg.Reads)
	}
}
