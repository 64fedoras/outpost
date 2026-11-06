package main

// Patch download and hand-off to a cloud coding assistant (idea 20).
//
// The patch is built from the per-round copies (tasks_rounds.go): the state
// of a file before round k is that round's kept copy; its state after round k
// is the kept copy of the next round that touched it, or the file as it is now.
//
// The hand-off zip holds the task's files plus HANDOFF.md (the request, the
// plan, what each round changed, failing tests) and a short pointer to it in
// each assistant's own instructions file, so any of them picks it up:
// AGENTS.md (OpenAI Codex and others), CLAUDE.md (Claude Code), GEMINI.md
// (Gemini CLI), .github/copilot-instructions.md (GitHub Copilot) and
// .cursor/rules/outpost-handoff.mdc (Cursor). A file the project already has
// keeps its content, with the pointer added at the end.

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type fileVersion struct {
	content string
	exists  bool
}

// fileBefore is path as it was before round n changed it (kept copy), or
// absent if that round added it.
func (t *Task) fileBefore(path string, round int) fileVersion {
	dir := t.roundDir(round)
	m := readManifest(dir)
	switch m.Files[path] {
	case "added":
		return fileVersion{}
	case "modified", "deleted":
		b, err := os.ReadFile(filepath.Join(dir, "orig", filepath.FromSlash(path)))
		if err != nil {
			return fileVersion{}
		}
		return fileVersion{string(b), true}
	}
	return fileVersion{}
}

// fileAfter is the content of path after round k: the kept copy of the next
// round that touched it, or the file now.
func (t *Task) fileAfter(path string, k int, nums []int) fileVersion {
	for _, n := range nums {
		if n <= k {
			continue
		}
		if _, ok := readManifest(t.roundDir(n)).Files[path]; ok {
			return t.fileBefore(path, n)
		}
	}
	p, err := safeJoin(t.repoDir(), path)
	if err != nil {
		return fileVersion{}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return fileVersion{}
	}
	return fileVersion{string(b), true}
}

