package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLocalUsageMonth(t *testing.T) {
	c := &Controller{dataDir: t.TempDir()}
	c.recordLocal(&InferenceOutput{PromptTokens: 100, CachedTokens: 40, OutputTokens: 50, TokensPerSec: 25})
	c.recordLocal(&InferenceOutput{PromptTokens: 10, OutputTokens: 150, TokensPerSec: 50})
	c.recordLocal(&InferenceOutput{}) // empty answers (failed tool calls, cancelled) are not counted
	c.recordLocal(nil)
	now := time.Now()
	m := c.localUsage().month(now.Format("2006-01"), 31)
	if m.Calls != 2 || m.In != 110 || m.Cached != 40 || m.Out != 200 {
		t.Fatalf("month = %+v", m)
	}
	if m.AvgTPS < 39 || m.AvgTPS > 41 { // 200 tokens in 2 s + 3 s = 40 tokens per second
		t.Fatalf("avg tok/s = %v", m.AvgTPS)
	}
	if m.Days[now.Day()-1] != 200 {
		t.Fatalf("today = %v", m.Days)
	}
	if other := c.localUsage().month("1999-01", 31); other.Calls != 0 {
		t.Fatalf("other month = %+v", other)
	}
	// It survives a restart, and old days are pruned when saving.
	c.localUsage().Days["2000-01-01"] = &localDay{Calls: 1}
	c.localUsage().flush()
	c2 := &Controller{dataDir: c.dataDir}
	if m := c2.localUsage().month(now.Format("2006-01"), 31); m.Calls != 2 {
		t.Fatalf("after reload = %+v", m)
	}
	if _, ok := c2.localUsage().Days["2000-01-01"]; ok {
		t.Fatal("old day kept")
	}
	_ = filepath.Join
}
