package main

// Research from the project conversation: when the planner writes a
// [start research] block, the question runs through chat Research mode
// (research.go: sub-questions as lanes on whichever nodes are free, a report
// with sources), shown live as its own answer in the conversation, and the
// report is saved to the project's Stash so the project's tasks can find it.
// Needs "Let chats search the web". One run per project at a time; Stop in the
// conversation cancels it. Nodes keep nothing.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
)

var researchBlockRe = regexp.MustCompile(`(?is)\[start research\]\s*(.*?)\s*\[/start research\]`)

const projectResearchHelp = `

For a question that needs many sources (comparing products or projects, how something works in depth, the state of a field), start research instead of searching it all yourself:

[start research]
The complete question, understandable on its own.
[/start research]

Research splits the question across the cluster's computers, searches the web on each, writes a report with sources, and saves it in the project's Stash, where tasks can read it. Start at most one per answer, and only when the owner asks for work that needs it or agrees. For a quick look-up (what a name is), use web_search yourself.`

// splitResearchBlocks takes the [start research] blocks out of an answer and
// keeps the first question.
func splitResearchBlocks(text string) (string, string) {
	q := ""
	if m := researchBlockRe.FindStringSubmatch(text); m != nil {
		q = strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(researchBlockRe.ReplaceAllString(text, "")), q
}

// researchRunning reports whether a project research run is working (pc.mu held).
func (pc *projectChat) researchRunningLocked() bool {
	return pc.resCancel != nil
}

// startProjectResearch adds a live research answer and runs it in the background.
func (c *Controller) startProjectResearch(p *Project, pc *projectChat, question string) error {
	pc.mu.Lock()
	if pc.researchRunningLocked() {
		pc.mu.Unlock()
		return errors.New("research is already running in this project; wait for it or press Stop")
	}
	ctx, cancel := context.WithTimeout(context.Background(), researchTimeout)
	id := newID("m")
	pc.resCancel = cancel
	pc.Messages = append(pc.Messages, ProjectChatMsg{ID: id, Role: "assistant", Time: time.Now(), Who: "Research", Question: question,
		Research: &ResearchRun{Stage: "planning"}})
	pc.saveLocked()
	pc.mu.Unlock()
	go func() {
		defer cancel()
		msg := c.runProjectResearch(ctx, p, pc, id, question)
		pc.mu.Lock()
		for i := range pc.Messages {
			if pc.Messages[i].ID == id {
				msg.ID, msg.Role, msg.Time, msg.Question = id, "assistant", time.Now(), question
				pc.Messages[i] = msg
			}
		}
		pc.resCancel = nil
		pc.saveLocked()
		pc.mu.Unlock()
	}()
	return nil
}

func (c *Controller) runProjectResearch(ctx context.Context, p *Project, pc *projectChat, id, question string) ProjectChatMsg {
	st := &researchState{run: ResearchRun{Stage: "planning"}, liveKey: "projres_" + p.ID, publish: func(r *ResearchRun) {
		pc.mu.Lock()
		for i := range pc.Messages {
			if pc.Messages[i].ID == id {
				pc.Messages[i].Research = r
			}
		}
		pc.mu.Unlock()
	}}
	hist := []ChatMessage{{Role: "system", Content: ""}, {Role: "user", Content: question}}
	r := c.runResearch(ctx, st, hist, func() {})
	who := "Research"
	if r.Node != "" {
		who = "Research · report by " + r.Node
	}
	msg := ProjectChatMsg{Text: r.Text, Who: who, Error: r.Error, Research: r.Research}
	if r.Error != "" || strings.TrimSpace(r.Text) == "" {
		return msg
	}
	msg.Note = r.Note
	name, err := c.saveResearchToStash(p, question, r.Text)
	if err != nil {
		log.Printf("project research: could not save the report to the stash: %v", err)
		msg.Error = "the report could not be saved to the project's Stash: " + err.Error()
		return msg
	}
	msg.Saved = name
	return msg
}

// saveResearchToStash adds the report to the project's Stash, creating it the
// first time, and returns the resource name.
func (c *Controller) saveResearchToStash(p *Project, question, report string) (string, error) {
	s, err := c.projectStash(p, true)
	if err != nil {
		return "", err
	}
	title := regexp.MustCompile(`[^\pL\pN ,.()-]+`).ReplaceAllString(question, " ")
	title = strings.Trim(strings.Join(strings.Fields(title), " "), " .")
	if len([]rune(title)) > 60 {
		title = strings.TrimSpace(strings.TrimRight(string([]rune(title)[:60]), " ,.("))
	}
	name := "Research - " + title + ".md"
	if title == "" {
		name = "Research report.md"
	}
	body := fmt.Sprintf("# Research: %s\n\n_Written by Outpost research on %s from web sources. A draft from local models: check the sources._\n\n%s\n",
		strings.TrimSpace(question), time.Now().Format("2006-01-02"), strings.TrimSpace(report))
	res, err := c.stashAddResource(s, name, []byte(body))
	if err != nil {
		return "", err
	}
	return res.Name, nil
}
