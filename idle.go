package main

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// Idle unload: a node stops llama-server after it has served no request for a
// while, to give the RAM or VRAM back. The model stays "cold": the next request
// loads it again. The wait is typed by the owner as text ("30s", "5m", "1hr",
// "2d", "0" for never) and is set for all nodes, with an optional override per node.

const (
	defaultIdleUnload = time.Hour
	minIdleUnload     = 10 * time.Second
	maxIdleUnload     = 365 * 24 * time.Hour
)

var idleRe = regexp.MustCompile(`^(\d+(?:\.\d+)?|\.\d+)\s*([a-z]*)$`)

// parseIdleDuration reads text such as 30s, 5m, 1hr, 1.5h or 2d. "0" and the
// words never, off and none mean no unloading (returned as 0). A number needs a
// unit, because a bare "30" could mean seconds or minutes.
func parseIdleDuration(s string) (time.Duration, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	switch t {
	case "0", "never", "off", "none", "no":
		return 0, nil
	}
	m := idleRe.FindStringSubmatch(t)
	if m == nil {
		return 0, fmt.Errorf("%q is not a time; use something like 30s, 5m, 1hr or 2d, or 0 for never", s)
	}
	var unit time.Duration
	switch m[2] {
	case "":
		return 0, fmt.Errorf("%q needs a unit: s, m, hr or d (or 0 for never)", s)
	case "s", "sec", "secs", "second", "seconds":
		unit = time.Second
	case "m", "min", "mins", "minute", "minutes":
		unit = time.Minute
	case "h", "hr", "hrs", "hour", "hours":
		unit = time.Hour
	case "d", "day", "days":
		unit = 24 * time.Hour
	default:
		return 0, fmt.Errorf("unknown unit %q in %q; use s, m, hr or d", m[2], s)
	}
	var v float64
	if _, err := fmt.Sscanf(m[1], "%g", &v); err != nil {
		return 0, fmt.Errorf("%q is not a time", s)
	}
	if v == 0 {
		return 0, nil
	}
	if v*float64(unit) > float64(maxIdleUnload) {
		return 0, fmt.Errorf("%q is too long; the most is 365d (or 0 for never)", s)
	}
	d := time.Duration(math.Round(v * float64(unit)))
	if d < minIdleUnload {
		return 0, fmt.Errorf("%q is too short; the least is 10s (or 0 for never)", s)
	}
	return d, nil
}

// formatIdleDuration is the reverse, for showing the parsed value back: 90m -> "1h 30m".
func formatIdleDuration(d time.Duration) string {
	if d <= 0 {
		return "never"
	}
	var parts []string
	for _, u := range []struct {
		n time.Duration
		s string
	}{{24 * time.Hour, "d"}, {time.Hour, "h"}, {time.Minute, "m"}, {time.Second, "s"}} {
		if d >= u.n {
			parts = append(parts, fmt.Sprintf("%d%s", d/u.n, u.s))
			d %= u.n
		}
	}
	return strings.Join(parts, " ")
}

// idleUnloadFor is the wait that applies to a node: its own text if set,
// otherwise the global text, otherwise one hour. Text that no longer parses
// counts as unset.
func idleUnloadFor(global, node string) time.Duration {
	for _, s := range []string{node, global} {
		if strings.TrimSpace(s) == "" {
			continue
		}
		if d, err := parseIdleDuration(s); err == nil {
			return d
		}
	}
	return defaultIdleUnload
}
