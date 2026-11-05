package main

// Agent mode (roadmap row 18). A fixer or an investigating worker gets tools
// and works in a loop: read a failing test, open the file it points at, try a
// change, run again. The controller owns the loop and the project files; the
// model only sees tool results. `run` goes to an exec node (chosen with the
// toolchain inventory), and `ask_user` posts a question in the thread and
// pauses only the agent that asked.
//
// The same loop drives three kinds of backend: a node's llama-server (started
// with --jinja, OpenAI-style tool calls), the Claude API (tool_use blocks) and
// OpenRouter (OpenAI-style tool calls). llmRequest carries neutral messages and
// each backend converts them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	maxAgentCalls  = 30
	maxAgentTime   = 20 * time.Minute // active time; waiting for the user does not count
	agentRunLimit  = 5 * time.Minute  // one `run` command
	agentResultCap = 24000            // characters of one tool result for cloud models
)

// errAgentUnsupported means the model or server refused tools, so the caller
// falls back to a single-shot request.
var errAgentUnsupported = errors.New("tool calling is not supported here")

// ---------- neutral and wire types ----------

type agentTool struct {
	Name        string
	Description string
	Params      map[string]any // JSON schema
}

type toolCall struct {
	ID   string
	Name string
	Args string // JSON object as text
}

// agentMsg is one turn of the loop. Role is user, assistant or tool.
type agentMsg struct {
	Role   string
	Text   string
	Calls  []toolCall // assistant: the tools it asked for
	CallID string     // tool: which call this answers
}

// ToolDef, WireToolCall are the OpenAI shapes used between the controller, the
// node and llama-server.
type ToolDef struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type WireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func toolDefs(tools []agentTool) []ToolDef {
	out := make([]ToolDef, len(tools))
	for i, t := range tools {
		out[i].Type = "function"
		out[i].Function.Name, out[i].Function.Description, out[i].Function.Parameters = t.Name, t.Description, t.Params
	}
	return out
}

func wireCalls(calls []toolCall) []WireToolCall {
	out := make([]WireToolCall, len(calls))
	for i, c := range calls {
		out[i].ID, out[i].Type = c.ID, "function"
		out[i].Function.Name, out[i].Function.Arguments = c.Name, c.Args
	}
	return out
}

func fromWireCalls(in []WireToolCall) []toolCall {
	var out []toolCall
	for i, w := range in {
		id := w.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		args := strings.TrimSpace(w.Function.Arguments)
		if args == "" {
			args = "{}"
		}
		out = append(out, toolCall{ID: id, Name: w.Function.Name, Args: args})
	}
	return out
}

// openAIMessages converts neutral messages to the OpenAI chat format.
func openAIMessages(msgs []agentMsg) []ChatMessage {
	var out []ChatMessage
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			out = append(out, ChatMessage{Role: "assistant", Content: m.Text, ToolCalls: wireCalls(m.Calls)})
		case "tool":
			out = append(out, ChatMessage{Role: "tool", Content: m.Text, ToolCallID: m.CallID})
		default:
			out = append(out, ChatMessage{Role: "user", Content: m.Text})
		}
	}
	return out
}

// claudeMessages converts neutral messages to Messages API content blocks.
// Consecutive tool results share one user turn, as the API requires. The last
// block is marked for caching so a long loop does not pay for the whole
// conversation again on every step.
func claudeMessages(msgs []agentMsg) []map[string]any {
	var out []map[string]any
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			var blocks []map[string]any
			if strings.TrimSpace(m.Text) != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Text})
			}
			for _, c := range m.Calls {
				var in json.RawMessage = json.RawMessage(c.Args)
				if !json.Valid(in) {
					in = json.RawMessage("{}")
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": in})
			}
			out = append(out, map[string]any{"role": "assistant", "content": blocks})
		case "tool":
			blk := map[string]any{"type": "tool_result", "tool_use_id": m.CallID, "content": m.Text}
			if n := len(out); n > 0 && out[n-1]["role"] == "user" {
				if prev, ok := out[n-1]["content"].([]map[string]any); ok && len(prev) > 0 && prev[0]["type"] == "tool_result" {
					out[n-1]["content"] = append(prev, blk)
					continue
				}
			}
			out = append(out, map[string]any{"role": "user", "content": []map[string]any{blk}})
		default:
			out = append(out, map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": m.Text}}})
		}
	}
	if n := len(out); n > 0 {
		if blocks, ok := out[n-1]["content"].([]map[string]any); ok && len(blocks) > 0 {
			blocks[len(blocks)-1]["cache_control"] = map[string]string{"type": "ephemeral"}
		}
	}
	return out
}

