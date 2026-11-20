package main

// Run code in chat (gap list item 9). A chat with "Run code" on gives the
// answering model a run_code tool: Python, Go or a shell script, sent as an
// exec job to a node that allows commands, in the same sandbox as task tests
// (no network in strict mode), with a short time limit. The chat's documents
// are in files/ next to the script; whatever the script writes to out/ comes
// back and is kept with the chat (<chat>-runs/), shown under the answer.
// Nodes keep nothing: the job's work folder is deleted as for any exec job.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// CodeRun is one run_code call, kept on the answer that made it.
type CodeRun struct {
	ID     string    `json:"id"`
	Lang   string    `json:"lang"`
	Code   string    `json:"code"`
	Node   string    `json:"node,omitempty"`
	Exit   int       `json:"exit"`
	Stdout string    `json:"stdout,omitempty"`
	Stderr string    `json:"stderr,omitempty"`
	Error  string    `json:"error,omitempty"` // the run did not happen or did not finish
	Files  []RunFile `json:"files,omitempty"`
	Ms     int64     `json:"ms"`
}

// RunFile is a file the code wrote to out/.
type RunFile struct {
	Name  string `json:"name"`
	File  string `json:"file"` // stored name in <chat>-runs/
	Bytes int64  `json:"bytes"`
}

var chatRunTimeout = 60 * time.Second

const (
	maxRunCode     = 60000   // characters of code in one call
	maxRunsPerTurn = 6       // run_code calls in one answer
	runOutCap      = 6000    // stdout/stderr characters the model sees
	maxRunFile     = 8 << 20 // a returned file larger than this is left out
	maxRunFiles    = 12
)

// runLangs maps the tool's language to the toolchain name and the file it is written to.
var runLangs = map[string]struct{ tool, file string }{
	"python": {"python", "main.py"},
	"go":     {"go", "main.go"},
	"sh":     {"", "main.sh"},
}

func (ch *Chat) runsDir() string { return filepath.Join(ch.dir, ch.ID+"-runs") }

// codeRunner runs one answer's run_code calls.
type codeRunner struct {
	c      *Controller
	ch     *Chat
	inputs []FileEntry // the chat's documents, under files/
	names  []string    // their paths, for the tool description
	onRun  func(node string)
	mu     sync.Mutex
	runs   []CodeRun
	// stub (tests) runs the code instead of a node: Python only, on node "stub".
	stub func(lang, code string) (*ExecOutput, map[string][]byte, error)
}

// newCodeRunner returns nil when the chat does not run code.
func (c *Controller) newCodeRunner(ch *Chat, onRun func(string)) *codeRunner {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if !ch.RunCode {
		return nil
	}
	r := &codeRunner{c: c, ch: ch, onRun: onRun}
	used := map[string]bool{}
	for _, m := range ch.Messages {
		for _, d := range m.Docs {
			name := d.Name
			if d.Kind != "csv" {
				name += ".txt"
			}
			name = "files/" + strings.ReplaceAll(filepath.Base(name), " ", "_")
			if used[name] || d.File == "" {
				continue
			}
			text := ch.docText(d)
			if strings.HasPrefix(text, "(the text of this document") {
				continue
			}
			used[name] = true
			r.inputs = append(r.inputs, inlineEntry(name, []byte(text)))
			r.names = append(r.names, name)
		}
	}
	return r
}

func inlineEntry(name string, data []byte) FileEntry {
	return FileEntry{Path: name, SHA256: sha256Hex(data), Bytes: int64(len(data)), Mode: "0644", InlineB64: base64.StdEncoding.EncodeToString(data)}
}

