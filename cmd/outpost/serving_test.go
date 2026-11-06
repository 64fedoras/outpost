package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestServingArgs(t *testing.T) {
	got := strings.Join(servingArgs(ModelLoad{CacheType: "q8_0", FlashAttn: "on", DraftPath: "/m/d.gguf"}), " ")
	if got != "-ctk q8_0 -ctv q8_0 -fa on -md /m/d.gguf -ngld 99" {
		t.Fatalf("args %q", got)
	}
	if a := servingArgs(ModelLoad{DraftPath: "/d", GPULayers: "0"}); strings.Join(a, " ") != "-md /d" {
		t.Fatalf("cpu draft %v", a)
	}
	if len(servingArgs(ModelLoad{})) != 0 {
		t.Fatal("defaults must add nothing")
	}
	if kvFactor("q4_0") >= kvFactor("q8_0") || kvFactor("") != 1 {
		t.Fatal("kvFactor")
	}
}

func TestCheckServing(t *testing.T) {
	c := &Controller{models: map[string]*ModelRec{
		"big":  {Name: "big", Status: "ready", BytesTotal: 10 << 30},
		"tiny": {Name: "tiny", Status: "ready", BytesTotal: 1 << 30},
		"half": {Name: "half", Status: "ready", BytesTotal: 6 << 30},
		"wait": {Name: "wait", Status: "downloading", BytesTotal: 1 << 20},
	}}
	ct, fa, par := "f16", "auto", 9
	if err := c.checkServing(&ct, &fa, "tiny", "big", &par); err != nil || ct != "" || fa != "" || par != maxSlots {
		t.Fatalf("%v %q %q %d", err, ct, fa, par)
	}
	for name, tc := range map[string]struct {
		ct, fa, draft string
	}{
		"bad cache":       {"q2", "", ""},
		"bad flash":       {"", "yes", ""},
		"q8 without fa":   {"q8_0", "off", ""},
		"same as main":    {"", "", "big"},
		"draft too big":   {"", "", "half"},
		"draft not ready": {"", "", "wait"},
		"draft missing":   {"", "", "nope"},
	} {
		ct, fa, p := tc.ct, tc.fa, 1
		if err := c.checkServing(&ct, &fa, tc.draft, "big", &p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	p := 1
	ct, fa = "q4_0", "on"
	if err := c.checkServing(&ct, &fa, "", "big", &p); err != nil || p != 0 {
		t.Fatalf("%v p=%d", err, p)
	}
}

func TestParallelSlotsReserve(t *testing.T) {
	c := &Controller{nodes: map[string]*NodeRec{"n": {ID: "n", LoadPrefs: &ModelLoad{Name: "m", Parallel: 3}}},
		lastHB: map[string]*Heartbeat{"n": {LoadedModel: "m"}}, reserved: map[string]int{}}
	for i := 0; i < 3; i++ {
		if !c.tryReserve("n") {
			t.Fatalf("slot %d refused", i+1)
		}
	}
	if c.tryReserve("n") || !c.isReserved("n") {
		t.Fatal("a fourth call must wait")
	}
	c.releaseNode("n")
	if c.isReserved("n") || !c.tryReserve("n") {
		t.Fatal("a freed slot must be usable")
	}
	// Another model loaded than the one with slots: one call at a time.
	c.lastHB["n"].LoadedModel = "other"
	if !c.isReserved("n") {
		t.Fatal("slots must follow the loaded model")
	}
	c.releaseNode("n")
	c.releaseNode("n")
	c.releaseNode("n")
	if c.reserved["n"] != 0 {
		t.Fatalf("reserved %d", c.reserved["n"])
	}
	// Benchmarks need the whole node.
	c.lastHB["n"].LoadedModel = "m"
	c.reserved["n"] = 1
	if c.benchBlockedLocked("n") == "" {
		t.Fatal("benchmark allowed on a node with a running call")
	}
}

func TestParseProgress(t *testing.T) {
	read, total, left, ok := parseProgress("100 1000 0 1000\n400 1000 0 4000\n")
	if !ok || read != 400 || total != 1000 || left != 6 {
		t.Fatalf("%d %d %d %v", read, total, left, ok)
	}
	// Cached tokens are free: only the rest sets the speed.
	if _, _, left, _ := parseProgress("600 1000 500 1000"); left != 4 {
		t.Fatalf("left %d", left)
	}
	if _, _, _, ok := parseProgress("junk"); ok {
		t.Fatal("junk accepted")
	}
	h := newLiveHub()
	lh := h.begin("task", "t1", "x", "node", false)
	lh.bindJob("job1")
	snap, ch, cancel := h.subscribe("task")
	defer cancel()
	if len(snap) != 1 {
		t.Fatal("snapshot")
	}
	h.jobOutput(JobOutput{JobID: "job1", Stream: "progress", Data: "250 1000 0 500\n"})
	ev := <-ch
	if ev.T != "progress" || ev.Read != 250 || ev.Total != 1000 || ev.Left != 1 {
		t.Fatalf("event %+v", ev)
	}
	h.jobOutput(JobOutput{JobID: "job1", Stream: "tokens", Data: "hi", Tokens: 1})
	if ev := <-ch; ev.T != "delta" || ev.Text != "hi" {
		t.Fatalf("delta %+v", ev)
	}
}

// A fake llama-server that reports prompt progress, and refuses a JSON schema.
func TestLlamaChatProgressAndSchemaFallback(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		if b["response_format"] != nil {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"json_schema not supported"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(s string) { fmt.Fprintf(w, "data: %s\n\n", s); w.(http.Flusher).Flush() }
		if b["return_progress"] == true {
			send(`{"choices":[],"prompt_progress":{"total":800,"cache":0,"processed":400,"time_ms":200}}`)
			send(`{"choices":[],"prompt_progress":{"total":800,"cache":0,"processed":800,"time_ms":400}}`)
		}
		send(`{"choices":[{"delta":{"content":"{}"}}]}`)
		send(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":800,"completion_tokens":2}}`)
		send(`[DONE]`)
	}))
	defer srv.Close()
	var port int
	fmt.Sscanf(srv.URL, "http://127.0.0.1:%d", &port)
	l := newLlamaMgr(nil)
	l.cmd, l.ready, l.port, l.model = &exec.Cmd{}, true, port, "m"
	var progs []promptProgress
	spec := InferenceSpec{Messages: []ChatMessage{{Role: "user", Content: "plan"}}, Params: InferenceParams{JSONSchema: planSchema}}
	out, e := l.chatP(context.Background(), spec, func(bool, string) {}, func(p promptProgress) { progs = append(progs, p) })
	if e != nil {
		t.Fatal(e)
	}
	if out.Text != "{}" || len(bodies) != 2 || bodies[1]["response_format"] != nil {
		t.Fatalf("schema fallback: text %q, %d requests", out.Text, len(bodies))
	}
	if bodies[0]["response_format"].(map[string]any)["type"] != "json_schema" {
		t.Fatalf("first request %v", bodies[0]["response_format"])
	}
	if len(progs) != 2 || progs[1].Processed != 800 {
		t.Fatalf("progress %+v", progs)
	}
	// Without a callback nothing asks for progress.
	bodies = nil
	if _, e := l.chat(context.Background(), InferenceSpec{Messages: []ChatMessage{{Role: "user", Content: "x"}}}, func(string) {}); e != nil {
		t.Fatal(e)
	}
	if bodies[0]["return_progress"] != nil {
		t.Fatal("progress asked for without a callback")
	}
}