func claudeTools(tools []agentTool) []map[string]any {
	var out []map[string]any
	for _, t := range tools {
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Params})
	}
	return out
}

// size is the request's input length in characters, for cost estimates.
func (r llmRequest) size() int {
	n := len(r.System) + len(r.User) + imageTokenChars*len(r.Images)
	for _, m := range r.Msgs {
		n += len(m.Text)
		for _, c := range m.Calls {
			n += len(c.Args) + len(c.Name)
		}
	}
	return n
}

// toolsUnsupported recognises the errors servers give when they cannot do
// tool calling, so the fixer falls back to a single-shot request.
func toolsUnsupported(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "jinja") || (strings.Contains(s, "tool") && (strings.Contains(s, "support") || strings.Contains(s, "400")))
}

// ---------- tools ----------

func objSchema(required []string, props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

var (
	toolReadFile = agentTool{"read_file", "Read a text file from the project. Use start_line and end_line to read part of a large file.",
		objSchema([]string{"path"}, map[string]any{"path": str("path relative to the project root"),
			"start_line": map[string]any{"type": "integer", "description": "first line to return (1-based, optional)"},
			"end_line":   map[string]any{"type": "integer", "description": "last line to return (optional)"}})}
	toolListFiles = agentTool{"list_files", "List the project's files with their sizes, optionally only below one directory.",
		objSchema(nil, map[string]any{"dir": str("directory relative to the project root (optional)")})}
	toolSearch = agentTool{"search", "Search the project's text files for a regular expression (or plain text). Returns file:line: text.",
		objSchema([]string{"pattern"}, map[string]any{"pattern": str("regular expression or plain text"), "dir": str("limit to this directory (optional)")})}
	toolWriteFile = agentTool{"write_file", "Create or replace a project file with its complete new content.",
		objSchema([]string{"path", "content"}, map[string]any{"path": str("path relative to the project root"), "content": str("the complete file content")})}
	toolRun = agentTool{"run", "Run a shell command on a build node with a fresh copy of the project, and return its exit code and output. Nothing persists between runs, so chain steps with && (for example the setup command, then the tests).",
		objSchema([]string{"command"}, map[string]any{"command": str("the command line")})}
	toolAskUser = agentTool{"ask_user", "Ask the person who started this task a question when you cannot continue without a decision. Work on other parts continues while you wait.",
		objSchema([]string{"question"}, map[string]any{"question": str("one short question"),
			"options": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "suggested answers (optional)"}})}
)

// agentRun is one tool-using conversation.
type agentRun struct {
	c       *Controller
	t       *Task
	label   string
	system  string
	user    string
	tools   []agentTool
	step    func(ctx context.Context, req llmRequest) (*llmResponse, error)
	subtask string // subtask id, "" for a task-level fixer
	resCap  int    // characters per tool result
	budget  int    // characters of conversation kept
	maxOut  int

	msgs    []agentMsg
	Written []string
	Calls   int
	Summary string
	Stopped string // "" when the model finished by itself, else why it was stopped
	active  time.Duration
}