// langs lists the languages some online exec node can run now, and where.
func (r *codeRunner) langs() map[string][]string {
	out := map[string][]string{}
	if r.stub != nil {
		return map[string][]string{"python": {"stub"}}
	}
	for _, id := range r.c.execNodes() {
		tools, known := r.c.nodeTools(id)
		for lang, l := range runLangs {
			switch {
			case l.tool == "" && r.c.nodeShell(id) == shellSh:
				out[lang] = append(out[lang], id)
			case l.tool != "" && known && hasTool(tools, l.tool):
				out[lang] = append(out[lang], id)
			}
		}
	}
	return out
}

func langList(m map[string][]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// tool is the run_code definition for the languages available now; ok is false when none is.
func (r *codeRunner) tool() (agentTool, bool) {
	langs := langList(r.langs())
	if len(langs) == 0 {
		return agentTool{}, false
	}
	desc := fmt.Sprintf("Run a short program on a cluster machine and get its output. Languages available now: %s. "+
		"Use it to compute, check or test something instead of guessing, or to make a chart or a file. "+
		"The program runs in an empty folder with a %d-second limit and no network; print results to stdout. "+
		"Write files the user should get (charts as PNG or SVG, CSV, text) into the out/ folder. "+
		"Go: a single main package file. Python: only the standard library and what the machine has installed.",
		strings.Join(langs, ", "), int(chatRunTimeout.Seconds()))
	if len(r.names) > 0 {
		desc += " The user's attached documents are readable as text at: " + strings.Join(r.names, ", ") + "."
	}
	return agentTool{Name: "run_code", Description: desc, Params: objSchema([]string{"language", "code"}, map[string]any{
		"language": map[string]any{"type": "string", "enum": langs, "description": "the language of the code"},
		"code":     str("the whole program"),
	})}, true
}

// run executes one call and returns the text the model sees.
func (r *codeRunner) run(ctx context.Context, argsJSON string) string {
	var args struct {
		Language string `json:"language"`
		Code     string `json:"code"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "Error: the arguments were not valid JSON."
	}
	r.mu.Lock()
	n := len(r.runs)
	r.mu.Unlock()
	if n >= maxRunsPerTurn {
		return fmt.Sprintf("Error: at most %d programs run per answer. Answer with what you have.", maxRunsPerTurn)
	}
	lang := strings.ToLower(strings.TrimSpace(args.Language))
	switch lang {
	case "python3", "py":
		lang = "python"
	case "bash", "shell":
		lang = "sh"
	case "golang":
		lang = "go"
	}
	l, ok := runLangs[lang]
	if !ok {
		return "Error: language must be one of python, go or sh."
	}
	if strings.TrimSpace(args.Code) == "" || len(args.Code) > maxRunCode {
		return fmt.Sprintf("Error: the code must be between 1 and %d characters.", maxRunCode)
	}
	run := CodeRun{ID: newID("run"), Lang: lang, Code: args.Code}
	start := time.Now()
	text := r.exec(ctx, &run, l.tool, l.file)
	run.Ms = time.Since(start).Milliseconds()
	r.mu.Lock()
	r.runs = append(r.runs, run)
	r.mu.Unlock()
	return text
}

func (r *codeRunner) exec(ctx context.Context, run *CodeRun, tool, file string) string {
	nodes := r.langs()[run.Lang]
	if len(nodes) == 0 {
		run.Error = "No node that allows commands can run " + run.Lang + " right now."
		return "Error: " + run.Error + " Answer without running it, and say so."
	}
	if r.stub != nil {
		run.Node = "stub"
		out, files, err := r.stub(run.Lang, run.Code)
		if err != nil {
			run.Error = err.Error()
			return "Error: the program did not finish on stub: " + run.Error
		}
		var res JobResult
		for name, data := range files {
			res.Files = append(res.Files, FileEntry{Path: "out/" + name, Bytes: int64(len(data)), InlineB64: base64.StdEncoding.EncodeToString(data)})
		}
		return r.report(run, out, &res, "")
	}
	node := r.c.leastBusy(nodes)
	run.Node = r.c.nodeLabel(node)
	var argv []string
	if tool == "" {
		argv = []string{"sh", file}
	} else {
		tools, _ := r.c.nodeTools(node)
		var bin string
		for _, t := range tools {
			if t.Name == tool {
				bin = firstNonEmpty(t.Path, t.Cmd)
			}
		}
		if tool == "go" {
			argv = []string{bin, "run", file}
		} else {
			argv = []string{bin, file}
		}
	}
	if r.onRun != nil {
		r.onRun(run.Node)
	}
	inputs := append([]FileEntry{inlineEntry(file, []byte(run.Code)), inlineEntry("out/.keep", nil)}, r.inputs...)
	out, res, err := r.c.runExecJobFiles(ctx, node, argv, inputs, nil, []string{"out/*"}, "Chat: run "+run.Lang, chatRunTimeout)
	if err != nil {
		r.c.releaseJobFiles(node, res)
		run.Error = err.Error()
		if (res != nil && res.Status == "timeout") || strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "timed out") {
			run.Error = fmt.Sprintf("Stopped after the %d-second limit.", int(chatRunTimeout.Seconds()))
		}
		return "Error: the program did not finish on " + run.Node + ": " + run.Error
	}
	return r.report(run, out, res, node)
}

// report keeps the run's output and files and writes what the model sees.
func (r *codeRunner) report(run *CodeRun, out *ExecOutput, res *JobResult, node string) string {
	run.Exit, run.Stdout, run.Stderr = out.ExitCode, clipTail(out.StdoutTail, 20000), clipTail(out.StderrTail, 20000)
	var skipped []string
	if res != nil {
		var blobs bool
		for _, f := range res.Files {
			name := path.Base(f.Path)
			if name == ".keep" {
				continue
			}
			if f.Blob || f.InlineB64 == "" {
				blobs = true
				skipped = append(skipped, name+" (too big to return)")
				continue
			}
			data, err := base64.StdEncoding.DecodeString(f.InlineB64)
			if err != nil || len(data) > maxRunFile || len(run.Files) >= maxRunFiles {
				skipped = append(skipped, name)
				continue
			}
			rf := RunFile{Name: name, File: run.ID + "-" + name, Bytes: int64(len(data))}
			if os.MkdirAll(r.ch.runsDir(), 0o700) == nil && writeFileAtomic(filepath.Join(r.ch.runsDir(), rf.File), data, 0o600) == nil {
				run.Files = append(run.Files, rf)
			}
		}
		if blobs && node != "" {
			r.c.releaseJobFiles(node, res)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Ran on %s. Exit code %d.\n", run.Node, run.Exit)
	if out.SandboxDenied {
		b.WriteString("The sandbox seems to have blocked something (no network, files only in the work folder). " + out.SandboxNote + "\n")
	}
	if s := strings.TrimSpace(run.Stdout); s != "" {
		b.WriteString("stdout:\n" + clipTail(s, runOutCap) + "\n")
	} else {
		b.WriteString("(nothing on stdout)\n")
	}
	if s := strings.TrimSpace(run.Stderr); s != "" {
		b.WriteString("stderr:\n" + clipTail(s, runOutCap/2) + "\n")
	}
	if len(run.Files) > 0 {
		var names []string
		for _, f := range run.Files {
			names = append(names, fmt.Sprintf("out/%s (%s)", f.Name, humanBytes(f.Bytes)))
		}
		b.WriteString("Files returned to the user (shown under your answer): " + strings.Join(names, ", ") + "\n")
	}
	if len(skipped) > 0 {
		b.WriteString("Not returned: " + strings.Join(skipped, ", ") + "\n")
	}
	return b.String()
}

// clipTail keeps the end of s, where errors are.
func clipTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + strings.ToValidUTF8(s[len(s)-n:], "")
}

// leastBusy picks the node with the fewest running jobs (sorted ids break ties).
func (c *Controller) leastBusy(ids []string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	best, bestN := ids[0], -1
	for _, id := range ids {
		if n := c.runningLocked(id); bestN < 0 || n < bestN {
			best, bestN = id, n
		}
	}
	return best
}

func (r *codeRunner) done() []CodeRun {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]CodeRun(nil), r.runs...)
}

// runsSummary is what later turns see of an answer's runs.
func runsSummary(runs []CodeRun) string {
	if len(runs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Code run for this answer:")
	for _, r := range runs {
		fmt.Fprintf(&b, "\n- %s on %s: ", r.Lang, firstNonEmpty(r.Node, "no node"))
		if r.Error != "" {
			b.WriteString(r.Error)
			continue
		}
		fmt.Fprintf(&b, "exit %d", r.Exit)
		if s := strings.TrimSpace(r.Stdout); s != "" {
			b.WriteString(", output: " + strings.ReplaceAll(clipTail(s, 300), "\n", " / "))
		}
		for _, f := range r.Files {
			b.WriteString(", wrote out/" + f.Name)
		}
	}
	b.WriteString("]")
	return b.String()
}

// handleChatRunCode turns running code on or off for a chat.
func (c *Controller) handleChatRunCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On bool `json:"on"`
	}
	ch := c.chatForChange(w, r, &req)
	if ch == nil {
		return
	}
	ch.mu.Lock()
	ch.RunCode = req.On
	ch.saveLocked()
	ch.mu.Unlock()
	taskLive.changed("chat_" + ch.ID)
	writeJSON(w, 200, map[string]any{"ok": true, "langs": c.runLangNames()})
}

// runLangNames lists what the cluster can run now, for the UI.
func (c *Controller) runLangNames() []string {
	return langList((&codeRunner{c: c}).langs())
}

// handleChatRunFile serves a file a run returned.
func (c *Controller) handleChatRunFile(w http.ResponseWriter, r *http.Request) {
	ch := c.getChat(r.PathValue("id"))
	if ch == nil {
		writeJSON(w, 404, map[string]string{"error": "no such chat"})
		return
	}
	name := filepath.Base(r.URL.Query().Get("file"))
	raw, err := os.ReadFile(filepath.Join(ch.runsDir(), name))
	if name == "." || name == "/" || err != nil {
		writeJSON(w, 404, map[string]string{"error": "no such file"})
		return
	}
	ext := strings.ToLower(path.Ext(name))
	ctype := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml"}[ext]
	if ctype == "" {
		ctype = "application/octet-stream"
		if t := mime.TypeByExtension(ext); strings.HasPrefix(t, "text/") || ext == ".csv" || ext == ".json" {
			ctype = "text/plain; charset=utf-8"
		}
	}
	if r.URL.Query().Get("download") != "" || ctype == "application/octet-stream" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", strings.SplitN(name, "-", 2)[len(strings.SplitN(name, "-", 2))-1]))
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:")
	w.Write(raw)
}

// answerWithRun answers with the run_code tool (and web search when that is on).
// ran is false when the tools could not be used; note then says why, and the
// caller answers the usual way.
func (c *Controller) answerWithRun(ctx context.Context, infer inferFn, model string, hist []ChatMessage, ctxTok, maxOut int, run *codeRunner) (*InferenceOutput, string, bool, error) {
	tool, ok := run.tool()
	if !ok {
		return nil, "Code was not run: no online node that allows commands has Python or Go (or a POSIX shell).", false, nil
	}
	if smallModel(model) {
		return nil, "Code was not run: this model is too small to call tools reliably.", false, nil
	}
	out, st, err := c.chatWithToolset(ctx, infer, hist, ctxTok, maxOut, chatToolset{web: c.chatToolsOn(), run: run, runTool: tool})
	if errors.Is(err, errAgentUnsupported) {
		return nil, "Code was not run: this model or its server cannot call tools.", false, nil
	}
	return out, st.note(), true, err
}

func joinNotes(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + " " + b
}
