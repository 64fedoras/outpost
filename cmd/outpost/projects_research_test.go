package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSplitResearchBlocks(t *testing.T) {
	rest, q := splitResearchBlocks("Looking into it.\n[start research]\nWhat is Hermes Agent and how does it work?\n[/start research]\n[start research]second[/start research]")
	if rest != "Looking into it." || q != "What is Hermes Agent and how does it work?" {
		t.Fatalf("%q %q", rest, q)
	}
	if rest, q := splitResearchBlocks("plain"); rest != "plain" || q != "" {
		t.Fatalf("%q %q", rest, q)
	}
}

func TestProjectChatStartsResearch(t *testing.T) {
	reply := fmt.Sprintf(claudeText, `I will research it.\n[start research]\nHow does Hermes Agent work?\n[/start research]`)
	c, p, pc, bodies := projectWebRig(t, true, reply)
	msg := c.answerProjectChat(context.Background(), p, pc)
	if msg.research != "How does Hermes Agent work?" || msg.Text != "I will research it." || msg.Error != "" {
		t.Fatalf("msg %+v", msg)
	}
	if s := systemText(bodies()[0]); !strings.Contains(s, "[start research]") {
		t.Fatalf("system prompt lacks research: %s", s)
	}
	// With web search off the block is refused with a note.
	c2, p2, pc2, bodies2 := projectWebRig(t, false, reply)
	msg = c2.answerProjectChat(context.Background(), p2, pc2)
	if msg.research != "" || msg.Offer == nil || msg.Offer.Key != "web_search" {
		t.Fatalf("web off: %+v", msg)
	}
	if strings.Contains(systemText(bodies2()[0]), "[start research]") {
		t.Fatal("research offered with web search off")
	}
}

func TestProjectResearchOneAtATime(t *testing.T) {
	c, p, pc, _ := projectWebRig(t, true, fmt.Sprintf(claudeText, "x"))
	pc.mu.Lock()
	pc.resCancel = func() {}
	pc.mu.Unlock()
	if err := c.startProjectResearch(p, pc, "q"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second run: %v", err)
	}
}

func TestProjectResearchSavedToStash(t *testing.T) {
	c, p, _, _ := projectWebRig(t, true, fmt.Sprintf(claudeText, "x"))
	name, err := c.saveResearchToStash(p, "How does Hermes / Agent work?", "Short answer [1].\n\nSources\n[1] https://example.org")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "Research - How does Hermes Agent work") || !strings.HasSuffix(name, ".md") {
		t.Fatalf("name %q", name)
	}
	s, _ := c.projectStash(p, false)
	for deadline := time.Now().Add(5 * time.Second); s != nil; time.Sleep(10 * time.Millisecond) {
		s.mu.Lock()
		busy := s.working
		s.mu.Unlock()
		if !busy || time.Now().After(deadline) {
			break
		}
	}
	if s == nil || len(s.Resources) != 1 || s.Resources[0].Name != name {
		t.Fatalf("stash %+v", s)
	}
	b, _ := os.ReadFile(filepath.Join(s.dir, "files", s.Resources[0].File))
	if !strings.Contains(string(b), "# Research: How does Hermes / Agent work?") || !strings.Contains(string(b), "https://example.org") {
		t.Fatalf("file %s", b)
	}
}

func TestProjectResearchStaleAfterRestart(t *testing.T) {
	dir := t.TempDir()
	msgs := map[string]any{"messages": []ProjectChatMsg{{ID: "m1", Role: "assistant", Question: "q",
		Research: &ResearchRun{Stage: "lanes", Lanes: []ResearchLane{{Q: "a", Status: "working"}, {Q: "b", Status: "done"}}}}}}
	b, _ := json.Marshal(msgs)
	os.WriteFile(filepath.Join(dir, "chat.json"), b, 0o600)
	p := &Project{ID: "proj_r", dir: dir}
	pc := p.chatLog()
	defer projectChats.Delete(pc.path)
	m := pc.view().Messages[0]
	if m.Research.Stage != "stopped" || m.Research.Lanes[0].Status != "stopped" || m.Research.Lanes[1].Status != "done" || !strings.Contains(m.Error, "restarted") {
		t.Fatalf("%+v %+v", m, m.Research)
	}
	// The transcript names the report, not the lanes.
	tr := projectChatTranscript([]ProjectChatMsg{{Role: "assistant", Text: "Report text", Question: "q", Research: &ResearchRun{Stage: "done"}, Saved: "Research - q.md"}}, 10000)
	if !strings.Contains(tr, "Research report: (question: q)") || !strings.Contains(tr, "Stash as Research - q.md") {
		t.Fatalf("transcript %s", tr)
	}
}