// execute dispatches one tool call and returns the text the model sees.
func (a *agentRun) execute(ctx context.Context, call toolCall) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Args), &args); err != nil {
		return "error: the arguments are not a JSON object: " + err.Error()
	}
	has := func(name string) bool {
		for _, t := range a.tools {
			if t.Name == name {
				return true
			}
		}
		return false
	}
	if !has(call.Name) {
		return "error: no such tool " + call.Name
	}
	s := func(k string) string { v, _ := args[k].(string); return v }
	n := func(k string) int {
		switch v := args[k].(type) {
		case float64:
			return int(v)
		case string:
			var i int
			fmt.Sscan(v, &i)
			return i
		}
		return 0
	}
	repo := a.t.repoDir()
	switch call.Name {
	case "read_file":
		return a.readFile(repo, strings.TrimPrefix(s("path"), "./"), n("start_line"), n("end_line"))
	case "list_files":
		return listFilesText(repo, strings.Trim(strings.TrimPrefix(s("dir"), "./"), "/"))
	case "search":
		return searchText(repo, s("pattern"), strings.Trim(strings.TrimPrefix(s("dir"), "./"), "/"))
	case "write_file":
		p := strings.TrimPrefix(strings.TrimSpace(s("path")), "./")
		content := s("content")
		if p == "" {
			return "error: path is required"
		}
		if len(content) > maxRepoFileSize {
			return "error: file too large"
		}
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		if err := a.t.writeFile(p, content); err != nil {
			return "error: " + err.Error()
		}
		a.t.touch(p)
		seen := false
		for _, w := range a.Written {
			seen = seen || w == p
		}
		if !seen {
			a.Written = append(a.Written, p)
		}
		return fmt.Sprintf("wrote %s (%d bytes)", p, len(content))
	case "run":
		return a.run(ctx, strings.TrimSpace(s("command")))
	case "ask_user":
		var opts []string
		if arr, ok := args["options"].([]any); ok {
			for _, o := range arr {
				if v, ok := o.(string); ok && strings.TrimSpace(v) != "" {
					opts = append(opts, strings.TrimSpace(v))
				}
			}
		}
		ans, err := a.ask(ctx, s("question"), opts)
		if err != nil {
			return "error: " + err.Error()
		}
		return "The user answered: " + ans
	}
	return "error: no such tool " + call.Name
}

func (a *agentRun) readFile(repo, rel string, from, to int) string {
	p, err := safeJoin(repo, rel)
	if err != nil {
		return "error: " + err.Error()
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return "error: no such file " + rel + " (use list_files)"
	}
	if st.Size() > 4*maxRepoFileSize {
		return "error: file is too large to read"
	}
	data, _ := os.ReadFile(p)
	if bytesLookBinary(data) {
		return "error: binary file"
	}
	text := string(data)
	if from > 0 || to > 0 {
		lines := strings.Split(text, "\n")
		if from < 1 {
			from = 1
		}
		if to < from || to > len(lines) {
			to = len(lines)
		}
		if from > len(lines) {
			return fmt.Sprintf("error: the file has %d lines", len(lines))
		}
		text = strings.Join(lines[from-1:to], "\n")
		text = fmt.Sprintf("(lines %d-%d of %d)\n", from, to, len(lines)) + text
	}
	return clipText(text, a.resCap)
}

func bytesLookBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

func clipText(s string, max int) string {
	if max > 0 && len(s) > max {
		return s[:max] + fmt.Sprintf("\n…(cut after %d characters; ask for a smaller range)", max)
	}
	return s
}

func listFilesText(repo, dir string) string {
	files := listRepo(repo)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	var b strings.Builder
	n := 0
	for _, f := range files {
		if dir != "" && !strings.HasPrefix(f.Path, dir+"/") && f.Path != dir {
			continue
		}
		if n >= 400 {
			b.WriteString("…(more files; give a dir to narrow)\n")
			break
		}
		fmt.Fprintf(&b, "%s (%d bytes)\n", f.Path, f.Bytes)
		n++
	}
	if n == 0 {
		return "no files"
	}
	return b.String()
}

func searchText(repo, pattern, dir string) string {
	if pattern == "" {
		return "error: pattern is required"
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		re = regexp.MustCompile(regexp.QuoteMeta(pattern))
	}
	files := listRepo(repo)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	var b strings.Builder
	hits := 0
	for _, f := range files {
		if (dir != "" && !strings.HasPrefix(f.Path, dir+"/") && f.Path != dir) || f.Bytes > maxRepoFileSize {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(f.Path)))
		if err != nil || bytesLookBinary(data) {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				if hits++; hits > 60 {
					b.WriteString("…(more matches; narrow the pattern or dir)\n")
					return b.String()
				}
				if len(line) > 240 {
					line = line[:240] + "…"
				}
				fmt.Fprintf(&b, "%s:%d: %s\n", f.Path, i+1, strings.TrimSpace(line))
			}
		}
	}
	if hits == 0 {
		return "no matches"
	}
	return b.String()
}

