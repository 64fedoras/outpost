package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCompareTwoNodes(t *testing.T) {
	c := chatController(t)
	c.lastHB["m"] = &Heartbeat{LoadedModel: "other-model", InferenceRdy: true}
	c.nodes["m"] = &NodeRec{ID: "m", Name: "big-ram", LoadPrefs: &ModelLoad{Name: "other-model", Ctx: 4096}}
	var specs []InferenceSpec
	var mu sync.Mutex
	fakeInferNode(t, c, "n", &specs, &mu)
	fakeInferNode(t, c, "m", &specs, &mu)
	m := http.NewServeMux()
	m.HandleFunc("POST /api/compare", c.handleCompareStart)
	m.HandleFunc("GET /api/compare/{id}", c.handleCompareGet)
	m.HandleFunc("DELETE /api/compare/{id}", c.handleCompareStop)
	c.compareRoutes(http.NewServeMux()) // the real routes register without clashing
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	for _, bad := range []string{`{"prompt":"","nodes":["n","m"]}`, `{"prompt":"hi","nodes":["n","n"]}`, `{"prompt":"hi","nodes":["n","ghost"]}`} {
		if rec := do("POST", "/api/compare", bad); rec.Code != 400 {
			t.Fatalf("%s -> %d", bad, rec.Code)
		}
	}
	rec := do("POST", "/api/compare", `{"prompt":"Say hi","nodes":["n","m"]}`)
	if rec.Code != 200 {
		t.Fatalf("start %d %s", rec.Code, rec.Body)
	}
	var id struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &id)
	var cmp Compare
	for i := 0; i < 300; i++ {
		json.Unmarshal(do("GET", "/api/compare/"+id.ID, "").Body.Bytes(), &cmp)
		if cmp.Done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cmp.Done {
		t.Fatalf("not done: %+v", cmp)
	}
	a, b := cmp.Sides[0], cmp.Sides[1]
	if a.Status != "done" || a.Text != "echo: Say hi" || a.Node != "gpu-box" || a.TPS != 12 || a.Tokens != 3 {
		t.Fatalf("side a = %+v", a)
	}
	if b.Status != "done" || b.Node != "big-ram" || b.Text != "echo: Say hi" {
		t.Fatalf("side b = %+v", b)
	}
	if c.reserved["n"] != 0 || c.reserved["m"] != 0 {
		t.Fatalf("slots not released: %v", c.reserved)
	}
	if rec := do("GET", "/api/compare/nope", ""); rec.Code != 404 {
		t.Fatalf("missing -> %d", rec.Code)
	}
}
