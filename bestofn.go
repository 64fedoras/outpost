package main

// Best-of-N on hard subtasks (roadmap row 6).
//
// When a hard subtask starts and at least two other nodes are idle with nothing
// ready to run on them, up to two of them write the same subtask too. The
// copies are not written into the task: the candidates that pass a quick
// syntax check on an exec node go to a judge node, which picks one against the
// instructions. Extra copies hold a node only while nothing else wants it; the
// scheduler cancels them as soon as a ready subtask finds no free node. Only
// local nodes take part, so this adds no Claude cost.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	bonOff  = "off"
	bonHard = "hard"
	bonAll  = "all"

	bonExtras    = 2               // extra copies per subtask
	bonGrace     = 3 * time.Minute // how long the primary waits for extra copies
	bonJudgeWait = 2 * time.Minute // how long to wait for a free judge node
	bonCheckTime = 2 * time.Minute // syntax check time limit
)

func normBestOf(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case bonOff:
		return bonOff
	case bonAll:
		return bonAll
	}
	return bonHard
}

func (c *Controller) bestOfMode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return normBestOf(c.settings.BestOfN)
}

// bonEligible says whether a subtask gets extra copies under the setting.
func bonEligible(mode string, spec PlanSubtask) bool {
	if spec.Kind == "investigate" {
		return false
	}
	switch mode {
	case bonAll:
		return true
	case bonHard:
		return normDifficulty(spec.Difficulty) == "hard"
	}
	return false
}

// pickExtraNodes chooses the nodes for extra copies from the idle ones: none
// unless at least two are idle, then up to bonExtras, strongest first.
func pickExtraNodes(free []nodeScore) []string {
	if len(free) < 2 {
		return nil
	}
	sorted := append([]nodeScore(nil), free...)
	sort.Slice(sorted, func(i, j int) bool { return betterNode(sorted[i], sorted[j]) })
	if len(sorted) > bonExtras {
		sorted = sorted[:bonExtras]
	}
	ids := make([]string, len(sorted))
	for i, s := range sorted {
		ids[i] = s.ID
	}
	return ids
}

// bonCand is one node's version of a subtask.
type bonCand struct {
	node   string
	res    *subtaskResult
	err    error
	done   chan struct{} // closed when the copy has finished or been cancelled
	cancel context.CancelFunc
	label  string // A, B, C for the judge
}

// bonGroup holds the extra copies of one subtask.
type bonGroup struct {
	mu     sync.Mutex
	extras []*bonCand
	sealed bool // the primary has finished; no more copies may start
	tried  bool // an attempt to start extras was made
}

func (g *bonGroup) add(cd *bonCand) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sealed {
		return false
	}
	g.extras = append(g.extras, cd)
	return true
}

func (g *bonGroup) seal() []*bonCand {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sealed = true
	return append([]*bonCand(nil), g.extras...)
}

// running lists extra copies that are still generating.
func (g *bonGroup) running() []*bonCand {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []*bonCand
	for _, e := range g.extras {
		select {
		case <-e.done:
		default:
			out = append(out, e)
		}
	}
	return out
}

// launchExtras starts copies on nodes the caller has already reserved.
func (c *Controller) launchExtras(ctx context.Context, t *Task, g *bonGroup, spec PlanSubtask, nodes []string) {
	g.mu.Lock()
	g.tried = true
	g.mu.Unlock()
	for _, n := range nodes {
		cctx, cancel := context.WithCancel(ctx)
		cd := &bonCand{node: n, done: make(chan struct{}), cancel: cancel}
		if !g.add(cd) {
			cancel()
			c.releaseNode(n)
			continue
		}
		t.logf("info", "%s: spare node %s also writes it (best of %d).", spec.ID, c.nodeLabel(n), 1+len(nodes))
		go func(n string, cd *bonCand) {
			cd.res, cd.err = c.generateSubtask(cctx, t, spec, n, " (extra copy)", false)
			cd.cancel()
			c.releaseNode(n)
			close(cd.done)
		}(n, cd)
	}
}

