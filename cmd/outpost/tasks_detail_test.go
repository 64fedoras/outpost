package main

import (
	"encoding/json"
	"testing"
)

func TestMergeJSONObjects(t *testing.T) {
	for _, c := range []struct{ a, b, want string }{
		{`{"id":"t1","x":[1]}`, `{"files":[],"n":2}`, `{"id":"t1","x":[1],"files":[],"n":2}`},
		{`{"id":"t1"}`, `{}`, `{"id":"t1"}`},
		{`{}`, `{"n":2}`, `{"n":2}`},
		{" {\"a\":1}\n", "{\"b\":2}\n", `{"a":1,"b":2}`},
	} {
		got := string(mergeJSONObjects([]byte(c.a), []byte(c.b)))
		if got != c.want {
			t.Errorf("%s + %s = %s", c.a, c.b, got)
		}
		var v map[string]any
		if json.Unmarshal([]byte(got), &v) != nil {
			t.Errorf("%s is not valid JSON", got)
		}
	}
	// The extra part of the task detail has no Task fields of its own.
	b, _ := json.Marshal(taskDetail{Files: []repoFile{}})
	var v map[string]any
	_ = json.Unmarshal(b, &v)
	for k := range v {
		switch k {
		case "files", "planner_label", "claude_key_set", "workers_online", "exec_nodes":
		default:
			t.Errorf("unexpected key %q in the task detail extras", k)
		}
	}
}