// taskPatch is the diff of one round (round > 0) or of all rounds together.
func (t *Task) taskPatch(round int) (string, int) {
	nums := t.roundNumbers()
	if len(nums) == 0 {
		return "", 0
	}
	first, last := nums[0], nums[len(nums)-1]
	if round > 0 {
		first, last = round, round
	}
	// For each path: its state before the first round in range that touched it.
	base := map[string]fileVersion{}
	for _, n := range nums {
		if n < first || n > last {
			continue
		}
		for p := range readManifest(t.roundDir(n)).Files {
			if _, ok := base[p]; !ok {
				base[p] = t.fileBefore(p, n)
			}
		}
	}
	paths := make([]string, 0, len(base))
	for p := range base {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var sb strings.Builder
	files := 0
	for _, p := range paths {
		after := t.fileAfter(p, last, nums)
		d := unifiedDiff(p, base[p].content, after.content, base[p].exists, after.exists)
		if d != "" {
			sb.WriteString(d)
			files++
		}
	}
	return sb.String(), files
}

func (c *Controller) handleTaskPatch(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	round, _ := strconv.Atoi(r.URL.Query().Get("round"))
	patch, n := t.taskPatch(round)
	if n == 0 {
		writeJSON(w, 404, map[string]string{"error": "no changes were kept for this task"})
		return
	}
	name := t.slugName()
	if round > 0 {
		name += "-request-" + strconv.Itoa(round)
	}
	w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.patch"`)
	io.WriteString(w, patch)
}

func (t *Task) slugName() string {
	t.mu.Lock()
	name := slug(t.Title)
	t.mu.Unlock()
	if name == "" {
		name = t.ID
	}
	return name
}

// handoffText is HANDOFF.md.
func (t *Task) handoffText(patchFiles int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var b strings.Builder
	title := firstNonEmpty(t.Title, "Outpost task")
	fmt.Fprintf(&b, "# Hand-off: %s\n\n", title)
	fmt.Fprintf(&b, "This project was written by Outpost, a self-hosted cluster of local models, and is handed to you to finish or improve. Read this file first, then the code. Exported %s.\n\n", time.Now().Format("2006-01-02 15:04"))

	b.WriteString("## Where it stands\n\n")
	status := t.Status
	switch {
	case t.TestStatus == "failing":
		fmt.Fprintf(&b, "- The tests **still fail** after %d fix round(s). Making them pass is the first job.\n", t.FixRounds)
	case t.TestStatus == "passed":
		b.WriteString("- The tests pass.\n")
	case t.Plan != nil && t.Plan.TestCommand == "":
		b.WriteString("- There are no automated tests.\n")
	default:
		b.WriteString("- The tests were not run (no node could run commands).\n")
	}
	if status == "failed" && t.Error != "" {
		fmt.Fprintf(&b, "- The last request stopped with an error: %s\n", t.Error)
	}
	if t.Plan != nil {
		if t.Plan.TestCommand != "" {
			fmt.Fprintf(&b, "- Test command: `%s`", t.Plan.TestCommand)
			if t.Plan.SetupCommand != "" {
				fmt.Fprintf(&b, " (after `%s`)", t.Plan.SetupCommand)
			}
			b.WriteString("\n")
		}
		if t.Plan.BuildCommand != "" {
			fmt.Fprintf(&b, "- Build command: `%s`\n", t.Plan.BuildCommand)
		}
		if t.Plan.Language != "" {
			fmt.Fprintf(&b, "- Language: %s\n", t.Plan.Language)
		}
	}
	if patchFiles > 0 {
		fmt.Fprintf(&b, "- `outpost-changes.patch` holds every change Outpost made (%d file(s)), as a git diff against the files it started from.\n", patchFiles)
	}
	b.WriteString("\n")

	b.WriteString("## What was asked\n\n")
	for _, m := range t.Messages {
		if m.Superseded || m.Role != "user" {
			continue
		}
		switch m.Kind {
		case "text", "steer", "answers":
			txt := strings.TrimSpace(m.Text)
			if txt == "" {
				continue
			}
			label := "Request"
			if m.Kind == "steer" {
				label = "Note while working"
			} else if m.Kind == "answers" {
				label = "Answers to the planner's questions"
			}
			fmt.Fprintf(&b, "**%s %d:**\n\n%s\n\n", label, m.Round, quoteBlock(txt))
		}
	}
	if t.Instructions != "" {
		fmt.Fprintf(&b, "Standing instructions for this work:\n\n%s\n\n", quoteBlock(t.Instructions))
	}

	if p := t.Plan; p != nil {
		b.WriteString("## The plan (latest request)\n\n")
		if p.Summary != "" {
			fmt.Fprintf(&b, "%s\n\n", p.Summary)
		}
		for _, s := range p.Subtasks {
			st := t.subtaskIfAny(s.ID)
			state := ""
			if st != nil {
				state = st.Status
				if st.Status == "failed" && st.Error != "" {
					state += ": " + st.Error
				}
			}
			fmt.Fprintf(&b, "- **%s** (%s) `%s`", s.Title, firstNonEmpty(state, "not run"), strings.Join(s.Files, "`, `"))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## What each request changed\n\n")
	for _, m := range t.Messages {
		if m.Superseded || (m.Kind != "result" && m.Kind != "hand_edit") || len(m.Files) == 0 {
			continue
		}
		who := "Outpost"
		if m.Kind == "hand_edit" {
			who = "The owner, by hand"
		}
		fmt.Fprintf(&b, "- Request %d (%s): %s\n", m.Round, who, strings.Join(m.Files, ", "))
	}
	b.WriteString("\n")

	if t.TestOutput != "" && t.TestStatus != "passed" {
		out := t.TestOutput
		if len(out) > 12000 {
			out = "…\n" + out[len(out)-12000:]
		}
		fmt.Fprintf(&b, "## Last test output\n\n```\n%s\n```\n\n", strings.TrimRight(out, "\n"))
	}

	b.WriteString("## Suggested next steps\n\n")
	if t.TestStatus == "failing" {
		b.WriteString("1. Run the test command and fix the failures above.\n2. Check the code against the requests; Outpost's local models may have missed details.\n")
	} else {
		b.WriteString("1. Run the tests (if any) to confirm everything still works on your machine.\n2. Review the code against the requests above and improve what is weak.\n")
	}
	return b.String()
}

func quoteBlock(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

// handoffPointer is added to each assistant's instructions file.
const handoffPointer = `## Outpost hand-off

This project was handed over from Outpost. Before changing anything, read HANDOFF.md at the project root: it has the original request, the plan, what was changed, the test command and any failing test output. outpost-changes.patch has Outpost's changes as a git diff.
`

// handoffFiles lists the assistants' instruction files.
var handoffFiles = []struct{ path, head string }{
	{"AGENTS.md", "# Agent instructions\n\n"},
	{"CLAUDE.md", "# Claude Code instructions\n\n"},
	{"GEMINI.md", "# Gemini CLI instructions\n\n"},
	{".github/copilot-instructions.md", ""},
	{".cursor/rules/outpost-handoff.mdc", "---\ndescription: Outpost hand-off notes\nalwaysApply: true\n---\n\n"},
}

func (c *Controller) handleTaskHandoff(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	if !t.idle() {
		writeJSON(w, 409, map[string]string{"error": "wait until the task has stopped"})
		return
	}
	patch, nfiles := t.taskPatch(0)
	extra := map[string]string{"HANDOFF.md": t.handoffText(nfiles)}
	if nfiles > 0 {
		extra["outpost-changes.patch"] = patch
	}
	repo := t.repoDir()
	for _, f := range handoffFiles {
		if b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(f.path))); err == nil {
			extra[f.path] = strings.TrimRight(string(b), "\n") + "\n\n" + handoffPointer
		} else {
			extra[f.path] = f.head + handoffPointer
		}
	}
	name := t.slugName() + "-handoff"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.zip"`)
	zw := zip.NewWriter(w)
	now := time.Now()
	for _, it := range dirItems(repo, name+"/") {
		rel := strings.TrimPrefix(it.name, name+"/")
		if _, replaced := extra[rel]; replaced || rel == ".git" || strings.HasPrefix(rel, ".git/") {
			continue
		}
		in, err := os.Open(it.path)
		if err != nil {
			continue
		}
		hdr := &zip.FileHeader{Name: it.name, Method: zip.Deflate, Modified: now}
		hdr.SetMode(0o644)
		if st, err := in.Stat(); err == nil && st.Mode()&0o111 != 0 {
			hdr.SetMode(0o755)
		}
		if fw, err := zw.CreateHeader(hdr); err == nil {
			_, _ = io.Copy(fw, in)
		}
		in.Close()
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		hdr := &zip.FileHeader{Name: name + "/" + k, Method: zip.Deflate, Modified: now}
		hdr.SetMode(0o644)
		if fw, err := zw.CreateHeader(hdr); err == nil {
			_, _ = io.WriteString(fw, extra[k])
		}
	}
	_ = zw.Close()
}