// cancelExtras stops up to n running copies (all when n <= 0); the scheduler
// calls it when a ready subtask has no free node.
func (c *Controller) cancelExtras(t *Task, g *bonGroup, subID string, n int) int {
	stopped := 0
	for _, e := range g.running() {
		if n > 0 && stopped >= n {
			break
		}
		e.cancel()
		stopped++
	}
	if stopped > 0 {
		t.logf("info", "%s: gave up %d spare cop%s because other work needs the node.", subID, stopped, map[bool]string{true: "y", false: "ies"}[stopped == 1])
	}
	return stopped
}

// runBestOf finishes a subtask whose primary copy was dispatched with a group:
// it waits for the extra copies, drops candidates that fail the syntax check,
// has a judge pick one, and writes the winner. It returns the winning node.
// release gives back the primary's node; it is called as soon as the primary's
// own copy is done so the node is free for the judge.
func (c *Controller) runBestOf(ctx context.Context, t *Task, spec PlanSubtask, node string, g *bonGroup, release func()) (string, error) {
	res, perr := c.generateSubtask(ctx, t, spec, node, "", true)
	release()
	extras := g.seal()
	stopAll := func() {
		for _, e := range extras {
			e.cancel()
		}
	}
	if errors.Is(perr, errCancelled) || ctx.Err() != nil {
		stopAll()
		return node, errCancelled
	}
	if errors.Is(perr, errRequeue) {
		stopAll()
		return node, errRequeue
	}
	// Wait for the other copies, but not for ever.
	if len(extras) > 0 {
		grace := time.NewTimer(bonGrace)
		for _, e := range extras {
			select {
			case <-e.done:
			case <-grace.C:
				stopAll()
				<-e.done
			case <-ctx.Done():
				stopAll()
				return node, errCancelled
			}
		}
		grace.Stop()
	}
	var cands []*bonCand
	if perr == nil {
		cands = append(cands, &bonCand{node: node, res: res})
	}
	for _, e := range extras {
		if e.err == nil && e.res != nil {
			cands = append(cands, e)
		}
	}
	if len(cands) == 0 {
		return node, perr
	}
	if len(cands) == 1 {
		if err := c.commitSubtask(t, spec, cands[0].node, cands[0].res); err != nil {
			return node, err
		}
		if len(extras) > 0 {
			c.setBestOfNote(t, spec.ID, cands[0].node, "the only copy that finished")
		}
		return cands[0].node, nil
	}
	for i, cd := range cands {
		cd.label = string(rune('A' + i))
	}
	// Syntax check each candidate; if none passes keep them all and let the tests decide.
	var passing []*bonCand
	for _, cd := range cands {
		if ok, why := c.syntaxCheck(ctx, t, spec, cd); ok {
			passing = append(passing, cd)
		} else {
			t.logf("info", "%s: copy from %s dropped: %s", spec.ID, c.nodeLabel(cd.node), why)
		}
	}
	if ctx.Err() != nil {
		return node, errCancelled
	}
	reason := ""
	pool := passing
	switch {
	case len(passing) == 0:
		pool = cands
		t.logf("warn", "%s: no copy passed the syntax check; picking among all of them.", spec.ID)
	case len(passing) == 1:
		reason = "the only copy that passed the syntax check"
	}
	var win *bonCand
	if len(pool) == 1 {
		win = pool[0]
	} else {
		var judge string
		win, reason, judge = c.judgeCandidates(ctx, t, spec, pool)
		if ctx.Err() != nil {
			return node, errCancelled
		}
		if judge != "" {
			reason = c.nodeLabel(judge) + " judged: " + reason
		}
	}
	for _, cd := range cands {
		if cd != win {
			c.recordSpeed(cd.node, cd.res.resp.TokensPerSec)
		}
	}
	if err := c.commitSubtask(t, spec, win.node, win.res); err != nil {
		return node, err
	}
	c.setBestOfNote(t, spec.ID, win.node, reason)
	t.logf("info", "%s: best of %d: %s won — %s", spec.ID, len(cands), c.nodeLabel(win.node), reason)
	return win.node, nil
}

