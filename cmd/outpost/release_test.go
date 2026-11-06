package main

import "testing"

func relNode(free uint64, models ...string) *NodeRec {
	h := &Hello{ModelsFreeBytes: free}
	for _, m := range models {
		h.Models = append(h.Models, ModelInfo{Name: m})
	}
	return &NodeRec{Hello: h}
}

func TestPickReleaseNode(t *testing.T) {
	m := &ModelRec{Name: "m", BytesTotal: 1000}
	c := &Controller{
		nodes: map[string]*NodeRec{"small": relNode(500), "big": relNode(5000), "bigger": relNode(9000),
			"busy": relNode(99999), "off": relNode(99999)},
		conns:     map[string]*nodeConn{"small": {}, "big": {}, "bigger": {}, "busy": {}},
		pipelines: map[string]*pipeline{"busy": {}},
	}
	id, push, err := c.pickReleaseNode(m, "")
	if err != nil || !push || id != "bigger" {
		t.Fatalf("got %q push=%v err=%v", id, push, err)
	}
	if id, _, _ := c.pickReleaseNode(m, "big"); id != "big" {
		t.Fatalf("requested node ignored: %q", id)
	}
	if _, _, err := c.pickReleaseNode(m, "small"); err == nil {
		t.Fatal("a node without room must be refused")
	}
	// an online node that already holds it needs no copy, even when busy
	c.nodes["small"] = relNode(0, "m")
	if id, push, err := c.pickReleaseNode(m, ""); err != nil || push || id != "small" {
		t.Fatalf("holder: %q push=%v err=%v", id, push, err)
	}
}
