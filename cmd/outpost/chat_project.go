package main

// Project conversations on the Chat engine. A project conversation is a Chat with ProjectID set:
// it is answered like any chat (model and node picker, Auto switching, prompt cache affinity,
// images, run code, versions, research) with the project added on top:
//
//   - the project's guidance, instructions and memory go in the system prompt, after the chat's own
//     text, so every turn starts with the same prefix and the node keeps its prompt cache;
//   - what changes as work goes on (the project's tasks and file list) goes after the owner's newest
//     message, never in the system prompt;
//   - the model gets the project's read-only file tools (projects_chat_tools.go), and may write
//     [start task] and [start research] blocks (projects_chat.go);
//   - the project's Stash is searched along with the chat's own stashes;
//   - the picker value "planner" answers with the project's cloud planner (Claude or OpenRouter)
//     through the code in answerProjectChat. Plain chats never get that choice.
//
// A plain chat (ProjectID empty) takes none of these steps: its request is built as before.
//
// The old single project conversation (chat.json in the project folder) is copied into a chat at
// start (migrateProjectChats) and never changed or deleted; /api/projects/{id}/chat keeps its
// shapes and works on that chat.

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// chatPlanner is the picker value that answers a project conversation with the project's planner.
const chatPlanner = "planner"

// projectConvTitle names the chat the old project conversation was copied into.
const projectConvTitle = "Project conversation"

var convCreateMu sync.Mutex // one conversation chat is made per project

func chatFromProjectMsg(m ProjectChatMsg) ChatMsg {
	return ChatMsg{ID: m.ID, Role: m.Role, Text: m.Text, At: m.Time, Tasks: m.Tasks, Node: m.Who, Error: m.Error, Cost: m.Cost,
		Reads: m.Reads, Suggest: m.Suggest, Question: m.Question, Research: m.Research, Saved: m.Saved, Note: m.Note, Offer: m.Offer, research: m.research}
}

func projectMsgFromChat(m ChatMsg) ProjectChatMsg {
	return ProjectChatMsg{ID: m.ID, Role: m.Role, Text: m.Text, Time: m.At, Tasks: m.Tasks, Who: m.Node, Error: m.Error, Cost: m.Cost,
		Reads: m.Reads, Suggest: m.Suggest, Question: m.Question, Research: m.Research, Saved: m.Saved, Note: m.Note, Offer: m.Offer, research: m.research}
}

// convChatID is the id of the chat a project's old conversation is copied into. It is derived from
// the project, so a copy interrupted before the project was saved is found again, not repeated.
func convChatID(p *Project) string { return "chat_" + p.ID }

// migrateProjectChats copies each project's old conversation (chat.json) into a chat. It runs at
// start and is safe to run again: a project with ConvChatID set is skipped, chat.json is only read.
func (c *Controller) migrateProjectChats() {
	c.mu.Lock()
	var ps []*Project
	for _, p := range c.projects {
		ps = append(ps, p)
	}
	c.mu.Unlock()
	for _, p := range ps {
		p.mu.Lock()
		done := p.ConvChatID != ""
		p.mu.Unlock()
		if done {
			continue
		}
		if pc := p.chatLog(); len(pc.view().Messages) > 0 {
			c.migrateProjectChat(p, false)
		}
	}
}

// migrateProjectChat makes the conversation chat for p from its chat.json (empty when there is
// none) and records it as p.ConvChatID. It returns the chat.
func (c *Controller) migrateProjectChat(p *Project, fresh bool) *Chat {
	id := convChatID(p)
	if fresh {
		id = newID("chat") // the owner deleted the conversation: start an empty one, do not copy the old log again
	}
	ch := c.getChat(id)
	if ch == nil {
		ch = &Chat{ID: id, Title: projectConvTitle, Node: chatPlanner, ProjectID: p.ID, Created: p.Created, Updated: p.Created, dir: c.chatsDir()}
		if !fresh {
			pc := p.chatLog()
			pc.mu.Lock()
			for _, m := range pc.Messages {
				ch.Messages = append(ch.Messages, chatFromProjectMsg(m))
			}
			ch.MemTries = pc.MemTries
			ch.OffersDeclined = append([]string(nil), pc.OffersDeclined...)
			pc.mu.Unlock()
		}
		if n := len(ch.Messages); n > 0 {
			if t := ch.Messages[0].At; !t.IsZero() {
				ch.Created = t
			}
			if t := ch.Messages[n-1].At; !t.IsZero() {
				ch.Updated = t
			}
		}
		if ch.Messages == nil {
			ch.Messages = []ChatMsg{}
		}
		ch.mu.Lock()
		ch.saveLocked()
		ch.mu.Unlock()
		c.chatMu.Lock()
		if c.chats == nil {
			c.chats = map[string]*Chat{}
		}
		c.chats[id] = ch
		c.chatMu.Unlock()
	}
	p.mu.Lock()
	p.ConvChatID = id
	p.mu.Unlock()
	if err := p.save(); err != nil {
		log.Printf("project %s: could not save the conversation link: %v", p.Name, err)
	}
	return ch
}

