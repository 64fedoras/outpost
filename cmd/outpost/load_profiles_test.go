package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoadProfiles(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), nodes: map[string]*NodeRec{"n1": {ID: "n1"}}}
	save := func(name string) int {
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"`+name+`","model":"m","ctx":32768,"cpu_moe":"all","extra_args":"--foo bar"}`))
		r.SetPathValue("id", "n1")
		w := httptest.NewRecorder()
		c.handleSaveLoadProfile(w, r)
		return w.Code
	}
	if save("b") != 200 || save("a") != 200 || save("B") != 200 {
		t.Fatal("save failed")
	}
	ps := c.nodes["n1"].Profiles
	if len(ps) != 2 || ps[0].Name != "a" || ps[1].Name != "B" || ps[1].Load.CPUMoe != "all" || len(ps[1].Load.ExtraArgs) != 2 {
		t.Fatalf("profiles: %+v", ps)
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"x","model":"m","cpu_moe":"banana"}`))
	r.SetPathValue("id", "n1")
	w := httptest.NewRecorder()
	c.handleSaveLoadProfile(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatal("bad cpu_moe accepted")
	}
	r = httptest.NewRequest("DELETE", "/", nil)
	r.SetPathValue("id", "n1")
	r.SetPathValue("name", "A")
	c.handleDeleteLoadProfile(httptest.NewRecorder(), r)
	if len(c.nodes["n1"].Profiles) != 1 {
		t.Fatal("delete failed")
	}
}