// run executes a command on an exec node that has the tools it names.
func (a *agentRun) run(ctx context.Context, command string) string {
	if command == "" {
		return "error: command is required"
	}
	c, t := a.c, a.t
	execs := c.execNodes()
	if len(execs) == 0 {
		return "error: no node has exec enabled, so commands cannot run. Reason from the code instead."
	}
	if need := toolsInCommand(command); len(need) > 0 {
		capable := c.execNodesWith(execs, need)
		if len(capable) == 0 {
			return "error: no exec node has " + c.missingToolsNote(execs, need) + ". Reason from the code instead."
		}
		execs = capable
	}
	node := execs[a.Calls%len(execs)]
	inputs, srcs, _ := repoJobInputs(t.repoDir())
	limit := agentRunLimit
	if left := maxAgentTime - a.active; left < limit {
		limit = left
	}
	if limit < 10*time.Second {
		limit = 10 * time.Second
	}
	t.logf("info", "%s: run on %s: %s", a.label, c.nodeLabel(node), firstLine(command, 160))
	out, res, err := c.runExecJobFiles(ctx, node, c.execArgv(node, command), inputs, srcs, nil, t.Title+" · agent run", limit)
	status := ""
	if res != nil {
		status = res.Status
	}
	if ctx.Err() != nil {
		return "error: cancelled"
	}
	if err != nil {
		if status == "timed_out" {
			return fmt.Sprintf("error: the command timed out after %s", limit.Round(time.Second))
		}
		return "error: could not run: " + err.Error()
	}
	text := strings.TrimSpace(out.StdoutTail + "\n" + out.StderrTail)
	if len(text) > a.resCap {
		text = "…" + text[len(text)-a.resCap:]
	}
	text = withSandboxNote(text, out)
	return fmt.Sprintf("exit code %d\n%s", out.ExitCode, text)
}

// ---------- asking the user ----------

// ask posts a question in the thread and waits for the answer. The wait does
// not count against the agent's time limit.
func (a *agentRun) ask(ctx context.Context, question string, options []string) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", errors.New("question is required")
	}
	t := a.t
	ch := make(chan string, 1)
	t.mu.Lock()
	if t.asks == nil {
		t.asks = map[string]chan string{}
	}
	t.addMsgLocked(TaskMessage{Role: "assistant", Kind: "agent_question", Text: question, Pending: true, Subtask: a.subtask,
		Questions: []Question{{ID: "aq", Question: question, Options: options}}})
	id := t.Messages[len(t.Messages)-1].ID
	t.asks[id] = ch
	if st := t.subtaskIfAny(a.subtask); st != nil {
		st.Waiting = true
	}
	t.saveLocked()
	t.mu.Unlock()
	t.logf("info", "%s asks: %s", a.label, firstLine(question, 200))
	defer func() {
		t.mu.Lock()
		delete(t.asks, id)
		if st := t.subtaskIfAny(a.subtask); st != nil {
			st.Waiting = false
		}
		t.saveLocked()
		t.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return "", errCancelled
	case ans := <-ch:
		return ans, nil
	}
}