// projectConvChat returns the project's conversation chat. With create it is made when missing (from
// the old chat.json if that has messages); without, nil means the project has none yet.
func (c *Controller) projectConvChat(p *Project, create bool) *Chat {
	p.mu.Lock()
	id := p.ConvChatID
	p.mu.Unlock()
	if ch := c.getChat(id); ch != nil && id != "" {
		return ch
	}
	if !create {
		return nil
	}
	convCreateMu.Lock()
	defer convCreateMu.Unlock()
	p.mu.Lock()
	id = p.ConvChatID
	p.mu.Unlock()
	if ch := c.getChat(id); ch != nil && id != "" {
		return ch
	}
	return c.migrateProjectChat(p, id != "")
}

// projectView is the conversation in the shape the project page reads (projectChatView).
func (ch *Chat) projectView() projectChatView {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	v := projectChatView{Messages: make([]ProjectChatMsg, 0, len(ch.Messages)), Busy: ch.Busy != "" && !ch.projRes,
		Reading: append([]string(nil), ch.Reading...), Research: ch.projRes}
	for _, m := range ch.Messages {
		v.Messages = append(v.Messages, projectMsgFromChat(m))
	}
	if len(v.Reading) == 0 {
		v.Reading = nil
	}
	return v
}

// projectChatViewFor is what the overview shows as the project's conversation.
func (c *Controller) projectChatViewFor(p *Project) projectChatView {
	if ch := c.projectConvChat(p, false); ch != nil {
		return ch.projectView()
	}
	return p.chatLog().view() // not copied yet: the old log is still the conversation
}

// setNodeLocked sets the chat's node choice. "planner" is only for project conversations.
func (ch *Chat) setNodeLocked(node string) {
	if node == chatPlanner && ch.ProjectID == "" {
		return
	}
	ch.Node = node
}

// removeChat deletes a chat and its files; false when there is no such chat.
func (c *Controller) removeChat(id string) bool {
	c.chatMu.Lock()
	ch := c.chats[id]
	delete(c.chats, id)
	c.chatMu.Unlock()
	if ch == nil {
		return false
	}
	ch.mu.Lock()
	if ch.cancel != nil {
		ch.cancel()
	}
	ch.mu.Unlock()
	os.Remove(filepath.Join(c.chatsDir(), id+".json"))
	os.RemoveAll(filepath.Join(c.chatsDir(), id+"-images"))
	os.RemoveAll(filepath.Join(c.chatsDir(), id+"-docs"))
	os.RemoveAll(filepath.Join(c.chatsDir(), id+"-runs"))
	return true
}

// removeProjectChats deletes the conversations of a project that is being deleted.
func (c *Controller) removeProjectChats(projectID string) {
	c.chatMu.Lock()
	var ids []string
	for id, ch := range c.chats {
		if ch.ProjectID == projectID {
			ids = append(ids, id)
		}
	}
	c.chatMu.Unlock()
	for _, id := range ids {
		c.removeChat(id)
	}
}

// chatSystemFor is the system prompt of a chat. A plain chat's is exactly chatSystemPrompt. A project
// conversation adds its memory scope and the project's guidance; none of that changes between turns
// unless the owner edits the project, so the prefix stays the same.
func (c *Controller) chatSystemFor(ch *Chat, p *Project) string {
	if p == nil {
		return c.chatSystemPrompt(ch.Instructions)
	}
	return c.chatSystemPromptIn(ch.Instructions, p.ID) + "\n\n" + projectGuidance(p)
}

// projectCtx is what one answer in a project conversation needs besides the chat: it travels in the
// context to answerChatOnce.
type projectCtx struct {
	p     *Project
	state string // tasks and files, put after the newest message
	last  string // the owner's newest message as typed
	reads *projectReads
	ws    *webStats
	webOn bool
	help  string // tool help added to the system message with the tools
}

type projectCtxKey struct{}

func projectCtxFrom(ctx context.Context) *projectCtx {
	pj, _ := ctx.Value(projectCtxKey{}).(*projectCtx)
	return pj
}

func (pj *projectCtx) notes() []string {
	if pj == nil || len(pj.reads.Notes) == 0 {
		return nil
	}
	return append([]string(nil), pj.reads.Notes...)
}

func (pj *projectCtx) memScope() string {
	if pj == nil {
		return ""
	}
	return pj.p.ID
}

func (c *Controller) newProjectCtx(ch *Chat, p *Project) *projectCtx {
	p.mu.Lock()
	repo := p.repoDir()
	p.mu.Unlock()
	pj := &projectCtx{p: p, state: "The project's current tasks and files (for your reference):" + c.projectState(p), reads: newProjectReads(repo), ws: &webStats{}, webOn: c.projectWebOn()}
	pj.help = projectToolsHelp + projectNoWebHelp
	if pj.webOn {
		pj.help = projectToolsHelp + projectWebHelp + projectResearchHelp
	}
	ch.mu.Lock()
	for i := len(ch.Messages) - 1; i >= 0; i-- {
		if ch.Messages[i].Role == "user" {
			pj.last = ch.Messages[i].Text
			break
		}
	}
	ch.mu.Unlock()
	pj.reads.onNote = func(n []string) {
		ch.mu.Lock()
		ch.Reading = n
		ch.mu.Unlock()
		taskLive.changed("chat_" + ch.ID)
	}
	return pj
}

