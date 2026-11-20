package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMCPServer(t *testing.T) {
	c, _, _, _ := compatMux(t)
	c.tasks = map[string]*Task{}
	c.apiTokens = []apiToken{{ID: "a", Hash: hashAPIToken("mcp"), Scope: "mcp"}, {ID: "b", Hash: hashAPIToken("chat"), Scope: "chat"}}
	m := http.NewServeMux()
	c.mcpRoutes(m)
	auth := map[string]string{"Authorization": "Bearer mcp", "Content-Type": "application/json"}
	call := func(body string) map[string]any {
		rec := send(m, "POST", "/mcp", body, auth)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
		}
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	tool := func(name, args string) (string, bool) {
		r := call(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`)
		res := r["result"].(map[string]any)
		return res["content"].([]any)[0].(map[string]any)["text"].(string), res["isError"].(bool)
	}
	if rec := send(m, "POST", "/mcp", `{}`, map[string]string{"Authorization": "Bearer chat"}); rec.Code != 401 {
		t.Fatalf("a chat token must not reach MCP: %d", rec.Code)
	}
	init := call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	if init["result"].(map[string]any)["protocolVersion"] != "2025-03-26" {
		t.Fatalf("init %v", init)
	}
	if rec := send(m, "POST", "/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, auth); rec.Code != 202 {
		t.Fatalf("notification %d", rec.Code)
	}
	list := call(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if n := len(list["result"].(map[string]any)["tools"].([]any)); n != 8 {
		t.Fatalf("%d tools", n)
	}
	if r := call(`{"jsonrpc":"2.0","id":3,"method":"nope"}`); r["error"] == nil {
		t.Fatal("unknown method")
	}
	if txt, isErr := tool("cluster_status", `{}`); isErr || !strings.Contains(txt, "gpu-box: m (free)") {
		t.Fatalf("status %q", txt)
	}
	if txt, isErr := tool("ask_local_model", `{"prompt":"hi there"}`); isErr || !strings.HasPrefix(txt, "echo: hi there") {
		t.Fatalf("ask %q", txt)
	}
	if txt, isErr := tool("start_task", `{"prompt":"x","planner":"claude"}`); !isErr || !strings.Contains(txt, "local planner") {
		t.Fatalf("paid planner must be refused by default: %q", txt)
	}
	if txt, isErr := tool("approve_plan", `{"task_id":"t1"}`); !isErr || !strings.Contains(txt, "web UI") {
		t.Fatalf("approve must wait for the owner by default: %q", txt)
	}
	c.tasks["t1"] = &Task{ID: "t1", Title: "Build it", Status: "awaiting_answers", Created: time.Now(),
		Messages: []TaskMessage{{Kind: "questions", Questions: []Question{{ID: "q1", Question: "Which language?", Options: []string{"Go", "Python"}}}}}}
	if txt, _ := tool("task_status", `{"task_id":"t1"}`); !strings.Contains(txt, "[q1] Which language? Options: Go | Python") {
		t.Fatalf("task status %q", txt)
	}
	if txt, _ := tool("list_tasks", `{}`); !strings.Contains(txt, "t1  awaiting_answers  Build it") {
		t.Fatalf("list %q", txt)
	}
	if txt, isErr := tool("get_files", `{"task_id":"nope"}`); !isErr || txt != "no such task" {
		t.Fatalf("files %q", txt)
	}
	// Batches answer each request and skip notifications.
	rec := send(m, "POST", "/mcp", `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/x"}]`, auth)
	var batch []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &batch)
	if len(batch) != 1 {
		t.Fatalf("batch %s", rec.Body)
	}
}