func (t *Task) subtaskIfAny(id string) *SubtaskState {
	if id == "" {
		return nil
	}
	for _, s := range t.Subtasks {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// deliverAnswer hands the user's answer to the waiting agent.
func (t *Task) deliverAnswer(msgID, answer string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	ch := t.asks[msgID]
	if ch == nil {
		return errors.New("nobody is waiting for that answer any more")
	}
	for i := range t.Messages {
		if t.Messages[i].ID == msgID {
			t.Messages[i].Pending = false
		}
	}
	delete(t.asks, msgID)
	t.addMsgLocked(TaskMessage{Role: "user", Kind: "agent_answer", Text: answer})
	ch <- answer
	return nil
}

func (t *Task) hasPendingAsk() bool {
	for _, m := range t.Messages {
		if m.Pending {
			return true
		}
	}
	return false
}

// ---------- the loop ----------

// compact drops the oldest tool output once the conversation outgrows the
// model's window, keeping the first prompt and the newest turns.
func compactMsgs(msgs []agentMsg, budget int) {
	total := func() int {
		n := 0
		for _, m := range msgs {
			n += len(m.Text)
			for _, c := range m.Calls {
				n += len(c.Args)
			}
		}
		return n
	}
	for i := 1; i < len(msgs)-4 && total() > budget; i++ {
		if msgs[i].Role == "tool" && len(msgs[i].Text) > 80 {
			msgs[i].Text = "(earlier output dropped to save room)"
		}
	}
}

// loop runs the conversation until the model answers without a tool call, or
// a limit is reached.
func (a *agentRun) loop(ctx context.Context) error {
	a.msgs = []agentMsg{{Role: "user", Text: a.user}}
	for {
		if a.Calls >= maxAgentCalls {
			a.Stopped = fmt.Sprintf("it used all %d tool calls", maxAgentCalls)
			break
		}
		if a.active >= maxAgentTime {
			a.Stopped = "it ran for 20 minutes"
			break
		}
		compactMsgs(a.msgs, a.budget)
		if a.subtask == "" {
			a.t.actBeginQ(fmt.Sprintf("%s: the planner is working out the next step (%d tool calls so far)", a.label, a.Calls), "", true)
		}
		start := time.Now()
		resp, err := a.step(ctx, llmRequest{System: a.system, Msgs: a.msgs, Tools: a.tools, MaxTokens: a.maxOut, Temperature: 0.2, Label: a.label})
		a.active += time.Since(start)
		if err != nil {
			if a.Calls == 0 && toolsUnsupported(err) {
				return errAgentUnsupported
			}
			return err
		}
		if len(resp.Calls) == 0 {
			a.Summary = strings.TrimSpace(resp.Text)
			return nil
		}
		a.msgs = append(a.msgs, agentMsg{Role: "assistant", Text: resp.Text, Calls: resp.Calls})
		for _, call := range resp.Calls {
			if a.Calls >= maxAgentCalls {
				a.msgs = append(a.msgs, agentMsg{Role: "tool", CallID: call.ID, Text: "error: out of tool calls"})
				continue
			}
			a.Calls++
			a.t.logf("info", "%s: %s %s", a.label, call.Name, firstLine(call.Args, 140))
			a.noteCall(call, "")
			t0 := time.Now()
			out := a.execute(ctx, call)
			a.noteCall(call, out)
			if call.Name != "ask_user" { // waiting for the user is free
				a.active += time.Since(t0)
			}
			if ctx.Err() != nil {
				return errCancelled
			}
			if strings.TrimSpace(out) == "" {
				out = "(empty)"
			}
			a.msgs = append(a.msgs, agentMsg{Role: "tool", CallID: call.ID, Text: clipText(out, a.resCap)})
			a.t.mu.Lock()
			if st := a.t.subtaskIfAny(a.subtask); st != nil {
				st.AgentCalls = a.Calls
			}
			a.t.mu.Unlock()
		}
	}
	// Out of budget: ask for a wrap-up without tools.
	a.msgs = append(a.msgs, agentMsg{Role: "user", Text: "You are out of budget (" + a.Stopped + "). Do not call tools. In a few sentences, say what you found or changed and what is still open."})
	resp, err := a.step(ctx, llmRequest{System: a.system, Msgs: a.msgs, MaxTokens: a.maxOut, Temperature: 0.2, Label: a.label + " (wrap-up)"})
	if err == nil {
		a.Summary = strings.TrimSpace(resp.Text)
	} else if errors.Is(err, errCancelled) || ctx.Err() != nil {
		return errCancelled
	}
	if a.Summary == "" {
		a.Summary = "Stopped: " + a.Stopped + "."
	}
	return nil
}

// newAgent sizes the conversation to the model that will run it. node is a
// worker node's id, or "" for the task's planner (cloud or local).
func (c *Controller) newAgent(t *Task, node, label, system, user, subtask string, tools []agentTool) *agentRun {
	a := &agentRun{c: c, t: t, label: label, system: system, user: user, tools: tools, subtask: subtask, resCap: agentResultCap, budget: 600000, maxOut: 8000}
	local := node
	if node == "" {
		t.mu.Lock()
		planner := t.Planner
		t.mu.Unlock()
		if !isCloudPlanner(planner) {
			local = strings.TrimPrefix(planner, "node:")
		}
		a.step = func(ctx context.Context, req llmRequest) (*llmResponse, error) { return c.plannerCall(ctx, t, req) }
	} else {
		a.step = func(ctx context.Context, req llmRequest) (*llmResponse, error) {
			return c.nodeCall(ctx, t, node, req, false)
		}
	}
	if local != "" { // a local model has a small window: shorter answers, results and history
		ctxTokens := c.nodeCtx(local)
		a.maxOut = 2048
		if a.maxOut > ctxTokens/3 {
			a.maxOut = ctxTokens / 3
		}
		a.budget = (ctxTokens-a.maxOut)*3 - len(system) - 3000
		a.resCap = a.budget / 4
		if a.resCap < 1500 {
			a.resCap = 1500
		}
		if a.resCap > agentResultCap {
			a.resCap = agentResultCap
		}
	}
	return a
}

func (c *Controller) agentEnabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.settings.AgentOff
}

// ---------- fixer ----------

const agentFixSystem = `You are the lead engineer integrating code written by several small models. The project's tests fail. Find the cause and fix it, working like a developer at a terminal.

You have tools: list_files, read_file and search to look around, write_file to change a file (always the complete new content), run to execute a command on a build node (each run starts from a fresh copy of the project, so chain setup and tests with &&), and ask_user only if a decision is truly the user's. You have at most 30 tool calls and 20 minutes.

Method: read the failing test and the file its error points at, make the smallest change that fixes the cause, then run the tests again to check. The tests describe the intended behaviour: fix the code to match them, and change a test only when the test itself is clearly wrong. If earlier fix rounds are listed and the tests still failed, their approach did not work; look for a different cause.

When you are done, answer in two or three sentences saying what was wrong and what you changed. Do not paste files into your answer.`

// agentFix runs one fix round as an agent. It returns the files changed.
func (c *Controller) agentFix(ctx context.Context, t *Task, plan *Plan, cmd, output string, history []string, round int) ([]string, error) {
	var b strings.Builder
	b.WriteString(t.instructionsBlock())
	fmt.Fprintf(&b, "Project: %s\n%s\n\nTest command: %s\n", plan.Title, plan.Summary, cmd)
	shown := output
	if len(shown) > 8000 {
		shown = "…" + shown[len(shown)-8000:]
	}
	fmt.Fprintf(&b, "\nTest output:\n%s\n", shown)
	if len(history) > 0 {
		b.WriteString("\nEarlier fix rounds (the tests still failed after each):\n")
		for _, h := range history {
			b.WriteString("- " + h + "\n")
		}
	}
	b.WriteString("\nProject files:\n" + clipText(listFilesText(t.repoDir(), ""), 6000))
	b.WriteString(t.steeringText())
	tools := []agentTool{toolListFiles, toolReadFile, toolSearch, toolWriteFile, toolRun, toolAskUser}
	a := c.newAgent(t, "", fmt.Sprintf("Fix round %d", round+1), agentFixSystem+c.toolsPrompt(), b.String(), "", tools)
	if err := a.loop(ctx); err != nil {
		return nil, err
	}
	changed := append([]string(nil), a.Written...)
	// A model that ignored the tools may still have answered with file blocks.
	for _, blk := range parseFileBlocks(a.Summary) {
		p := strings.TrimPrefix(blk.Path, "./")
		if err := t.writeFile(p, blk.Content); err != nil {
			t.logf("warn", "Ignored a fix for %s: %v", p, err)
			continue
		}
		t.touch(p)
		changed = append(changed, p)
	}
	note := firstLine(a.Summary, 300)
	if a.Stopped != "" {
		note = "stopped because " + a.Stopped + ". " + note
	}
	t.logf("info", "Fix round %d agent: %d tool call(s). %s", round+1, a.Calls, note)
	return changed, nil
}

// ---------- investigate subtasks ----------

const agentInvestigateSystem = `You are an investigator on a software team. Your job is to find things out, not to write the project's code: read the files, search, and run commands to answer the question you are given, so that the workers who write code afterwards start from facts.

You have tools: list_files, read_file and search to look through the project, run to execute a command on a build node (each run starts from a fresh copy of the project; chain steps with &&), and ask_user only if you need a decision from the person. You cannot change files. You have at most 30 tool calls and 20 minutes.

When you are done, answer with a short report: what you found, with file paths and line numbers, the cause or the facts that matter, and what you recommend changing. Under 400 words.`

// investigate runs an investigate subtask. node is the worker node, or "" to
// let the planner do it. The report is stored on the subtask and given to the
// subtasks that depend on it.
func (c *Controller) investigate(ctx context.Context, t *Task, spec PlanSubtask, node string) error {
	var b strings.Builder
	b.WriteString(t.instructionsBlock())
	t.mu.Lock()
	plan := t.Plan
	t.mu.Unlock()
	fmt.Fprintf(&b, "Project: %s\n%s\n", plan.Title, plan.Summary)
	if plan.TestCommand != "" {
		fmt.Fprintf(&b, "Tests run with: %s\n", plan.TestCommand)
	}
	fmt.Fprintf(&b, "\nYour investigation %s: %s\n%s\n", spec.ID, spec.Title, spec.Instructions)
	b.WriteString(c.dependencyReports(t, spec))
	b.WriteString("\nProject files:\n" + clipText(listFilesText(t.repoDir(), ""), 6000))
	b.WriteString(t.steeringText())
	tools := []agentTool{toolListFiles, toolReadFile, toolSearch, toolRun, toolAskUser}
	label := fmt.Sprintf("%s · %s", t.Title, spec.ID)
	who := "the planner"
	if node != "" {
		who = c.nodeLabel(node)
	}
	t.logf("info", "%s: investigating on %s", spec.ID, who)
	a := c.newAgent(t, node, label, agentInvestigateSystem+c.toolsPrompt(), b.String(), spec.ID, tools)
	if err := a.loop(ctx); err != nil {
		if errors.Is(err, errAgentUnsupported) {
			return fmt.Errorf("%s cannot use tools (is the model one that supports tool calling?)", who)
		}
		return err
	}
	report := a.Summary
	if a.Stopped != "" {
		report = "(Stopped because " + a.Stopped + ".)\n" + report
	}
	if strings.TrimSpace(report) == "" {
		return errors.New("the investigation produced no report")
	}
	t.mu.Lock()
	st := t.subtask(spec.ID)
	now := time.Now()
	st.Report, st.Attempts, st.AgentCalls = report, st.Attempts+1, a.Calls
	st.Status, st.Ended = "written", &now
	t.addMsgLocked(TaskMessage{Role: "assistant", Kind: "text", Text: "Findings from " + spec.Title + ":\n" + report, Subtask: spec.ID})
	t.mu.Unlock()
	t.logf("info", "%s: investigation finished after %d tool call(s).", spec.ID, a.Calls)
	return nil
}

// dependencyReports puts the findings of finished investigations a subtask
// depends on into its prompt.
func (c *Controller) dependencyReports(t *Task, spec PlanSubtask) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var b strings.Builder
	for _, d := range spec.DependsOn {
		if st := t.subtaskIfAny(d); st != nil && st.Report != "" {
			if b.Len() == 0 {
				b.WriteString("\nFindings from earlier investigations (treat as facts to build on):\n")
			}
			fmt.Fprintf(&b, "--- %s ---\n%s\n", d, clipText(st.Report, 4000))
		}
	}
	return b.String()
}

