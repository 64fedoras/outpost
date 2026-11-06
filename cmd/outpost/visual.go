package main

// UI mockups and visual checks (roadmap row 20).
//
// An exec node with Chromium renders the pages a task wrote, at desktop
// (1280 px) and phone (390 px) widths, and the screenshots appear in the
// thread. When the planner can read images it then looks at them against the
// request and may fix what it sees. A mockup task writes two or three
// variants (mockups/*.html) and stops at a pick card; picking one starts a
// follow-up round that builds the real code from it.
//
// Nodes get the repo files inside the job and keep nothing: the private
// workspace is removed when the job ends.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	shotDesktopW    = 1280
	shotDesktopH    = 900
	shotPhoneW      = 390
	shotPhoneH      = 844
	maxVisualRounds = 2
	maxShotPages    = 3
	shotTimeout     = 2 * time.Minute
)

// Shot is one rendered page: file names in the task's shots folder.
type Shot struct {
	Label   string `json:"label"` // "a" for mockups/a.html, the page name otherwise
	Page    string `json:"page"`  // path in the project, or the preview URL
	Desktop string `json:"desktop"`
	Phone   string `json:"phone"`
}

// shotTarget is one thing to render.
type shotTarget struct {
	Label string
	Page  string // repo-relative HTML file; empty for an app
	Cmd   string // app: command that starts it
	URL   string // app: where it listens
}

// localURL accepts only addresses on the node itself, which is where the
// previewed app runs.
func localURL(u string) bool {
	return strings.HasPrefix(u, "http://127.0.0.1") || strings.HasPrefix(u, "http://localhost")
}

func (t *Task) shotsDir() string { return filepath.Join(t.dir, "shots") }

const noChromiumNote = "No exec node has Chromium or Chrome, so no screenshots were taken. Install Chromium (for example `sudo apt install chromium`) or Chrome on a node with exec enabled; the Nodes page lists the tools each node has."

// chromiumNode picks an exec node that reports Chromium or Chrome.
func (c *Controller) chromiumNode() (string, ToolInfo, bool) {
	for _, id := range c.execNodes() {
		tools, known := c.nodeTools(id)
		if !known {
			continue
		}
		for _, tl := range tools {
			if tl.Name == "chromium" {
				return id, tl, true
			}
		}
	}
	return "", ToolInfo{}, false
}

// chromeArgs is the browser command line for one screenshot. Relative paths
// resolve in the job's workspace, which is the working directory.
func chromeArgs(bin, out string, w, h int, target string) []string {
	return []string{bin, "--headless", "--no-sandbox", "--disable-gpu", "--hide-scrollbars", "--no-first-run",
		"--disable-background-networking", "--force-device-scale-factor=1", "--user-data-dir=.chrome-profile",
		"--virtual-time-budget=4000", "--window-size=" + fmt.Sprintf("%d,%d", w, h), "--screenshot=" + out, target}
}

// appShotScript starts the app, waits for its URL, takes both screenshots and
// stops the app. Arguments: $1 browser, $2 start command, $3 URL.
const appShotScript = `chrome="$1"; url="$3"
( exec sh -c "$2" ) >preview.log 2>&1 &
pid=$!
i=0
if command -v curl >/dev/null 2>&1; then
  while [ $i -lt 40 ] && ! curl -sf -o /dev/null "$url"; do i=$((i+1)); sleep 1; done
else
  sleep 8
fi
rc=0
common="--headless --no-sandbox --disable-gpu --hide-scrollbars --no-first-run --disable-background-networking --force-device-scale-factor=1 --user-data-dir=.chrome-profile --virtual-time-budget=4000"
"$chrome" $common --window-size=1280,900 --screenshot=shot-desktop.png "$url" || rc=$?
"$chrome" $common --window-size=390,844 --screenshot=shot-phone.png "$url" || rc=$?
kill $pid 2>/dev/null; pkill -P $pid 2>/dev/null
tail -n 20 preview.log >&2
exit $rc`

