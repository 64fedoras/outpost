package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSpendLedgerView(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), tasks: map[string]*Task{}}
	l := c.spendLedger()
	nov := time.Date(2026, 11, 9, 12, 0, 0, 0, time.Local)
	oct := time.Date(2026, 10, 31, 23, 0, 0, 0, time.Local)
	l.mu.Lock()
	l.addLocked(nov, "claude", "a", "Port scanner", "Claude Sonnet 5.5", 2, 1000, 200, 1.25)
	l.addLocked(nov.AddDate(0, 0, 1), "openrouter", "b", "Recipe API", "OpenRouter · x", 1, 500, 50, 0.5)
	l.addLocked(nov, "claude", "a", "Port scanner", "Claude Sonnet 5.5", 1, 10, 2, 0.25)
	l.addLocked(oct, "claude", "c", "Old", "Claude Sonnet 5.5", 1, 1, 1, 3)
	_ = l.saveLocked()
	l.mu.Unlock()

	if got := l.monthTotal(nov); got != 2 {
		t.Fatalf("November total %v, want 2", got)
	}
	v := c.spendViewFor("2026-11", nov)
	if len(v.Days) != 30 || v.Days[8].Claude != 1.5 || v.Days[9].OpenRouter != 0.5 {
		t.Fatalf("days %+v", v.Days[8:10])
	}
	if v.Total != 2 || v.Tasks != 2 || v.Calls != 4 || v.Claude != 1.5 || v.OpenRouter != 0.5 {
		t.Fatalf("totals %+v", v)
	}
	if len(v.Top) != 2 || v.Top[0].ID != "a" || v.Top[0].USD != 1.5 || v.Top[0].Calls != 3 || v.Top[0].In != 1010 {
		t.Fatalf("top %+v", v.Top)
	}
	if len(v.Months) != 2 || v.Months[0] != "2026-11" || v.Months[1] != "2026-10" || !v.Current {
		t.Fatalf("months %v current %v", v.Months, v.Current)
	}
	if o := c.spendViewFor("2026-10", nov); o.Total != 3 || len(o.Days) != 31 || o.Current {
		t.Fatalf("October %+v", o)
	}

	// Rename and delete keep the cost but update the row.
	c.spendRename("a", "Port scanner in Go")
	c.spendDeleted("b")
	v = c.spendViewFor("2026-11", nov)
	if v.Top[0].Title != "Port scanner in Go" || !v.Top[1].Deleted || v.Total != 2 {
		t.Fatalf("after rename/delete %+v", v.Top)
	}

	// It survives a restart.
	c2 := &Controller{dataDir: c.dataDir, tasks: map[string]*Task{}}
	if got := c2.spendLedger().monthTotal(nov); got != 2 {
		t.Fatalf("reloaded total %v", got)
	}
}

func TestSpendSeedsFromTasks(t *testing.T) {
	created := time.Now().Add(-time.Hour)
	c := &Controller{dataDir: t.TempDir(), tasks: map[string]*Task{
		"x": {ID: "x", Title: "Weather", Planner: "claude", ClaudeModel: "claude-sonnet-5-5", CostUSD: 0.9, ClaudeCalls: 4, Created: created},
		"y": {ID: "y", Prompt: "a local one", Planner: "node:n1", Created: created},
		"z": {ID: "z", Prompt: "Recipe API\nmore", Planner: "openrouter", ORModel: "deepseek/x", CostUSD: 0.1, Created: created},
	}}
	v := c.spendViewFor("", time.Now())
	if v.Tasks != 2 || v.Claude != 0.9 || v.OpenRouter != 0.1 {
		t.Fatalf("seeded %+v", v)
	}
	if v.Top[1].Title != "Recipe API" {
		t.Fatalf("untitled task should use its first prompt line, got %q", v.Top[1].Title)
	}
	if _, err := os.Stat(filepath.Join(c.dataDir, "spend.json")); err != nil {
		t.Fatal("seeded ledger not saved:", err)
	}
}

func TestUntilNextMonth(t *testing.T) {
	now := time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC)
	if d := untilNextMonth(now); d != time.Hour {
		t.Fatalf("got %v", d)
	}
}

// A cloud call that would pass the monthly cap pauses the task with cap_kind
// "monthly"; raising the cap on the Spending page lets it continue, and the call
// is added to the ledger.
func TestMonthlyCapPausesCloudCalls(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1000,"output_tokens":100}}`))
	}))
	defer srv.Close()
	t.Setenv("OUTPOST_ANTHROPIC_URL", srv.URL)

	c := &Controller{dataDir: t.TempDir(), tasks: map[string]*Task{}}
	enc, err := c.encrypt("k")
	if err != nil {
		t.Fatal(err)
	}
	c.settings = defaultSettings()
	c.settings.AnthropicKeyEnc = enc
	c.settings.MonthlyCapUSD = 1
	l := c.spendLedger()
	l.mu.Lock()
	l.addLocked(time.Now(), "claude", "other", "Earlier", "Claude", 1, 1, 1, 1) // the month is already at the cap
	l.mu.Unlock()

	tk := &Task{ID: "t1", dir: t.TempDir(), Planner: "claude", ClaudeModel: "claude-sonnet-5-5", CapUSD: 10, Status: "planning", Title: "demo", resume: make(chan struct{}, 1)}
	c.tasks[tk.ID] = tk
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.plannerCall(ctx, tk, llmRequest{System: "s", User: "u", MaxTokens: 100, Label: "Plan"})
		done <- err
	}()
	waitFor(t, func() bool {
		tk.mu.Lock()
		defer tk.mu.Unlock()
		return tk.Status == "paused_cap" && tk.CapKind == "monthly"
	})
	if calls != 0 {
		t.Fatal("the call ran despite the monthly cap")
	}
	if v := c.spendViewFor("", time.Now()); v.Paused != 1 {
		t.Fatalf("paused count %d", v.Paused)
	}

	rr := httptest.NewRecorder()
	c.handleSpendingCap(rr, httptest.NewRequest("POST", "/api/spending/cap", bytes.NewReader([]byte(`{"cap_usd":50}`))))
	if rr.Code != 200 {
		t.Fatalf("cap save %d %s", rr.Code, rr.Body)
	}
	var view spendView
	_ = json.Unmarshal(rr.Body.Bytes(), &view)
	if view.CapUSD != 50 {
		t.Fatalf("view cap %v", view.CapUSD)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("task did not continue after the cap was raised")
	}
	tk.mu.Lock()
	status, kind := tk.Status, tk.CapKind
	tk.mu.Unlock()
	if status != "planning" || kind != "" || calls != 1 {
		t.Fatalf("status %q kind %q calls %d", status, kind, calls)
	}
	v := c.spendViewFor("", time.Now())
	if v.Calls != 2 || v.Total <= 1 {
		t.Fatalf("call not recorded: %+v", v)
	}
	for _, tt := range v.Top {
		if tt.ID == "t1" && tt.Calls == 1 && tt.Title == "demo" {
			return
		}
	}
	t.Fatalf("task row missing: %+v", v.Top)
}

func TestSpendingCapRejectsBadInput(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), tasks: map[string]*Task{}}
	for _, body := range []string{`{}`, `{"cap_usd":-1}`, `nope`} {
		rr := httptest.NewRecorder()
		c.handleSpendingCap(rr, httptest.NewRequest("POST", "/api/spending/cap", bytes.NewReader([]byte(body))))
		if rr.Code != 400 {
			t.Errorf("%s: code %d", body, rr.Code)
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
