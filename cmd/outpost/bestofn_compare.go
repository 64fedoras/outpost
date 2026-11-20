package main

// Best-of-N comparison (idea 42). When several nodes wrote the same subtask
// and a judge picked one, every candidate's files, the syntax-check result and
// the judge's reason are kept in the task's round folder
// (tasks/<id>/rounds/<n>/bestof/<subtask>.json). The web UI shows them side by
// side, and on a stopped task the owner can pick a different version: its files
// replace the subtask's files (kept for Undo like any write of the round) and
// the tests run again.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type bonRecordCand struct {
	Label     string            `json:"label"`
	Node      string            `json:"node"`
	NodeName  string            `json:"node_name"`
	Model     string            `json:"model,omitempty"`
	TokPerSec float64           `json:"tok_per_s,omitempty"`
	Dropped   string            `json:"dropped,omitempty"` // why the syntax check dropped it
	Files     map[string]string `json:"files"`
}

type bonRecord struct {
	Sub    string          `json:"sub"`
	Title  string          `json:"title"`
	Round  int             `json:"round"`
	Time   time.Time       `json:"time"`
	Files  []string        `json:"files"`
	Winner string          `json:"winner"`           // label the judge (or the fallback) chose
	Judge  string          `json:"judge,omitempty"`  // node name of the judge
	Reason string          `json:"reason,omitempty"` // the judge's reason
	Picked string          `json:"picked,omitempty"` // label the owner picked instead, if any
	Cands  []bonRecordCand `json:"cands"`
}

var bonSubIDRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func (t *Task) bonPath(round int, sub string) (string, error) {
	if !bonSubIDRe.MatchString(sub) || sub == "." || sub == ".." {
		return "", errors.New("bad subtask id")
	}
	return filepath.Join(t.roundDir(round), "bestof", sub+".json"), nil
}

func (t *Task) saveBonRecord(rec *bonRecord) error {
	p, err := t.bonPath(rec.Round, rec.Sub)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return writeFileAtomic(p, b, 0o644)
}

func (t *Task) loadBonRecord(round int, sub string) (*bonRecord, error) {
	p, err := t.bonPath(round, sub)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var rec bonRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// keepBestOf saves the candidates of a decided best-of-N subtask and points
// the subtask at the record. Failing to save only costs the comparison view.
func (c *Controller) keepBestOf(t *Task, spec PlanSubtask, cands []*bonCand, dropped map[*bonCand]string, win *bonCand, judge, reason string) {
	t.mu.Lock()
	round := max(t.Round, 1)
	t.mu.Unlock()
	rec := &bonRecord{Sub: spec.ID, Title: spec.Title, Round: round, Time: time.Now(), Files: spec.Files, Winner: win.label, Reason: reason}
	if judge != "" {
		rec.Judge = c.nodeLabel(judge)
	}
	for _, cd := range cands {
		rc := bonRecordCand{Label: cd.label, Node: cd.node, NodeName: c.nodeLabel(cd.node), Model: c.loadedModelName(cd.node), Dropped: dropped[cd], Files: map[string]string{}}
		if cd.res.resp != nil {
			rc.TokPerSec = cd.res.resp.TokensPerSec
		}
		for _, f := range spec.Files {
			rc.Files[f] = cd.res.files[f]
		}
		rec.Cands = append(rec.Cands, rc)
	}
	if err := t.saveBonRecord(rec); err != nil {
		t.logf("warn", "%s: could not keep the best-of-%d copies for comparison: %v", spec.ID, len(cands), err)
		return
	}
	t.mu.Lock()
	st := t.subtask(spec.ID)
	st.BestOfN, st.BestOfRound = len(cands), round
	t.saveLocked()
	t.mu.Unlock()
}

// bonView is the record plus what the page needs to know now.
type bonView struct {
	*bonRecord
	Current string `json:"current"`  // label whose files are in the project now, "" if they were changed since
	CanPick bool   `json:"can_pick"` // the task has stopped, so a different version may be picked
}

func (c *Controller) bonForRequest(w http.ResponseWriter, r *http.Request) (*Task, *bonRecord, bool) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return nil, nil, false
	}
	sid := r.PathValue("sid")
	t.mu.Lock()
	round := 0
	for _, s := range t.Subtasks {
		if s.ID == sid && s.BestOfN > 1 {
			round = s.BestOfRound
		}
	}
	t.mu.Unlock()
	if q := r.URL.Query().Get("round"); q != "" {
		round, _ = strconv.Atoi(q)
	}
	rec, err := t.loadBonRecord(round, sid)
	if round <= 0 || err != nil {
		writeJSON(w, 404, map[string]string{"error": "no kept versions for this subtask"})
		return nil, nil, false
	}
	return t, rec, true
}