// runCollectJob runs an exec job and returns the files it collected (small
// enough to come back inline, which a screenshot is).
func (c *Controller) runCollectJob(ctx context.Context, node string, argv []string, inputs []FileEntry, srcs map[string]string, collect []string, label string) (*ExecOutput, map[string][]byte, error) {
	out, res, err := c.runExecJobFiles(ctx, node, argv, inputs, srcs, collect, label, shotTimeout)
	if err != nil {
		return nil, nil, err
	}
	files := map[string][]byte{}
	for _, f := range res.Files {
		if data, err := base64.StdEncoding.DecodeString(f.InlineB64); err == nil && len(data) > 0 {
			files[f.Path] = data
		}
	}
	return out, files, nil
}

// shotName makes the stored file name for one screenshot.
func (t *Task) shotName(idx int, label, kind string) string {
	t.mu.Lock()
	round := t.Round
	t.mu.Unlock()
	return fmt.Sprintf("r%d-%d-%d-%s-%s.png", round, time.Now().UnixMilli(), idx, slug(label), kind)
}

func isPNG(b []byte) bool { return len(b) > 8 && string(b[1:4]) == "PNG" }

// renderShots renders every target at both widths on a Chromium node.
func (c *Controller) renderShots(ctx context.Context, t *Task, targets []shotTarget) ([]Shot, error) {
	node, tool, ok := c.chromiumNode()
	if !ok {
		return nil, errors.New(noChromiumNote)
	}
	bin := tool.Path
	if bin == "" {
		bin = tool.Cmd
	}
	inputs, srcs, _ := repoJobInputs(t.repoDir())
	if err := os.MkdirAll(t.shotsDir(), 0o755); err != nil {
		return nil, err
	}
	var shots []Shot
	for i, tg := range targets {
		sh := Shot{Label: tg.Label, Page: tg.Page}
		if tg.URL != "" {
			sh.Page = tg.URL
		}
		var files map[string][]byte
		var out *ExecOutput
		var err error
		if tg.URL != "" {
			if n := c.nodeShell(node); n != shellSh {
				return nil, errors.New("previewing a running app needs a Linux or macOS exec node with Chromium (this one uses " + n + ")")
			}
			out, files, err = c.runCollectJob(ctx, node, []string{"sh", "-c", appShotScript, "sh", bin, tg.Cmd, tg.URL}, inputs, srcs, []string{"shot-desktop.png", "shot-phone.png"}, t.Title+" · screenshots")
			if err == nil && out.ExitCode != 0 {
				err = fmt.Errorf("the browser failed (exit %d): %s", out.ExitCode, firstLine(strings.TrimSpace(out.StderrTail), 400))
			}
			if err != nil {
				return nil, err
			}
			sh.Desktop, sh.Phone = t.shotName(i, tg.Label, "desktop"), t.shotName(i, tg.Label, "phone")
			if err := t.saveShot(sh.Desktop, files["shot-desktop.png"]); err != nil {
				return nil, err
			}
			if err := t.saveShot(sh.Phone, files["shot-phone.png"]); err != nil {
				return nil, err
			}
			shots = append(shots, sh)
			continue
		}
		for _, v := range []struct {
			kind string
			w, h int
		}{{"desktop", shotDesktopW, shotDesktopH}, {"phone", shotPhoneW, shotPhoneH}} {
			outName := "shot-" + v.kind + ".png"
			out, files, err = c.runCollectJob(ctx, node, chromeArgs(bin, outName, v.w, v.h, tg.Page), inputs, srcs, []string{outName}, t.Title+" · screenshots")
			if err != nil {
				return nil, err
			}
			if out.ExitCode != 0 && len(files[outName]) == 0 {
				return nil, fmt.Errorf("the browser failed (exit %d): %s", out.ExitCode, firstLine(strings.TrimSpace(out.StderrTail), 400))
			}
			name := t.shotName(i, tg.Label, v.kind)
			if err := t.saveShot(name, files[outName]); err != nil {
				return nil, err
			}
			if v.kind == "desktop" {
				sh.Desktop = name
			} else {
				sh.Phone = name
			}
		}
		shots = append(shots, sh)
	}
	return shots, nil
}

