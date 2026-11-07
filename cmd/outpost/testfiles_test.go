package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Tests live beside the code they cover and are named for it, not for the pull
// request that added them (pr7_test.go told the next reader nothing). New files
// named like that fail here.
func TestTestFilesAreNotNamedAfterPullRequests(t *testing.T) {
	bad := regexp.MustCompile(`^(pr|pull|issue|row|step|phase)[-_]?\d+.*_test\.go$`)
	generic := map[string]bool{"outpost_test.go": true, "misc_test.go": true, "tests_test.go": true, "fieldkit_test.go": true}
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, "_test.go") {
			continue
		}
		n++
		if bad.MatchString(name) || generic[name] {
			t.Errorf("%s: name test files after the feature they test (for example schedules_test.go)", name)
		}
	}
	if n < 50 { // the working directory moved, so the check above saw nothing
		t.Fatalf("only %d test files found in %s", n, "cmd/outpost")
	}
}
