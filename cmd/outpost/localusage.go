package main

// Local usage: how many answers the cluster's own models gave, and how many
// tokens they read and wrote, per day. It sits next to the cloud spending
// ledger (spend.go) so the Spending page can show free local use beside paid
// cloud use. Counts come from every inference job that succeeds (chat, task
// work, review, research lanes), recorded in runInferenceJobLive. Nothing
// about the prompts is kept, only counts. Pruned after localUsageDays days.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const localUsageDays = 400

type localDay struct {
	Calls  int     `json:"calls"`
	In     int     `json:"in"`     // prompt tokens
	Cached int     `json:"cached"` // prompt tokens the server reused instead of reading again
	Out    int     `json:"out"`    // generated tokens
	Secs   float64 `json:"secs"`   // generation time (output tokens / tokens per second), for the average speed
}

type localUsage struct {
	mu   sync.Mutex
	path string
	Days map[string]*localDay `json:"days"` // "2026-11-24"
	last time.Time
}

func (c *Controller) localUsage() *localUsage {
	c.localOnce.Do(func() {
		l := &localUsage{Days: map[string]*localDay{}}
		if c.dataDir != "" {
			l.path = filepath.Join(c.dataDir, "local-usage.json")
			if b, err := os.ReadFile(l.path); err == nil {
				if json.Unmarshal(b, l) != nil || l.Days == nil {
					l.Days = map[string]*localDay{}
				}
			}
		}
		c.local = l
	})
	return c.local
}

// recordLocal adds one finished inference job.
func (c *Controller) recordLocal(out *InferenceOutput) {
	if out == nil || (out.PromptTokens == 0 && out.OutputTokens == 0) {
		return
	}
	now := time.Now()
	l := c.localUsage()
	l.mu.Lock()
	defer l.mu.Unlock()
	key := now.Format("2006-01-02")
	d := l.Days[key]
	if d == nil {
		d = &localDay{}
		l.Days[key] = d
	}
	d.Calls++
	d.In += out.PromptTokens
	d.Cached += out.CachedTokens
	d.Out += out.OutputTokens
	if out.TokensPerSec > 0 {
		d.Secs += float64(out.OutputTokens) / out.TokensPerSec
	}
	// Saving on every call would rewrite the file hundreds of times during a task; once every 20 seconds is enough.
	if l.path != "" && now.Sub(l.last) > 20*time.Second {
		l.last = now
		l.saveLocked(now)
	}
}

func (l *localUsage) saveLocked(now time.Time) {
	cut := now.AddDate(0, 0, -localUsageDays).Format("2006-01-02")
	for k := range l.Days {
		if k < cut {
			delete(l.Days, k)
		}
	}
	b, _ := json.Marshal(l)
	_ = writeFileAtomic(l.path, b, 0o600)
}

// flush writes the ledger (called when the controller stops, and by tests).
func (l *localUsage) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.path != "" {
		l.saveLocked(time.Now())
	}
}

// LocalMonth is what the Spending page shows for the local models.
type LocalMonth struct {
	Calls  int     `json:"calls"`
	In     int     `json:"in"`
	Cached int     `json:"cached"`
	Out    int     `json:"out"`
	AvgTPS float64 `json:"avg_tps"`
	Days   []int   `json:"days"` // generated tokens per day of the month
}

func (l *localUsage) month(month string, daysInMonth int) LocalMonth {
	l.mu.Lock()
	defer l.mu.Unlock()
	m := LocalMonth{Days: make([]int, daysInMonth)}
	var secs float64
	keys := make([]string, 0, len(l.Days))
	for k := range l.Days {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(k) != 10 || k[:7] != month {
			continue
		}
		d := l.Days[k]
		m.Calls += d.Calls
		m.In += d.In
		m.Cached += d.Cached
		m.Out += d.Out
		secs += d.Secs
		if day := int(k[8]-'0')*10 + int(k[9]-'0'); day >= 1 && day <= daysInMonth {
			m.Days[day-1] = d.Out
		}
	}
	if secs > 0 {
		m.AvgTPS = float64(m.Out) / secs
	}
	return m
}