const plannerInvestigateHelp = `

Investigate subtasks: when the request needs digging before code can be written (find the cause of a bug, understand unfamiliar code, check what a tool or library does here), you may add up to two subtasks with "kind": "investigate" and "files": []. An investigator reads the project, searches and runs commands, and writes a short report; it changes no files. Make the subtasks that need its findings depend on it with depends_on, and they will receive the report. For ordinary subtasks leave "kind" out.`

// noteCall puts a fixer tool call on the activity card: before it runs (out is
// "") as the running step, after it as a finished one. Investigate subtasks
// have their own rows and are not shown here.
func (a *agentRun) noteCall(call toolCall, out string) {
	if a.subtask != "" || a.t == nil {
		return
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(call.Args), &args)
	s := func(k string) string { v, _ := args[k].(string); return strings.TrimSpace(v) }
	var doing, did, node string
	switch call.Name {
	case "read_file":
		doing, did = "reading "+s("path"), "read "+s("path")
	case "list_files":
		doing, did = "listing the project files", "listed the project files"
	case "search":
		doing, did = "searching for "+firstLine(s("pattern"), 60), "searched for "+firstLine(s("pattern"), 60)
	case "write_file":
		doing, did = "editing "+s("path"), "edited "+s("path")
	case "run":
		doing, did = "running "+firstLine(s("command"), 80), "ran "+firstLine(s("command"), 80)
	case "ask_user":
		doing, did = "waiting for your answer", "asked you a question"
	default:
		return
	}
	_ = node
	if out == "" {
		a.t.actBegin(fmt.Sprintf("%s: %s", a.label, doing), "")
		return
	}
	st := "ok"
	if strings.HasPrefix(out, "error") {
		st = "bad"
		did += " (" + firstLine(out, 80) + ")"
	} else if call.Name == "run" {
		var code int
		if n, _ := fmt.Sscanf(out, "exit code %d", &code); n == 1 {
			did += ": " + testSummary(strings.TrimPrefix(out, fmt.Sprintf("exit code %d\n", code)), code)
			if code != 0 {
				st = "bad"
			}
		}
	}
	a.t.actEnd(st, strings.ToUpper(did[:1])+did[1:])
}
