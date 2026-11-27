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

// startProjectResearch runs a research question in the project's conversation chat when it is idle.
func (c *Controller) startProjectResearch(ch *Chat, p *Project, question string) error {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if ch.Busy != "" {
		return errors.New("research is already running in this project; wait for it or press Stop")
	}
	c.beginProjectResearchLocked(ch, p, question)
	return nil
}

// beginProjectResearchLocked adds a live research answer to the conversation and runs it in the
// background. ch.mu must be held and ch.Busy empty. The conversation counts as busy meanwhile
// (Stop cancels the run); the project page shows it as a research run, not as thinking.
func (c *Controller) beginProjectResearchLocked(ch *Chat, p *Project, question string) {
	ctx, cancel := context.WithTimeout(context.Background(), researchTimeout)
	id := newID("m")
	ch.Busy, ch.projRes, ch.cancel = "answering", true, cancel
	ch.Messages = append(ch.Messages, ChatMsg{ID: id, Role: "assistant", At: time.Now(), Node: "Research", Question: question,
		Research: &ResearchRun{Stage: "planning"}})
	ch.saveLocked()
	go func() {
		defer cancel()
		msg := c.runProjectResearch(ctx, ch, p, id, question)
		ch.mu.Lock()
		for i := range ch.Messages {
			if ch.Messages[i].ID == id {
				msg.ID, msg.Role, msg.At, msg.Question = id, "assistant", time.Now(), question
				ch.Messages[i] = msg
			}
		}
		ch.Busy, ch.projRes, ch.cancel, ch.Updated = "", false, nil, time.Now()
		ch.saveLocked()
		ch.mu.Unlock()
		taskLive.changed("chat_" + ch.ID)
	}()
}

func (c *Controller) runProjectResearch(ctx context.Context, ch *Chat, p *Project, id, question string) ChatMsg {
	st := &researchState{run: ResearchRun{Stage: "planning"}, liveKey: "chat_" + ch.ID, publish: func(r *ResearchRun) {
		ch.mu.Lock()
		for i := range ch.Messages {
			if ch.Messages[i].ID == id {
				ch.Messages[i].Research = r
			}
		}
		ch.mu.Unlock()
	}}
	hist := []ChatMessage{{Role: "system", Content: ""}, {Role: "user", Content: question}}
	r := c.runResearch(ctx, st, hist, func() {})
	who := "Research"
	if r.Node != "" {
		who = "Research · report by " + r.Node
	}
	msg := ChatMsg{Text: r.Text, Node: who, Error: r.Error, Research: r.Research}
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