func (c *Controller) setBestOfNote(t *Task, id, winner, reason string) {
	t.mu.Lock()
	st := t.subtask(id)
	st.Node, st.NodeName = winner, c.nodeLabel(winner)
	st.BestOf = fmt.Sprintf("%s won: %s", c.nodeLabel(winner), reason)
	t.saveLocked()
	t.mu.Unlock()
}

// ---------- syntax check ----------

// syntaxTool is the tool a file's syntax check needs, "" if the file has no check.
func syntaxTool(file string) string {
	switch strings.ToLower(path.Ext(file)) {
	case ".py":
		return "python"
	case ".js", ".mjs", ".cjs":
		return "node"
	case ".go":
		return "go"
	}
	return ""
}

// syntaxCommands builds one check command per file that has one, for a node
// running the given OS, and reports a file that is invalid without needing a node.
func syntaxCommands(files map[string]string, osName string) (cmds []string, bad string) {
	names := make([]string, 0, len(files))
	for f := range files {
		names = append(names, f)
	}
	sort.Strings(names)
	py := "python3"
	if osName == "windows" {
		py = "python"
	}
	for _, f := range names {
		q := `"` + f + `"`
		switch strings.ToLower(path.Ext(f)) {
		case ".py":
			cmds = append(cmds, py+" -m py_compile "+q)
		case ".js", ".mjs", ".cjs":
			cmds = append(cmds, "node --check "+q)
		case ".go":
			cmds = append(cmds, "gofmt -e -l "+q)
		case ".json":
			if !json.Valid([]byte(files[f])) {
				return nil, f + " is not valid JSON"
			}
		}
	}
	return cmds, ""
}

// syntaxCheck runs the quick check on an exec node. A candidate is dropped only
// when the check ran and failed; if no node can run it, the candidate stays.
func (c *Controller) syntaxCheck(ctx context.Context, t *Task, spec PlanSubtask, cd *bonCand) (bool, string) {
	files := cd.res.files
	if _, bad := syntaxCommands(files, ""); bad != "" {
		return false, bad
	}
	var need []string
	seen := map[string]bool{}
	for f := range files {
		if tool := syntaxTool(f); tool != "" && !seen[tool] {
			seen[tool] = true
			need = append(need, tool)
		}
	}
	if len(need) == 0 {
		return true, ""
	}
	sort.Strings(need)
	execs := c.execNodesWith(c.execNodes(), need)
	if len(execs) == 0 {
		return true, "" // nobody can run the check
	}
	node := execs[0]
	osName := ""
	c.mu.Lock()
	if n := c.nodes[node]; n != nil && n.Hello != nil {
		osName = n.Hello.OS
	}
	c.mu.Unlock()
	cmds, _ := syntaxCommands(files, osName)
	if len(cmds) == 0 {
		return true, ""
	}
	var inputs []FileEntry
	for f, content := range files {
		inputs = append(inputs, inlineFile(f, []byte(content)))
	}
	var out *ExecOutput
	var err error
	if c.execStub != nil {
		out, err = c.execStub(ctx, node, c.execArgv(node, cmds...), inputs)
	} else {
		out, _, err = c.runExecJob(ctx, node, c.execArgv(node, cmds...), inputs, t.Title+" · syntax check "+spec.ID, bonCheckTime)
	}
	if err != nil {
		return true, "" // the check itself failed to run
	}
	if out.ExitCode == 0 {
		return true, ""
	}
	msg := strings.TrimSpace(out.StderrTail + "\n" + out.StdoutTail)
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return false, "failed the syntax check: " + firstLine(msg, 300)
}

// ---------- judge ----------

