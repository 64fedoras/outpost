package main

// Outpost as an MCP server: POST /mcp speaks the Model Context Protocol
// (Streamable HTTP transport, JSON responses, no server-sent stream), so
// Claude Code, Claude Desktop and other MCP clients can hand work to the
// cluster: start a task, answer its questions, approve its plan, read its
// files, ask a local model, see the cluster. Auth is an API token with full
// access or the "mcp" scope (Settings → API tokens).
//
// Safety defaults: tasks started this way use local planners only unless
// Settings allow paid planners (mcp_allow_paid), and plans wait for approval
// in the web UI unless Settings let the tool approve (mcp_allow_approve).
// The tools call the same handlers the web UI uses, through an in-process
// mux, so validation and side effects stay in one place.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"time"
)

var mcpProtocols = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

func (c *Controller) mcpRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /mcp", c.authMCP(c.handleMCP))
	mux.HandleFunc("GET /mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST")
		writeJSON(w, 405, map[string]string{"error": "this MCP server answers POST requests only (no event stream)"})
	})
	mux.HandleFunc("DELETE /mcp", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
}

// authMCP accepts a browser session, a full-access token or an "mcp" token.
func (c *Controller) authMCP(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok := false
		c.mu.Lock()
		if ck, err := r.Cookie("outpost_session"); err == nil {
			if exp, found := c.sessions[ck.Value]; found && time.Now().Before(exp) {
				ok = true
			}
		}
		if i := c.bearerAPIToken(r); !ok && i >= 0 && (c.apiTokens[i].Scope == "" || c.apiTokens[i].Scope == "mcp") {
			ok = true
			if time.Since(c.apiTokens[i].LastUsed) > time.Minute {
				c.apiTokens[i].LastUsed = time.Now()
				c.saveAPITokensLocked()
			}
		}
		c.mu.Unlock()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="outpost"`)
			writeJSON(w, 401, map[string]string{"error": "missing or unknown API token (Settings → API tokens, tick MCP)"})
			return
		}
		h(w, r)
	}
}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Controller) handleMCP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		writeJSON(w, 400, rpcResp{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcErr{-32700, "could not read the request"}})
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' { // a batch (2025-03-26)
		var reqs []rpcReq
		if json.Unmarshal(body, &reqs) != nil {
			writeJSON(w, 400, rpcResp{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcErr{-32700, "parse error"}})
			return
		}
		var out []rpcResp
		for _, q := range reqs {
			if res := c.mcpOne(r.Context(), q); res != nil {
				out = append(out, *res)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(202)
			return
		}
		writeJSON(w, 200, out)
		return
	}
	var q rpcReq
	if json.Unmarshal(body, &q) != nil {
		writeJSON(w, 400, rpcResp{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcErr{-32700, "parse error"}})
		return
	}
	res := c.mcpOne(r.Context(), q)
	if res == nil { // a notification: accepted, no body
		w.WriteHeader(202)
		return
	}
	writeJSON(w, 200, res)
}

// mcpOne answers one JSON-RPC message; nil for notifications.
func (c *Controller) mcpOne(ctx context.Context, q rpcReq) *rpcResp {
	if len(q.ID) == 0 || string(q.ID) == "null" {
		return nil
	}
	res := &rpcResp{JSONRPC: "2.0", ID: q.ID}
	switch q.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(q.Params, &p)
		ver := mcpProtocols[0]
		for _, v := range mcpProtocols {
			if v == p.ProtocolVersion {
				ver = v
			}
		}
		res.Result = map[string]any{"protocolVersion": ver, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":   map[string]string{"name": "outpost", "title": "Outpost cluster", "version": Version},
			"instructions": "Outpost runs coding tasks on the user's own machines with local models: start_task, then poll task_status until it is done, answer questions with answer_task, and fetch results with get_files. ask_local_model asks one question of a local model."}
	case "ping":
		res.Result = map[string]any{}
	case "tools/list":
		res.Result = map[string]any{"tools": mcpTools()}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(q.Params, &p); err != nil {
			res.Error = &rpcErr{-32602, "bad params"}
			return res
		}
		text, isErr := c.mcpTool(ctx, p.Name, p.Arguments)
		res.Result = map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": isErr}
	case "resources/list":
		res.Result = map[string]any{"resources": []any{}}
	case "prompts/list":
		res.Result = map[string]any{"prompts": []any{}}
	default:
		res.Error = &rpcErr{-32601, "method not found: " + q.Method}
	}
	return res
}

func mcpSchema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func mcpStr(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func mcpTools() []map[string]any {
	ro := map[string]any{"readOnlyHint": true}
	return []map[string]any{
		{"name": "cluster_status", "description": "Nodes online, the model each has loaded and whether it is busy, and how many tasks are running or waiting.", "inputSchema": mcpSchema(map[string]any{}), "annotations": ro},
		{"name": "list_tasks", "description": "The most recent tasks with their status.", "inputSchema": mcpSchema(map[string]any{"limit": map[string]any{"type": "integer", "description": "How many (default 10, at most 50)"}}), "annotations": ro},
		{"name": "start_task", "description": "Start a coding task on the cluster. A planner splits it into subtasks that nodes write in parallel, then tests and fixes them. Returns the task id; poll task_status. The planner may ask questions first (answer with answer_task), and the plan may need approval.",
			"inputSchema": mcpSchema(map[string]any{"prompt": mcpStr("What to build or change, in full"), "project": mcpStr("Optional project id or name to work in"),
				"planner": mcpStr("Optional: 'auto' (a local model, the default), 'node:<id>', or 'claude'/'openrouter' if Outpost allows paid planners for MCP")}, "prompt")},
		{"name": "task_status", "description": "Status of a task: questions waiting for answers, the plan, subtask progress, test results and the result note.", "inputSchema": mcpSchema(map[string]any{"task_id": mcpStr("Task id")}, "task_id"), "annotations": ro},
		{"name": "answer_task", "description": "Answer the planner's questions, or send a follow-up or change request to a task. answers maps question ids to the chosen option or free text.",
			"inputSchema": mcpSchema(map[string]any{"task_id": mcpStr("Task id"), "text": mcpStr("A note, or the follow-up request"), "answers": map[string]any{"type": "object", "description": "Question id → answer", "additionalProperties": map[string]any{"type": "string"}}}, "task_id")},
		{"name": "approve_plan", "description": "Approve a task's plan so the nodes start writing. Only works if Outpost's settings allow MCP to approve; otherwise the owner approves in the web UI.", "inputSchema": mcpSchema(map[string]any{"task_id": mcpStr("Task id")}, "task_id")},
		{"name": "get_files", "description": "Without path: the list of the task's files. With path: that file's text (up to 200 KB).", "inputSchema": mcpSchema(map[string]any{"task_id": mcpStr("Task id"), "path": mcpStr("Optional file path from the list")}, "task_id"), "annotations": ro},
		{"name": "ask_local_model", "description": "Ask one question of a local model on the cluster (free). Good for second opinions, summaries or bulk text work.", "inputSchema": mcpSchema(map[string]any{"prompt": mcpStr("The question"), "model": mcpStr("Optional model name from cluster_status; default the strongest free node")}, "prompt")},
	}
}

// mcpLocal calls one of the controller's own handlers in process (no sign-in check).
func (c *Controller) mcpLocal(method, path string, body any) (int, []byte) {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.mcpMux().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (c *Controller) mcpMux() *http.ServeMux {
	c.mcpMuxOnce.Do(func() {
		m := http.NewServeMux()
		m.HandleFunc("POST /api/tasks", c.handleCreateTask)
		m.HandleFunc("GET /api/tasks/{id}", c.handleGetTask)
		m.HandleFunc("POST /api/tasks/{id}/approve", c.handleApproveTask)
		m.HandleFunc("POST /api/tasks/{id}/messages", c.handleTaskMessage)
		m.HandleFunc("POST /api/tasks/{id}/answers", c.handleTaskAnswers)
		m.HandleFunc("GET /api/tasks/{id}/file", c.handleTaskFile)
		c.mcpMuxV = m
	})
	return c.mcpMuxV
}

func errText(b []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(b))
}

func argStr(a map[string]any, k string) string {
	s, _ := a[k].(string)
	return strings.TrimSpace(s)
}

func (c *Controller) mcpLink(path string) string {
	c.mu.Lock()
	base := strings.TrimRight(strings.TrimSpace(c.settings.Notify.PublicURL), "/")
	if base == "" {
		base = strings.TrimRight(c.advertise, "/")
	}
	c.mu.Unlock()
	return base + "/#" + path
}

// mcpTool runs one tool; the bool is MCP's isError.
func (c *Controller) mcpTool(ctx context.Context, name string, a map[string]any) (string, bool) {
	c.mu.Lock()
	allowPaid, allowApprove := c.settings.MCPAllowPaid, c.settings.MCPAllowApprove
	c.mu.Unlock()
	switch name {
	case "cluster_status":
		return c.mcpClusterStatus(), false
	case "list_tasks":
		n := 10
		if f, ok := a["limit"].(float64); ok && f > 0 {
			n = min(int(f), 50)
		}
		return c.mcpListTasks(n), false
	case "start_task":
		prompt := argStr(a, "prompt")
		if prompt == "" {
			return "prompt is required", true
		}
		planner := firstNonEmpty(argStr(a, "planner"), "auto")
		if !allowPaid && (planner == "claude" || planner == "openrouter" || planner == "auto_api") {
			return "Outpost only lets MCP start tasks with a local planner (Settings → API tokens → MCP allows paid planners is off). Use planner 'auto'.", true
		}
		req := map[string]any{"prompt": prompt, "planner": planner}
		if p := argStr(a, "project"); p != "" {
			id := c.projectIDByName(p)
			if id == "" {
				return "no project called " + p, true
			}
			req["project"] = id
		}
		code, b := c.mcpLocal("POST", "/api/tasks", req)
		if code != 200 {
			return "could not start the task: " + errText(b), true
		}
		var r struct{ ID string }
		json.Unmarshal(b, &r)
		return fmt.Sprintf("Started task %s. Poll task_status with this id; the planner may ask questions first. Web UI: %s", r.ID, c.mcpLink("/tasks/"+r.ID)), false
	case "task_status":
		return c.mcpTaskStatus(argStr(a, "task_id"))
	case "answer_task":
		id := argStr(a, "task_id")
		t := c.getTask(id)
		if t == nil {
			return "no such task", true
		}
		t.mu.Lock()
		status := t.Status
		t.mu.Unlock()
		text := argStr(a, "text")
		if status == "awaiting_answers" {
			ans := map[string][]string{}
			if m, ok := a["answers"].(map[string]any); ok {
				for k, v := range m {
					if s, ok := v.(string); ok && s != "" {
						ans[k] = []string{s}
					}
				}
			}
			code, b := c.mcpLocal("POST", "/api/tasks/"+url.PathEscape(id)+"/answers", map[string]any{"answers": ans, "note": text})
			if code != 200 {
				return "answers refused: " + errText(b), true
			}
			return "Answers sent; the planner continues. Poll task_status.", false
		}
		if text == "" {
			return "the task is not waiting for answers; send text to give it a follow-up request", true
		}
		code, b := c.mcpLocal("POST", "/api/tasks/"+url.PathEscape(id)+"/messages", map[string]any{"text": text})
		if code != 200 {
			return "message refused: " + errText(b), true
		}
		return "Sent to the task. Poll task_status.", false
	case "approve_plan":
		id := argStr(a, "task_id")
		if !allowApprove {
			return "Plans started from MCP wait for the owner's approval in the web UI: " + c.mcpLink("/tasks/"+id) + " (Settings can let MCP approve). Poll task_status.", true
		}
		code, b := c.mcpLocal("POST", "/api/tasks/"+url.PathEscape(id)+"/approve", map[string]any{})
		if code != 200 {
			return "could not approve: " + errText(b), true
		}
		return "Plan approved; the nodes are writing. Poll task_status.", false
	case "get_files":
		id, path := argStr(a, "task_id"), argStr(a, "path")
		t := c.getTask(id)
		if t == nil {
			return "no such task", true
		}
		if path == "" {
			files := listRepo(t.repoDir())
			if len(files) == 0 {
				return "No files yet.", false
			}
			var sb strings.Builder
			for _, f := range files {
				fmt.Fprintf(&sb, "%s (%d bytes)\n", f.Path, f.Bytes)
			}
			fmt.Fprintf(&sb, "\nZip of everything: %s", c.mcpLink("/tasks/"+id))
			return sb.String(), false
		}
		code, b := c.mcpLocal("GET", "/api/tasks/"+url.PathEscape(id)+"/file?path="+url.QueryEscape(path), nil)
		if code != 200 {
			return "could not read " + path + ": " + errText(b), true
		}
		if bytes.IndexByte(b, 0) >= 0 {
			return path + " is a binary file; download it from the web UI: " + c.mcpLink("/tasks/"+id), true
		}
		text := string(b)
		if len(text) > 200<<10 {
			text = text[:200<<10] + "\n…(cut at 200 KB)"
		}
		return text, false
	case "ask_local_model":
		prompt := argStr(a, "prompt")
		if prompt == "" {
			return "prompt is required", true
		}
		run, _, err := c.apiAcquire(ctx, argStr(a, "model"))
		if err != nil {
			return err.Error(), true
		}
		defer run.release()
		out, err := c.apiInfer(ctx, run, InferenceSpec{Messages: []ChatMessage{{Role: "system", Content: chatBaseSystem}, {Role: "user", Content: prompt}}}, "MCP: ask", nil)
		if err != nil {
			return err.Error(), true
		}
		return out.Text + "\n\n(" + run.model + " on " + c.nodeLabel(run.node) + ")", false
	}
	return "unknown tool " + name, true
}

func (c *Controller) projectIDByName(p string) string {
	if c.getProject(p) != nil {
		return p
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, pr := range c.projects {
		if strings.EqualFold(pr.Name, p) {
			return id
		}
	}
	return ""
}

func (c *Controller) mcpClusterStatus() string {
	var sb strings.Builder
	nodes := c.workerNodes()
	fmt.Fprintf(&sb, "%d node(s) online.\n", len(nodes))
	for _, id := range nodes {
		c.mu.Lock()
		hb := c.lastHB[id]
		busy := c.reserved[id] > 0
		c.mu.Unlock()
		model, state := "no model loaded", "free"
		if hb != nil && hb.LoadedModel != "" {
			model = hb.LoadedModel
		}
		if busy || (hb != nil && hb.InferenceBusy > 0) {
			state = "busy"
		}
		if hb != nil && hb.Paused {
			state = "paused"
		}
		fmt.Fprintf(&sb, "- %s: %s (%s)\n", c.nodeLabel(id), model, state)
	}
	counts := map[string]int{}
	for _, t := range c.allTasks() {
		t.mu.Lock()
		counts[t.Status]++
		t.mu.Unlock()
	}
	var keys []string
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
	}
	fmt.Fprintf(&sb, "Tasks: %s.", strings.Join(parts, ", "))
	return sb.String()
}

func (c *Controller) mcpListTasks(n int) string {
	type row struct {
		id, title, status string
		at                time.Time
	}
	var rows []row
	for _, t := range c.allTasks() {
		t.mu.Lock()
		rows = append(rows, row{t.ID, t.Title, t.Status, t.Created})
		t.mu.Unlock()
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].at.After(rows[j].at) })
	if len(rows) == 0 {
		return "No tasks yet."
	}
	var sb strings.Builder
	for i, r := range rows {
		if i >= n {
			break
		}
		fmt.Fprintf(&sb, "%s  %s  %s\n", r.id, r.status, r.title)
	}
	return sb.String()
}

func (c *Controller) mcpTaskStatus(id string) (string, bool) {
	t := c.getTask(id)
	if t == nil {
		return "no such task", true
	}
	var sb strings.Builder
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintf(&sb, "Task %s: %s\nStatus: %s\n", t.ID, t.Title, t.Status)
	if t.Error != "" {
		fmt.Fprintf(&sb, "Error: %s\n", t.Error)
	}
	if t.Status == "awaiting_answers" {
		for i := len(t.Messages) - 1; i >= 0; i-- {
			if m := t.Messages[i]; m.Kind == "questions" {
				sb.WriteString("The planner asks (answer with answer_task, answers keyed by id):\n")
				for _, q := range m.Questions {
					fmt.Fprintf(&sb, "- [%s] %s", q.ID, q.Question)
					if len(q.Options) > 0 {
						fmt.Fprintf(&sb, " Options: %s", strings.Join(q.Options, " | "))
					}
					sb.WriteString("\n")
				}
				break
			}
		}
	}
	if t.Plan != nil {
		fmt.Fprintf(&sb, "Plan: %s\n", firstNonEmpty(t.Plan.Summary, t.Plan.Title))
		for i, s := range t.Plan.Subtasks {
			state := ""
			if i < len(t.Subtasks) && t.Subtasks[i] != nil {
				state = " [" + t.Subtasks[i].Status + "]"
			}
			fmt.Fprintf(&sb, "  %d. %s%s\n", i+1, s.Title, state)
		}
	}
	if t.Status == "awaiting_approval" {
		sb.WriteString("The plan waits for approval (approve_plan, or the owner in the web UI).\n")
	}
	if t.TestStatus != "" {
		fmt.Fprintf(&sb, "Tests: %s\n", t.TestStatus)
	}
	for i := len(t.Messages) - 1; i >= 0; i-- {
		if m := t.Messages[i]; m.Kind == "result" {
			fmt.Fprintf(&sb, "Result: %s\n", m.Text)
			if len(m.Files) > 0 {
				fmt.Fprintf(&sb, "Files changed: %s\n", strings.Join(m.Files, ", "))
			}
			break
		}
	}
	fmt.Fprintf(&sb, "Web UI: %s", c.mcpLink("/tasks/"+t.ID))
	return sb.String(), false
}

// allTasks copies the task list so each task can be locked without c.mu held.
func (c *Controller) allTasks() []*Task {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*Task, 0, len(c.tasks))
	for _, t := range c.tasks {
		out = append(out, t)
	}
	return out
}
