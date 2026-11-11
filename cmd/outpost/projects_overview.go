package main

// The project page (web/project.js): a feed of the project's task requests in the
// middle and the Threads panel on the right, with the artifacts every task in the
// project produced and the project's files. Everything here is read from the
// controller's own task and project folders; nodes keep none of it.

import (
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
)

const (
	overviewMaxFiles     = 3000 // file list cap for the Files dialog
	overviewMaxArtifacts = 200
	overviewPromptChars  = 1500
)

type feedRequest struct {
	Text  string    `json:"text"`
	Time  time.Time `json:"time"`
	Round int       `json:"round"`
}

// feedTask is one task in the project feed: what was asked, and the subtask
// states for the progress strip (the task list carries the status itself).
type feedTask struct {
	ID       string        `json:"id"`
	Created  time.Time     `json:"created"`
	Requests []feedRequest `json:"requests"`
	Subtasks []string      `json:"subtasks"`
	Result   string        `json:"result,omitempty"` // the last round's result note
}

type projectArtifact struct {
	TaskID    string    `json:"task_id,omitempty"`
	TaskTitle string    `json:"task_title,omitempty"`
	Kind      string    `json:"kind"` // shot | build | result | pr
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Preview   string    `json:"preview,omitempty"` // image URL for a thumbnail
	Bytes     int64     `json:"bytes,omitempty"`
	Time      time.Time `json:"time"`
}

type projectOverview struct {
	Feed       []feedTask        `json:"feed"`
	Artifacts  []projectArtifact `json:"artifacts"`
	Files      []repoFile        `json:"files"`
	FilesTotal int               `json:"files_total"`
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func (c *Controller) handleProjectOverview(w http.ResponseWriter, r *http.Request) {
	p := c.getProject(r.PathValue("id"))
	if p == nil {
		writeJSON(w, 404, map[string]string{"error": "no such project"})
		return
	}
	c.mu.Lock()
	var list []*Task
	for _, t := range c.tasks {
		if t.ProjectID == p.ID {
			list = append(list, t)
		}
	}
	c.mu.Unlock()

	out := projectOverview{Feed: []feedTask{}, Artifacts: []projectArtifact{}}
	for _, t := range list {
		t.mu.Lock()
		out.Feed = append(out.Feed, t.feedLocked())
		out.Artifacts = append(out.Artifacts, t.artifactsLocked()...)
		t.mu.Unlock()
	}
	sort.Slice(out.Feed, func(i, j int) bool { return out.Feed[i].Created.Before(out.Feed[j].Created) })

	p.mu.Lock()
	lastPR, repo := p.LastPR, p.repoDir()
	p.mu.Unlock()
	if strings.HasPrefix(lastPR, "https://") {
		out.Artifacts = append(out.Artifacts, projectArtifact{Kind: "pr", Name: "Last pull request", URL: lastPR})
	}
	sort.SliceStable(out.Artifacts, func(i, j int) bool { return out.Artifacts[i].Time.After(out.Artifacts[j].Time) })
	if len(out.Artifacts) > overviewMaxArtifacts {
		out.Artifacts = out.Artifacts[:overviewMaxArtifacts]
	}

	files := listRepo(repo)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	out.FilesTotal = len(files)
	if len(files) > overviewMaxFiles {
		files = files[:overviewMaxFiles]
	}
	out.Files = files
	writeJSON(w, 200, out)
}

func (t *Task) feedLocked() feedTask {
	f := feedTask{ID: t.ID, Created: t.Created, Requests: []feedRequest{}, Subtasks: []string{}}
	for _, m := range t.Messages {
		if m.Superseded {
			continue
		}
		switch {
		case m.Role == "user" && (m.Kind == "text" || m.Kind == ""):
			f.Requests = append(f.Requests, feedRequest{Text: clip(m.Text, overviewPromptChars), Time: m.Time, Round: m.Round})
		case m.Role == "assistant" && m.Kind == "result":
			f.Result = clip(m.Text, 400)
		}
	}
	if len(f.Requests) == 0 { // older tasks kept the first request only in Prompt
		f.Requests = append(f.Requests, feedRequest{Text: clip(t.Prompt, overviewPromptChars), Time: t.Created, Round: 1})
	}
	for _, st := range t.Subtasks {
		f.Subtasks = append(f.Subtasks, st.Status)
	}
	return f
}

// artifactsLocked lists what the task produced, newest last: screenshots, build
// outputs and, once it is done, its result zip.
func (t *Task) artifactsLocked() []projectArtifact {
	title := t.Title
	if title == "" {
		title = firstLine(t.Prompt, 80)
	}
	base := "/api/tasks/" + url.PathEscape(t.ID)
	var out []projectArtifact
	for _, m := range t.Messages {
		if m.Superseded || (m.Kind != "shots" && m.Kind != "mockup") {
			continue
		}
		for _, s := range m.Shots {
			if s.Desktop == "" {
				continue
			}
			u := base + "/shot?name=" + url.QueryEscape(s.Desktop)
			name := s.Page
			if m.Kind == "mockup" {
				name = "Variant " + strings.ToUpper(s.Label)
			}
			if name == "" {
				name = s.Desktop
			}
			out = append(out, projectArtifact{TaskID: t.ID, TaskTitle: title, Kind: "shot", Name: path.Base(name), URL: u, Preview: u, Time: m.Time})
		}
	}
	when := t.Created
	if t.Ended != nil {
		when = *t.Ended
	}
	for _, a := range t.Artifacts {
		out = append(out, projectArtifact{TaskID: t.ID, TaskTitle: title, Kind: "build", Name: path.Base(a.Path), Bytes: a.Bytes, Time: when,
			URL: base + "/artifact?path=" + url.QueryEscape(a.Path)})
	}
	if t.Status == "done" && t.ResultBytes > 0 {
		out = append(out, projectArtifact{TaskID: t.ID, TaskTitle: title, Kind: "result", Name: "Result .zip", Bytes: t.ResultBytes, Time: when,
			URL: base + "/download"})
	}
	return out
}