const judgeSystem = `You judge several versions of the same code written by different team members. Pick the version that best follows the instructions and is most likely to work as written: complete, correct, matching the interfaces the instructions name.

Answer with exactly two lines:
<<<PICK X>>>   (X is the letter of the best version)
REASON: one short sentence.`

var judgePickRe = regexp.MustCompile(`(?i)<<<\s*PICK\s+([A-Z])\s*>>>`)
var judgeReasonRe = regexp.MustCompile(`(?im)^\s*REASON:\s*(.+)$`)

// parseJudge reads the judge's answer; ok is false when it picked no valid candidate.
func parseJudge(text string, n int) (idx int, reason string, ok bool) {
	m := judgePickRe.FindStringSubmatch(text)
	if m == nil {
		return 0, "", false
	}
	idx = int(strings.ToUpper(m[1])[0] - 'A')
	if idx < 0 || idx >= n {
		return 0, "", false
	}
	reason = "it fit the instructions best"
	if r := judgeReasonRe.FindStringSubmatch(text); r != nil {
		reason = firstLine(strings.TrimSpace(r[1]), 240)
	}
	return idx, reason, true
}

// judgeCandidates asks a free node to pick one candidate. When no judge is
// available or it gives no usable answer, the candidate from the strongest
// node wins.
func (c *Controller) judgeCandidates(ctx context.Context, t *Task, spec PlanSubtask, cands []*bonCand) (*bonCand, string, string) {
	ids := make([]string, len(cands))
	for i, cd := range cands {
		ids[i] = cd.node
	}
	scores := c.scoreNodes(ids)
	fallback := cands[0]
	best := scores[0]
	for i, s := range scores {
		if betterNode(s, best) {
			best, fallback = s, cands[i]
		}
	}
	fb := func(why string) (*bonCand, string, string) {
		t.logf("warn", "%s: %s; keeping the copy from %s.", spec.ID, why, c.nodeLabel(fallback.node))
		return fallback, "kept the copy from the strongest node (" + why + ")", ""
	}
	judge := ""
	deadline := time.Now().Add(bonJudgeWait)
	for judge == "" {
		all := c.scoreNodes(c.workerNodes())
		if w := pickReviewer(best, c.freeScores(all, "")); w != "" && c.tryReserve(w) {
			judge = w
			break
		}
		if len(all) == 0 || time.Now().After(deadline) {
			return fb("no node was free to judge")
		}
		select {
		case <-ctx.Done():
			return fallback, "", ""
		case <-time.After(time.Second):
		}
	}
	defer c.releaseNode(judge)

	ctxTokens := c.nodeCtx(judge)
	maxOut := 300
	var body strings.Builder
	for _, cd := range cands {
		fmt.Fprintf(&body, "=== Version %s ===\n", cd.label)
		for _, f := range spec.Files {
			fmt.Fprintf(&body, "<<<FILE %s>>>\n%s<<<END>>>\n", f, cd.res.files[f])
		}
	}
	budget := (ctxTokens-maxOut)*3 - len(judgeSystem) - 1500
	if budget < len(body.String())+1500 {
		return fb("the versions were too long for the judge")
	}
	prompt := c.subtaskPromptWith(t, spec, budget-body.Len(), false)
	prompt = strings.Replace(prompt, "Write these files:", "Files in this subtask:", 1)
	resp, err := c.nodeCall(ctx, t, judge, llmRequest{System: judgeSystem, User: prompt + "\nThe versions:\n" + body.String(), MaxTokens: maxOut, Temperature: 0.1,
		Label: fmt.Sprintf("%s · judge %s", t.Title, spec.ID), Sub: spec.ID}, false)
	if err != nil {
		if ctx.Err() != nil {
			return fallback, "", ""
		}
		return fb("the judge failed: " + firstLine(err.Error(), 80))
	}
	idx, reason, ok := parseJudge(resp.Text, len(cands))
	if !ok {
		return fb("the judge gave no usable answer")
	}
	return cands[idx], reason, judge
}