func (c *Controller) nodeShell(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := c.nodes[id]; n != nil && n.Hello != nil {
		return n.Hello.Shell
	}
	return ""
}

func (t *Task) saveShot(name string, data []byte) error {
	if !isPNG(data) {
		return errors.New("the browser produced no screenshot (files over 1 MiB are not collected, so a very large page can fail)")
	}
	return os.WriteFile(filepath.Join(t.shotsDir(), name), data, 0o644)
}

// previewTargets decides what to render for the round. Mockup tasks render
// their variants; otherwise the plan's preview app or page, or the HTML pages
// this round wrote (index.html first).
func previewTargets(t *Task, plan *Plan) []shotTarget {
	repo := t.repoDir()
	exists := func(p string) bool {
		st, err := os.Stat(filepath.Join(repo, filepath.FromSlash(p)))
		return err == nil && st.Mode().IsRegular()
	}
	if plan.Mockup {
		var out []shotTarget
		for _, f := range listRepo(repo) {
			if strings.HasPrefix(f.Path, "mockups/") && strings.HasSuffix(strings.ToLower(f.Path), ".html") && strings.Count(f.Path, "/") == 1 {
				out = append(out, shotTarget{Label: strings.TrimSuffix(path.Base(f.Path), path.Ext(f.Path)), Page: f.Path})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Page < out[j].Page })
		if len(out) > 3 {
			out = out[:3]
		}
		return out
	}
	if plan.PreviewCommand != "" && localURL(plan.PreviewURL) {
		return []shotTarget{{Label: "app", Cmd: plan.PreviewCommand, URL: plan.PreviewURL}}
	}
	if p := strings.TrimPrefix(strings.TrimSpace(plan.PreviewPage), "./"); p != "" && exists(p) {
		return []shotTarget{{Label: path.Base(p), Page: p}}
	}
	t.mu.Lock()
	touched := append([]string(nil), t.Touched...)
	t.mu.Unlock()
	var pages []string
	for _, f := range touched {
		if strings.HasSuffix(strings.ToLower(f), ".html") && !strings.HasPrefix(f, "mockups/") && exists(f) {
			pages = append(pages, f)
		}
	}
	sort.SliceStable(pages, func(i, j int) bool {
		bi, bj := path.Base(pages[i]) == "index.html", path.Base(pages[j]) == "index.html"
		if bi != bj {
			return bi
		}
		return pages[i] < pages[j]
	})
	if len(pages) == 0 && exists("index.html") {
		for _, f := range touched { // a stylesheet or script changed: show the main page
			if ext := strings.ToLower(path.Ext(f)); ext == ".css" || ext == ".js" {
				pages = append(pages, "index.html")
				break
			}
		}
	}
	if len(pages) > maxShotPages {
		pages = pages[:maxShotPages]
	}
	out := make([]shotTarget, len(pages))
	for i, p := range pages {
		out[i] = shotTarget{Label: strings.TrimSuffix(p, path.Ext(p)), Page: p}
	}
	return out
}

func (c *Controller) shotMessage(t *Task, kind, text string, shots []Shot) {
	t.addMsg(TaskMessage{Role: "assistant", Kind: kind, Text: text, Shots: shots})
}

