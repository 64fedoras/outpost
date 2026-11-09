package main

// The planner: prompts, plan parsing and validation.
// Split out of tasks.go; see that file for the task model and overview.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const plannerSystem = `You are the planner for Outpost, which splits a software task across several small local coding models that work in parallel and never talk to each other.

Each worker sees only: the project summary, the list of all subtasks, its own instructions, the files of the subtasks it depends on, and the current content of any existing file it must change. Workers are small models, so:
- Keep each subtask small and concrete: one to three files, ideally under 200 lines in total.
- Every file belongs to exactly one subtask. Use exact paths relative to the project root.
- Spell out interfaces in the instructions: module names, function and class names, signatures, data formats, and which file defines them. Two subtasks must never guess at each other's names.
- Use depends_on when a subtask needs to read another subtask's files.
- Include a README.md that explains how to run the project and its tests.
- Prefer the language's standard library. Avoid anything that needs network access at test time.
- If the project can be tested automatically, include small tests and give a test_command that runs them from the project root on Linux (for example "python3 -m unittest discover -s tests"). Use setup_command only if something must be installed or built first. Leave test_command empty if testing is not practical.
- Tag every subtask with a difficulty: "easy" (boilerplate, config, README, simple data files), "normal", or "hard" (tricky logic, algorithms, or code that many other files depend on). Hard subtasks go to the strongest model, so use the tag sparingly.
- If the project produces something to ship (a binary, a package, a bundle), give a build_command that builds it from the project root (for example "sh build.sh") and artifacts, the glob patterns of the files it produces (for example ["dist/*"]; one * matches within a directory, there is no **). The build runs after the tests pass. Leave both empty for a project that is only source code.
- Use at most 12 subtasks.
- To remove files, list them in "delete"; to move or rename a file, add {"from": "old/path", "to": "new/path"} to "rename" (its content moves with it; a subtask may then change the file at its new path). Do not write a subtask that empties or stubs out a file to "remove" it. Leave both lists empty when nothing is removed or moved.
- If the project has a web page, set preview_page to its HTML file (for example "index.html") so it can be screenshotted and checked. For a server app that needs running first, set preview_command (starts it, e.g. "python3 app.py") and preview_url (where it listens, e.g. "http://127.0.0.1:8000"). Leave them empty otherwise.

Follow-up requests on an existing project include its current files. Plan only the files to add or change; a subtask that changes an existing file lists it, and its worker gets the file's current content and outputs the complete new version. Keep the summary describing the whole project, and keep test_command running the whole test suite, and keep build_command and artifacts unless the build changes.

Questions: if the request leaves open a choice that would change the design (language, interface, storage, platform, scope) and a wrong guess would waste the work, you may ask before planning, once per request. Prefer sensible defaults; most requests need no questions. To ask, answer with only:
{"questions": [{"id": "q1", "question": "short question", "options": ["option A", "option B"], "multi": false}], "note": "one sentence on why you are asking"}
Use at most 4 questions with 2 to 4 short options each; the user can also type their own answer.

Otherwise answer with only a JSON object, no other text:
{"title": "short name", "summary": "what the program does and how it is structured, including the key interfaces", "language": "main language", "setup_command": "", "test_command": "", "preview_page": "", "preview_command": "", "preview_url": "", "build_command": "", "artifacts": [], "delete": [], "rename": [], "note": "one or two sentences to the user: the approach and any assumption you made", "subtasks": [{"id": "t1", "title": "short title", "files": ["path/file.ext"], "depends_on": [], "difficulty": "normal", "instructions": "precise instructions"}]}`

// plannerIndexHelp is added to the planner's instructions when the project is
// too large to paste in, so it sees an index instead.
const plannerIndexHelp = `