// answerWithProject answers with the project's read tools (and run_code when the chat has it). A
// model that cannot call tools gets the files the owner's message names, and one web search,
// in front of the question instead.
func (c *Controller) answerWithProject(ctx context.Context, infer inferFn, model string, hist []ChatMessage, ctxTok, maxOut int, run *codeRunner, pj *projectCtx) (*InferenceOutput, string, error) {
	pj.reads.web = nil
	if pj.webOn {
		pj.reads.web = func(call toolCall) string {
			return c.runChatTool(ctx, wireCalls([]toolCall{call})[0], chatToolPageCap, pj.ws)
		}
	}
	var note string
	if !smallModel(model) {
		ts := chatToolset{project: pj.reads, help: pj.help}
		if run != nil {
			if tool, ok := run.tool(); ok {
				ts.run, ts.runTool = run, tool
			}
		}
		out, st, err := c.chatWithToolset(ctx, infer, hist, ctxTok, maxOut, ts)
		if !errors.Is(err, errAgentUnsupported) {
			return out, joinNotes(pj.ws.note(), st.note()), err
		}
	}
	h2 := append([]ChatMessage(nil), hist...)
	if pj.webOn {
		h2[0].Content += projectResearchHelp
	} else {
		h2[0].Content += projectNoWebHelp
	}
	last := len(h2) - 1
	if files, names := namedFiles(pj.reads.repo, pj.last, ctxTok-maxOut); files != "" && h2[last].Role == "user" {
		h2[last].Content += "\n\nProject files the owner's last message names:\n" + files
		for _, n := range names {
			pj.reads.note("Read " + n)
		}
	}
	if pj.webOn {
		var st *webStats
		h2, st = c.searchOnce(ctx, h2)
		note = st.note()
	}
	out, err := infer(ctx, InferenceSpec{Messages: fitHistory(h2, (ctxTok-maxOut)*3), Params: InferenceParams{MaxTokens: maxOut}})
	return out, note, err
}

// answerProjectCloud answers a project conversation with the project's cloud planner by the code the
// old project conversation used (answerProjectChat), on a copy of the chat's messages.
func (c *Controller) answerProjectCloud(ctx context.Context, ch *Chat, p *Project) ChatMsg {
	pc := &projectChat{conv: ch, chatID: ch.ID, onReading: func(n []string) {
		ch.mu.Lock()
		ch.Reading = n
		ch.mu.Unlock()
		taskLive.changed("chat_" + ch.ID)
	}}
	ch.mu.Lock()
	for _, m := range ch.Messages {
		pc.Messages = append(pc.Messages, projectMsgFromChat(m))
	}
	ch.mu.Unlock()
	msg := chatFromProjectMsg(c.answerProjectChat(ctx, p, pc))
	msg.Role = "assistant"
	return msg
}

// applyProjectBlocks takes the task and research blocks out of an answer written through the chat
// engine and starts the tasks.
func (c *Controller) applyProjectBlocks(ch *Chat, p *Project, msg *ChatMsg, scope offerScope) {
	pm := c.projectBlocks(p, ch.ID, msg.Text, scope)
	msg.Text, msg.Offer, msg.Tasks, msg.research = pm.Text, pm.Offer, pm.Tasks, pm.research
	if pm.Error != "" {
		msg.Error = pm.Error
	}
}

// projectStashIDs is the chat's own stashes plus the project's.
func (c *Controller) projectStashIDs(own []string, p *Project) []string {
	ids := append([]string(nil), own...)
	if p == nil {
		return ids
	}
	if s, _ := c.projectStash(p, false); s != nil && !slices.Contains(ids, s.ID) {
		ids = append(ids, s.ID)
	}
	return ids
}

// resolveProjectPlanner turns the picker value "planner" into what answers: cloud is true when the
// project's planner is Claude or OpenRouter (or cannot be used, which answerProjectChat reports);
// otherwise pref is the node to answer on.
func (c *Controller) resolveProjectPlanner(p *Project) (pref string, cloud bool) {
	planner, _, _, _, err := c.projectPlanner(p)
	if err != nil || isCloudPlanner(planner) {
		return "", true
	}
	if id := strings.TrimPrefix(planner, "node:"); id != "" {
		return id, false
	}
	return "auto", false
}

// chatInFilter picks the chats of a list: "" and "none" the plain ones, "all" every chat, else
// the conversations of that project. A chat of a project that no longer exists counts as plain.
func (c *Controller) chatInFilter(ch *Chat, filter string) bool {
	pid := ch.ProjectID
	if pid != "" && c.getProject(pid) == nil {
		pid = ""
	}
	switch filter {
	case "", "none":
		return pid == ""
	case "all":
		return true
	}
	return pid == filter
}
