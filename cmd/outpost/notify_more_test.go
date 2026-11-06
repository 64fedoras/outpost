package main

import (
	"strings"
	"testing"
	"time"
)

func TestDigestOffUntilTicked(t *testing.T) {
	var ns NotifySettings
	if ns.eventOn("digest") || !ns.eventOn("model_failed") {
		t.Fatal("digest must default off, the rest on")
	}
	ns.Events = map[string]bool{"digest": true}
	if !ns.eventOn("digest") {
		t.Fatal("ticked digest is off")
	}
	if (NotifySettings{}).digestHour() != 8 || (NotifySettings{DigestHour: 24}).digestHour() != 0 || (NotifySettings{DigestHour: 7}).digestHour() != 7 {
		t.Fatal("digest hour mapping")
	}
}

func TestNextDigest(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 11, 5, 7, 30, 0, 0, loc)
	if got := nextDigest(now, 8); !got.Equal(time.Date(2026, 11, 5, 8, 0, 0, 0, loc)) {
		t.Fatalf("same day: %v", got)
	}
	if got := nextDigest(now, 7); !got.Equal(time.Date(2026, 11, 6, 7, 0, 0, 0, loc)) {
		t.Fatalf("next day: %v", got)
	}
}

func TestDigestText(t *testing.T) {
	now := time.Now()
	old, recent := now.Add(-48*time.Hour), now.Add(-2*time.Hour)
	c := &Controller{
		tasks: map[string]*Task{
			"a": {ID: "a", Title: "Dice roller", Status: "done", Ended: &recent},
			"b": {ID: "b", Title: "Old thing", Status: "done", Ended: &old},
			"c": {ID: "c", Title: "Parser", Status: "failed", Ended: &recent},
			"d": {ID: "d", Title: "Needs answers", Status: "awaiting_answers", Created: recent},
		},
		nodes: map[string]*NodeRec{"n1": {ID: "n1", Name: "gpu-box"}, "n2": {ID: "n2", Name: "cpu"}},
		conns: map[string]*nodeConn{"n2": {}},
	}
	txt := c.digestText(now.Add(-24 * time.Hour))
	for _, want := range []string{"Finished (1): Dice roller", "Failed (1): Parser", "Waiting for you (1): Needs answers", "Offline nodes: gpu-box"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("missing %q in %q", want, txt)
		}
	}
	if strings.Contains(txt, "Old thing") {
		t.Fatal("old task listed")
	}
	quiet := &Controller{tasks: map[string]*Task{"b": {ID: "b", Status: "done", Ended: &old}}}
	if quiet.digestText(now.Add(-24*time.Hour)) != "" {
		t.Fatal("a quiet day must send nothing")
	}
}

func TestModelFailureNotifiesOnce(t *testing.T) {
	hook, hits := receiver(t)
	c := &Controller{dataDir: t.TempDir(), nodeOps: map[string]*nodeOpView{}, nodes: map[string]*NodeRec{"n1": {ID: "n1", Name: "gpu-box"}}}
	c.settings.Notify = NotifySettings{WebhookURL: hook.URL}
	st := OpStatus{Op: "model.load", Target: "qwen", State: "failed", Error: "out of memory"}
	c.setNodeOp("n1", st)
	c.setNodeOp("n1", st) // the same failure reported again
	c.setNodeOp("n1", OpStatus{Op: "model.unload", Target: "qwen", State: "failed"})
	hits(1)
	time.Sleep(150 * time.Millisecond)
	h := hits(2)
	if len(h) != 1 || h[0].body["event"] != "model_failed" || !strings.Contains(h[0].body["message"].(string), "gpu-box") {
		t.Fatalf("hits: %+v", h)
	}
}