This project is large, so you see an index (each file's path, size and top-level declarations) instead of the file contents. If you need to read some files before planning, answer once with only {"need_files": ["path/one", "path/two"]} (at most 12 paths) and they will be added. Otherwise plan from the index; a worker is given the current content of every file its subtask changes and the declarations of the files it depends on.`

func (c *Controller) planTask(ctx context.Context, t *Task) {
	t.mu.Lock()
	mayAsk := !t.Asked && !t.Unattended
	t.flushQueuedLocked() // notes still waiting (Send now, a re-plan) join the round's messages, so they are in this prompt
	t.mu.Unlock()
	budget := c.plannerBudget(t)
	user := c.plannerPrompt(t, budget, nil)
	user += c.webContext(ctx, c.roundURLs(t))
	c.mu.Lock()
	webSearch := c.settings.WebSearch
	c.mu.Unlock()
	t.mu.Lock()
	webSearch = webSearch && t.Planner == "claude"
	t.mu.Unlock()
	indexed := t.roundHasFiles() && repoNeedsIndex(t.repoDir(), budget)
	t.mu.Lock()
	mockup := t.Mockup
	testFirst := t.TestFirst && t.Round == 1 && !t.Seeded
	t.mu.Unlock()
	fetched := false
	var plan *Plan
	var lastErr error
	for attempt := 0; attempt < 3 && plan == nil; attempt++ {
		req := llmRequest{System: plannerSystem + c.toolsPrompt(), User: user, MaxTokens: 8000, Temperature: 0.2, Label: "Planning", WebSearch: webSearch, Images: t.roundImages()}
		if indexed {
			req.System += plannerIndexHelp
		}
		if mockup {
			req.System += mockupHelp
		}
		if c.agentEnabled() {
			req.System += plannerInvestigateHelp
		}
		if t.Unattended {
			req.User += "\n\nNobody is watching this task (it was started by a schedule), so do not ask questions; produce the plan, using sensible defaults for anything still open."
		} else if !mayAsk {
			req.User += "\n\nYou have already asked your questions for this request. Do not ask again; produce the plan, using sensible defaults for anything still open."
		}
		if attempt > 0 {
			req.User += "\n\nYour previous answer could not be used: " + lastErr.Error() + ". Answer again with only the JSON object."
			if parseFailed(lastErr) && !t.cloudPlanner() {
				// A local model wrote something that is not JSON: this time llama-server
				// holds it to the plan's shape (idea 8, only as a retry).
				req.JSONSchema = planSchema
				t.logf("info", "Asking the planner again with the answer held to the plan's JSON shape.")
			}
		}
		resp, err := c.plannerCall(ctx, t, req)
		if err != nil {
			if ctx.Err() == nil {
				c.failTask(t, err)
			}
			return
		}
		if ctx.Err() != nil { // sent now, stopped or steered: a newer pass owns the task
			return
		}
		if notes := t.takeQueued(); len(notes) > 0 {
			// The user added to the request while this answer was being written.
			// Keep the answer and give the planner the note in the same conversation.
			user += foldNotesPrompt(resp.Text, notes)
			t.logf("info", "Giving the planner your note (%d) with its answer so far.", len(notes))
			attempt-- // not a failed attempt
			continue
		}
		raw := []byte(extractJSON(resp.Text))
		var need struct {
			NeedFiles []string `json:"need_files"`
		}
		if indexed && !fetched && json.Unmarshal(raw, &need) == nil && len(need.NeedFiles) > 0 {
			fetched = true
			user = c.plannerPrompt(t, budget, need.NeedFiles)
			t.logf("info", "The planner asked for %d file(s) from the project index.", len(need.NeedFiles))
			attempt-- // the file exchange is not a failed attempt
			continue
		}
		var ask struct {
			Questions []Question `json:"questions"`
			Note      string     `json:"note"`
		}
		if json.Unmarshal(raw, &ask) == nil && len(ask.Questions) > 0 {
			if !mayAsk {
				lastErr = errors.New("you asked questions again; produce the plan instead")
				continue
			}
			qs := cleanQuestions(ask.Questions)
			if len(qs) == 0 {
				lastErr = errors.New("the questions had no usable options")
				continue
			}
			t.mu.Lock()
			if ctx.Err() != nil { // the user steered meanwhile; a newer planning run owns the task
				t.mu.Unlock()
				return
			}
			if len(t.Queued) > 0 { // a note arrived after the last check: plan again with it
				t.mu.Unlock()
				c.replan(t, "Planning again with your note.")
				return
			}
			t.Asked = true
			t.Status = "awaiting_answers"
			t.addMsgLocked(TaskMessage{Role: "assistant", Kind: "questions", Questions: qs, Text: strings.TrimSpace(ask.Note)})
			t.mu.Unlock()
			c.notifyTask(t, "question", fmt.Sprintf("The planner has %d question(s) for you.", len(qs)))
			t.logf("info", "The planner has %d question(s) before planning.", len(qs))
			return
		}
		p := &Plan{}
		if err := json.Unmarshal(raw, p); err != nil {
			lastErr = &parseError{fmt.Errorf("not valid JSON (%v)", err)}
			continue
		}
		mergeSharedFiles(p)
		if err := validatePlan(p); err != nil {
			lastErr = err
			continue
		}
		if testFirst && !mockup {
			if err := enforceTestFirst(p); err != nil {
				lastErr = err
				continue
			}
			if err := validatePlan(p); err != nil { // the new dependencies must still make a valid plan
				lastErr = err
				continue
			}
		}
		if mockup {
			p.Mockup, p.SetupCommand, p.TestCommand = true, "", ""
			if err := validateMockupPlan(p); err != nil {
				lastErr = err
				continue
			}
		}
		plan = p
	}
	if plan == nil {
		c.failTask(t, fmt.Errorf("the planner did not produce a usable plan: %v", lastErr))
		return
	}
	t.mu.Lock()
	if ctx.Err() != nil { // the user steered meanwhile; a newer planning run owns the task
		t.mu.Unlock()
		return
	}
	if len(t.Queued) > 0 { // a note arrived after the last check: plan again with it
		t.mu.Unlock()
		c.replan(t, "Planning again with your note.")
		return
	}
	t.Plan = plan
	if t.Title == "" && plan.Title != "" {
		t.Title = plan.Title
		go c.spendRename(t.ID, plan.Title) // the Spending page shows the same name as the sidebar
	}
	t.Subtasks = make([]*SubtaskState, len(plan.Subtasks))
	for i, s := range plan.Subtasks {
		t.Subtasks[i] = &SubtaskState{ID: s.ID, Status: "pending"}
	}
	t.EstimateUSD = 0
	if isCloudPlanner(t.Planner) {
		files := 0
		for _, s := range plan.Subtasks {
			files += len(s.Files)
		}
		repoTok := files * 1200
		var fix float64
		if t.Planner == "openrouter" {
			fix = openRouterEstimate(t.ORModel, (repoTok+3000)*3, (repoTok/2+500)*3)
		} else {
			fix = usageCost(t.ClaudeModel, repoTok+3000, repoTok/2+500, 0, 0)
		}
		t.EstimateUSD = t.CostUSD + 1.5*fix
	}
	t.Status = "awaiting_approval"
	cp := *plan
	t.addMsgLocked(TaskMessage{Role: "assistant", Kind: "plan", Plan: &cp, Text: strings.TrimSpace(plan.Note)})
	if t.Unattended && t.Round == 1 {
		now := time.Now()
		t.Approved = &now
		t.Status = "running"
		t.addMsgLocked(TaskMessage{Role: "event", Kind: "event", Text: "Approved on its own: this task was started by a schedule."})
		t.mu.Unlock()
		t.logf("info", "Plan ready: %d subtasks; approved on its own (scheduled).", len(plan.Subtasks))
		c.startTask(t, c.execTask)
		return
	}
	t.mu.Unlock()
	c.notifyTask(t, "plan", fmt.Sprintf("Plan ready with %d subtasks. Review it and approve to start.", len(plan.Subtasks)))
	t.logf("info", "Plan ready: %d subtasks. Review it and approve to start.", len(plan.Subtasks))
}

// cleanQuestions trims the planner's questions to what the UI can show.
func cleanQuestions(in []Question) []Question {
	var out []Question
	for i, q := range in {
		if len(out) == 4 {
			break
		}
		q.Question = strings.TrimSpace(q.Question)
		var opts []string
		for _, o := range q.Options {
			if o = strings.TrimSpace(o); o != "" && len(opts) < 5 {
				opts = append(opts, o)
			}
		}
		if q.Question == "" {
			continue
		}
		q.Options = opts
		q.ID = fmt.Sprintf("q%d", i+1)
		out = append(out, q)
	}
	return out
}

// mergeSharedFiles folds subtasks that claim the same file into the first one
// that claims it, so two workers never write one file at once. Small models do
// this often (for example an index.html split into "markup" and "script").
// The merged subtask keeps every file and instruction, and anything that
// depended on a removed subtask now depends on the survivor.
func mergeSharedFiles(p *Plan) {
	norm := func(f string) string { return strings.TrimPrefix(strings.TrimSpace(f), "./") }
	for {
		owner := map[string]int{}
		from, into := -1, -1
		for i, s := range p.Subtasks {
			for _, f := range s.Files {
				f = norm(f)
				if o, ok := owner[f]; ok && o != i {
					from, into = i, o
					break
				}
				owner[f] = i
			}
			if from >= 0 {
				break
			}
		}
		if from < 0 {
			return
		}
		dst, src := &p.Subtasks[into], p.Subtasks[from]
		have := map[string]bool{}
		for _, f := range dst.Files {
			have[norm(f)] = true
		}
		for _, f := range src.Files {
			if !have[norm(f)] {
				dst.Files = append(dst.Files, f)
				have[norm(f)] = true
			}
		}
		if dst.Title != "" && src.Title != "" {
			dst.Title += " + " + src.Title
		}
		if si := strings.TrimSpace(src.Instructions); si != "" {
			dst.Instructions = strings.TrimSpace(dst.Instructions + "\n\n" + si)
		}
		if src.Kind == "" {
			dst.Kind = ""
		}
		if dst.Difficulty != src.Difficulty && src.Difficulty == "hard" {
			dst.Difficulty = "hard"
		}
		dst.DependsOn = append(dst.DependsOn, src.DependsOn...)
		p.Subtasks = append(p.Subtasks[:from], p.Subtasks[from+1:]...)
		for i := range p.Subtasks {
			s := &p.Subtasks[i]
			seen := map[string]bool{}
			var deps []string
			for _, d := range s.DependsOn {
				if d == src.ID {
					d = p.Subtasks[into].ID
				}
				if d != s.ID && !seen[d] {
					seen[d] = true
					deps = append(deps, d)
				}
			}
			s.DependsOn = deps
		}
	}
}

func validatePlan(p *Plan) error {
	if len(p.Subtasks) == 0 && len(p.Delete) == 0 && len(p.Rename) == 0 {
		return errors.New("the plan has no subtasks")
	}
	if len(p.Subtasks) > maxSubtasks {
		return fmt.Errorf("the plan has %d subtasks; use at most 12", len(p.Subtasks))
	}
	ids := map[string]bool{}
	owner := map[string]string{}
	for i := range p.Subtasks {
		s := &p.Subtasks[i]
		s.ID = strings.TrimSpace(s.ID)
		if s.ID == "" || ids[s.ID] {
			return fmt.Errorf("subtask ids must be unique and non-empty (%q)", s.ID)
		}
		ids[s.ID] = true
		s.Difficulty = normDifficulty(s.Difficulty)
		if k := strings.ToLower(strings.TrimSpace(s.Kind)); k == "investigate" {
			s.Kind = k
		} else {
			s.Kind = ""
		}
		if len(s.Files) == 0 && s.Kind != "investigate" {
			return fmt.Errorf("subtask %s lists no files", s.ID)
		}
		for j, f := range s.Files {
			f = strings.TrimPrefix(strings.TrimSpace(f), "./")
			s.Files[j] = f
			if _, err := safeJoin("/repo", f); err != nil || f == "" {
				return fmt.Errorf("subtask %s has a bad file path %q", s.ID, f)
			}
			if o, ok := owner[f]; ok {
				if o == s.ID {
					continue // listed twice by the same subtask
				}
				return fmt.Errorf("file %s is assigned to both %s and %s", f, o, s.ID)
			}
			owner[f] = s.ID
		}
	}
	if err := validateFileOps(p, owner); err != nil {
		return err
	}
	var pats []string
	for _, a := range p.Artifacts {
		a = strings.TrimPrefix(strings.TrimSpace(a), "./")
		if _, err := safeJoin("/repo", a); err != nil || a == "" {
			return fmt.Errorf("the artifact pattern %q is not a path inside the project", a)
		}
		pats = append(pats, a)
	}
	if len(pats) > 20 {
		return errors.New("the plan lists more than 20 artifact patterns")
	}
	p.Artifacts = pats
	for _, s := range p.Subtasks {
		for _, d := range s.DependsOn {
			if !ids[d] {
				return fmt.Errorf("subtask %s depends on unknown subtask %s", s.ID, d)
			}
		}
	}
	// Reject dependency cycles.
	state := map[string]int{}
	deps := map[string][]string{}
	for _, s := range p.Subtasks {
		deps[s.ID] = s.DependsOn
	}
	var visit func(string) bool
	visit = func(id string) bool {
		switch state[id] {
		case 1:
			return false
		case 2:
			return true
		}
		state[id] = 1
		for _, d := range deps[id] {
			if !visit(d) {
				return false
			}
		}
		state[id] = 2
		return true
	}
	for _, s := range p.Subtasks {
		if !visit(s.ID) {
			return errors.New("subtask dependencies form a cycle")
		}
	}
	return nil
}

func (c *Controller) failTask(t *Task, err error) {
	if errors.Is(err, errCancelled) {
		return
	}
	t.mu.Lock()
	t.Error = err.Error()
	t.addMsgLocked(TaskMessage{Role: "assistant", Kind: "error", Text: err.Error()})
	t.mu.Unlock()
	t.setStatus("failed")
	c.notifyTask(t, "failed", err.Error())
	t.logf("error", "%v", err)
}