func (t *Task) bonCurrent(rec *bonRecord) string {
	repo := t.repoDir()
	for _, cd := range rec.Cands {
		same := true
		for _, f := range rec.Files {
			p, err := safeJoin(repo, f)
			if err != nil {
				return ""
			}
			b, err := os.ReadFile(p)
			if err != nil || string(b) != cd.Files[f] {
				same = false
				break
			}
		}
		if same {
			return cd.Label
		}
	}
	return ""
}

// handleBestOfView: GET /api/tasks/{id}/subtasks/{sid}/bestof
func (c *Controller) handleBestOfView(w http.ResponseWriter, r *http.Request) {
	t, rec, ok := c.bonForRequest(w, r)
	if !ok {
		return
	}
	t.mu.Lock()
	stopped := t.Status == "done" || t.Status == "failed" || t.Status == "cancelled"
	t.mu.Unlock()
	writeJSON(w, 200, bonView{bonRecord: rec, Current: t.bonCurrent(rec), CanPick: stopped})
}

// handleBestOfPick: POST /api/tasks/{id}/subtasks/{sid}/bestof {"label":"B"}
// writes that version's files and runs the tests again.
func (c *Controller) handleBestOfPick(w http.ResponseWriter, r *http.Request) {
	t, rec, ok := c.bonForRequest(w, r)
	if !ok {
		return
	}
	var req struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	var pick *bonRecordCand
	for i := range rec.Cands {
		if rec.Cands[i].Label == strings.ToUpper(strings.TrimSpace(req.Label)) {
			pick = &rec.Cands[i]
		}
	}
	if pick == nil {
		writeJSON(w, 400, map[string]string{"error": "no such version"})
		return
	}
	t.mu.Lock()
	status := t.Status
	t.mu.Unlock()
	if status != "done" && status != "failed" && status != "cancelled" {
		writeJSON(w, 409, map[string]string{"error": "stop the task or wait for it to finish first"})
		return
	}
	for _, f := range rec.Files {
		if err := t.writeFile(f, pick.Files[f]); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
	}
	t.touch(rec.Files...)
	rec.Picked = pick.Label
	if pick.Label == rec.Winner {
		rec.Picked = ""
	}
	_ = t.saveBonRecord(rec)

	who := pick.NodeName
	if who == "" {
		who = pick.Node
	}
	t.mu.Lock()
	st := t.subtask(rec.Sub)
	st.Node, st.NodeName, st.Model, st.TokPerSec = pick.Node, who, pick.Model, pick.TokPerSec
	st.Reviewer, st.ReviewFix, st.ReviewNote = "", false, ""
	if rec.Picked != "" {
		st.BestOf = "you picked " + who + "'s version"
	} else {
		st.BestOf = who + " won: " + rec.Reason
	}
	test := t.Plan != nil && t.Plan.TestCommand != ""
	text := "Using version " + pick.Label + " of " + rec.Title + " (from " + who + ")"
	if test {
		text += ", then running the tests again."
		t.Ended, t.Error, t.PausedFrom = nil, "", ""
		t.TestStatus, t.TestOutput, t.FixRounds = "", "", 0
		t.BuildStatus, t.BuildOutput = "", ""
		t.Status = "running"
	} else {
		text += "."
		t.saveLocked()
	}
	t.addMsgLocked(TaskMessage{Role: "event", Kind: "event", Text: text})
	t.mu.Unlock()
	t.logf("info", "%s", text)
	if test {
		c.startTask(t, c.execTask)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "tests": test})
}
