package main

import (
	"strings"
	"testing"
)

func TestCPUMoeArgs(t *testing.T) {
	join := func(ml ModelLoad) string { return strings.Join(servingArgs(ml), " ") }
	if got := join(ModelLoad{CPUMoe: "all"}); got != "--cpu-moe" {
		t.Errorf("all: %q", got)
	}
	if got := join(ModelLoad{CPUMoe: "20"}); got != "--n-cpu-moe 20" {
		t.Errorf("n: %q", got)
	}
	if got := join(ModelLoad{CPUMoe: "all", ExtraArgs: []string{"--n-cpu-moe", "5"}}); got != "" {
		t.Errorf("extra args win: %q", got)
	}
	for v, ok := range map[string]bool{"": true, "all": true, "1": true, "999": true, "0": false, "1000": false, "-3": false, "some": false} {
		if validCPUMoe(v) != ok {
			t.Errorf("validCPUMoe(%q) != %v", v, ok)
		}
	}
}