// visualCheck renders what the round built, shows it in the thread and, when
// the planner can read images, reviews it. It never fails the task.
func (c *Controller) visualCheck(ctx context.Context, t *Task) {
	t.mu.Lock()
	plan := t.Plan
	t.mu.Unlock()
	if plan == nil {
		return
	}
	targets := previewTargets(t, plan)
	if len(targets) == 0 {
		return
	}
	t.setStatus("testing")
	t.logf("info", "Taking screenshots of %d page(s) at %d px and %d px wide.", len(targets), shotDesktopW, shotPhoneW)
	shots, err := c.renderShots(ctx, t, targets)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		t.logf("warn", "Screenshots skipped: %v", err)
		t.addMsg(TaskMessage{Role: "event", Kind: "event", Text: "Screenshots skipped: " + err.Error()})
		return
	}
	if plan.Mockup {
		c.shotMessage(t, "mockup", "Here are the variants. Pick one and I will build the real code from it, or reply below to ask for changes.", shots)
		return
	}
	c.shotMessage(t, "shots", "What it looks like at desktop (1280 px) and phone (390 px) width.", shots)
	if err := c.checkPlannerVision(t.Planner, t.ORModel); err != nil {
		t.logf("info", "Visual review skipped: the planner cannot read images (%v)", err)
		return
	}
	for round := 1; round <= maxVisualRounds; round++ {
		changed, note, err := c.reviewShots(ctx, t, plan, shots, round)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			t.logf("warn", "Visual review stopped: %v", err)
			return
		}
		if len(changed) == 0 {
			t.logf("info", "Visual review: %s", note)
			t.addMsg(TaskMessage{Role: "event", Kind: "event", Text: "Visual review: " + note})
			return
		}
		t.touch(changed...)
		t.logf("info", "Visual review round %d changed: %s", round, strings.Join(changed, ", "))
		t.addMsg(TaskMessage{Role: "event", Kind: "event", Text: fmt.Sprintf("Visual review found problems (%s) and changed %s. Screenshots again:", note, strings.Join(changed, ", "))})
		shots, err = c.renderShots(ctx, t, targets)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			t.logf("warn", "Screenshots after the fix failed: %v", err)
			return
		}
		c.shotMessage(t, "shots", fmt.Sprintf("After visual fix %d.", round), shots)
	}
}

const visualSystem = `You are the lead engineer checking how a web page that your team just built actually looks. You are given screenshots of each page, a desktop one (1280 px wide) then a phone one (390 px wide), in that order, plus the request and the page source.

Check: does it match the request; is anything cut off, overlapping, unreadable (low contrast) or empty; does the phone layout scroll sideways or squeeze its content; are buttons and text a usable size on the phone. Ignore small taste differences.

If it looks right, answer with exactly:
<<<OK>>>

Otherwise start with one line "ISSUES: " followed by a short list of what is wrong, then output the corrected files, each one complete, and nothing else.
` + fileFormatHelp

// reviewShots asks the planner to judge the screenshots. It returns the files
// it changed (none when the page looks right) and a one-line note.
func (c *Controller) reviewShots(ctx context.Context, t *Task, plan *Plan, shots []Shot, round int) ([]string, string, error) {
	var imgs []llmImage
	var named strings.Builder
	for _, s := range shots {
		for _, n := range []struct{ kind, file string }{{"desktop", s.Desktop}, {"phone", s.Phone}} {
			raw, err := os.ReadFile(filepath.Join(t.shotsDir(), n.file))
			if err != nil {
				continue
			}
			imgs = append(imgs, llmImage{Media: "image/png", B64: base64.StdEncoding.EncodeToString(raw)})
			fmt.Fprintf(&named, "Image %d: %s, %s\n", len(imgs), s.Page, n.kind)
		}
	}
	if len(imgs) == 0 {
		return nil, "", errors.New("no screenshots to review")
	}
	budget := c.plannerBudget(t)
	var hint strings.Builder
	for _, s := range shots {
		hint.WriteString(s.Page + " ")
	}
	user := t.instructionsBlock() + fmt.Sprintf("Request:\n%s\n\nProject: %s\n%s\n\nScreenshots, in order:\n%s\nProject files:\n%s",
		t.Prompt, plan.Title, plan.Summary, named.String(), repoSnapshotFirst(t.repoDir(), budget/2, hint.String())) + t.steeringText()
	resp, err := c.plannerCall(ctx, t, llmRequest{System: visualSystem, User: user, MaxTokens: 16000, Temperature: 0.2, Label: fmt.Sprintf("Visual review %d", round), Images: imgs})
	if err != nil {
		return nil, "", err
	}
	blocks := parseFileBlocks(resp.Text)
	if len(blocks) == 0 {
		return nil, "the screenshots look right.", nil
	}
	note := "layout problems"
	if i := strings.Index(resp.Text, "ISSUES:"); i >= 0 {
		note = firstLine(strings.TrimSpace(resp.Text[i+len("ISSUES:"):]), 200)
	}
	var changed []string
	for _, b := range blocks {
		p := strings.TrimPrefix(b.Path, "./")
		if err := t.writeFile(p, b.Content); err != nil {
			t.logf("warn", "Ignored a visual fix for %s: %v", p, err)
			continue
		}
		changed = append(changed, p)
	}
	if len(changed) == 0 {
		return nil, "the screenshots look right.", nil
	}
	return changed, note, nil
}

// ---------- mockup tasks ----------

const mockupHelp = `

MOCKUP MODE. The user wants to choose a design before any real code is written. Do not plan the real application. Plan exactly 2 or 3 design variants of the page or screen they described, and set "mockup": true in the JSON.
- Each variant is one subtask with one file: mockups/a.html, mockups/b.html (and mockups/c.html). No other files, no test_command, no setup_command.
- Each file is a complete, self-contained HTML page: inline CSS, no external fonts, scripts, images or network requests; use realistic sample text. It must look good at 1280 px and at 390 px wide.
- Make the variants clearly different (layout, density, colour or style), and say in each subtask's instructions what makes it different. Subtasks are independent: no depends_on.`

// validateMockupPlan checks a mockup plan has two or three variant pages.
func validateMockupPlan(p *Plan) error {
	n := 0
	for _, s := range p.Subtasks {
		for _, f := range s.Files {
			if strings.HasPrefix(f, "mockups/") && strings.HasSuffix(strings.ToLower(f), ".html") && strings.Count(f, "/") == 1 {
				n++
			} else {
				return fmt.Errorf("a mockup plan may only contain files named mockups/<name>.html, not %s", f)
			}
		}
	}
	if n < 2 || n > 3 {
		return fmt.Errorf("a mockup plan needs 2 or 3 variants, not %d", n)
	}
	return nil
}

// handlePickVariant starts the real build from the chosen mockup variant.
func (c *Controller) handlePickVariant(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	var req struct {
		Page string `json:"page"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "pick a variant"})
		return
	}
	t.mu.Lock()
	ok := false
	if t.Status == "done" && t.Mockup {
		for i := len(t.Messages) - 1; i >= 0 && !ok; i-- {
			if t.Messages[i].Kind == "mockup" {
				for _, s := range t.Messages[i].Shots {
					ok = ok || s.Page == req.Page
				}
				break
			}
		}
	}
	t.mu.Unlock()
	if !ok {
		writeJSON(w, 409, map[string]string{"error": "there is no mockup waiting for that pick"})
		return
	}
	t.mu.Lock()
	t.Mockup = false
	t.mu.Unlock()
	c.startFollowUp(t, fmt.Sprintf("Build the real implementation from the mockup %s. Keep its layout, colours, typography and sample copy, and make it work as described in the original request: %s\nThe other variants in mockups/ were not chosen; leave them as they are.", req.Page, firstLine(t.Prompt, 1500)), nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleTaskShot serves a screenshot to the web UI.
func (c *Controller) handleTaskShot(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	raw, err := os.ReadFile(filepath.Join(t.shotsDir(), filepath.Base(r.URL.Query().Get("name"))))
	if err != nil || !isPNG(raw) {
		writeJSON(w, 404, map[string]string{"error": "no such screenshot"})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(raw)
}
