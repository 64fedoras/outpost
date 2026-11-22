package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCadParams(t *testing.T) {
	code := `// a stand
width = 70; // [20:200] base width
angle = 65;   // [30:5:85]
slot = 12.5;
name = "stand";
hole = true; // screw hole
$fn = 64;
module m() {
  inner = 3;
}
depth = width / 2;
height = -4e1;
`
	ps := cadParams(code)
	var names []string
	for _, p := range ps {
		names = append(names, p.Name)
	}
	if want := []string{"width", "angle", "slot", "hole", "height"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("params %v, want %v", names, want)
	}
	if ps[0].Min == nil || *ps[0].Min != 20 || *ps[0].Max != 200 || ps[0].Comment != "base width" {
		t.Errorf("width range/comment wrong: %+v", ps[0])
	}
	if ps[1].Step == nil || *ps[1].Step != 5 || *ps[1].Max != 85 {
		t.Errorf("angle step wrong: %+v", ps[1])
	}
	if !ps[3].Bool || ps[3].Comment != "screw hole" {
		t.Errorf("hole should be a bool: %+v", ps[3])
	}
}

func TestCadOverrides(t *testing.T) {
	ps := cadParams("w = 10;\non = false;\n")
	got, err := cadOverrides(ps, map[string]string{"w": "12.5", "on": "true"})
	if err != nil || got["w"] != "12.5" || got["on"] != "true" {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, _ := cadOverrides(ps, map[string]string{"w": "10"}); len(got) != 0 {
		t.Errorf("an unchanged value is not an override: %v", got)
	}
	for _, bad := range []map[string]string{{"w": "1; echo(1)"}, {"w": "x"}, {"on": "1"}, {"nope": "1"}} {
		if _, err := cadOverrides(ps, bad); err == nil {
			t.Errorf("%v should be refused", bad)
		}
	}
}

func TestCadExtract(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Here it is.\n```openscad\ncube(10);\n```\n", "cube(10);"},
		{"```python\nprint(1)\n```\n```scad\nsphere(5);\n```", "sphere(5);"},
		{"```\ncube([1,2,3]);\n```", "cube([1,2,3]);"},
		{"cube([10, 20, 30]); // plain reply", "cube([10, 20, 30]); // plain reply"},
		{"OK", ""},
	} {
		got, ok := cadExtract(tc.in)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("cadExtract(%q) = %q, %v; want %q", tc.in, got, ok, tc.want)
		}
	}
}

// binarySTL builds a binary STL of an axis-aligned box from 0 to (x, y, z), two triangles per face.
func binarySTL(x, y, z float32) []byte {
	v := [8][3]float32{{0, 0, 0}, {x, 0, 0}, {x, y, 0}, {0, y, 0}, {0, 0, z}, {x, 0, z}, {x, y, z}, {0, y, z}}
	faces := [][3]int{{0, 2, 1}, {0, 3, 2}, {4, 5, 6}, {4, 6, 7}, {0, 1, 5}, {0, 5, 4}, {1, 2, 6}, {1, 6, 5}, {2, 3, 7}, {2, 7, 6}, {3, 0, 4}, {3, 4, 7}}
	var b bytes.Buffer
	b.Write(make([]byte, 80))
	binary.Write(&b, binary.LittleEndian, uint32(len(faces)))
	for _, f := range faces {
		binary.Write(&b, binary.LittleEndian, [3]float32{})
		for _, i := range f {
			binary.Write(&b, binary.LittleEndian, v[i])
		}
		b.Write([]byte{0, 0})
	}
	return b.Bytes()
}

func TestSTLStats(t *testing.T) {
	size, tris, err := stlStats(binarySTL(70, 80, 8))
	if err != nil || tris != 12 || size != [3]float64{70, 80, 8} {
		t.Fatalf("binary: %v %d %v", size, tris, err)
	}
	ascii := "solid x\nfacet normal 0 0 1\nouter loop\nvertex 0 0 0\nvertex 10 0 0\nvertex 0 5 2\nendloop\nendfacet\nendsolid x\n"
	size, tris, err = stlStats([]byte(ascii))
	if err != nil || tris != 1 || size != [3]float64{10, 5, 2} {
		t.Fatalf("ascii: %v %d %v", size, tris, err)
	}
	if _, _, err := stlStats([]byte("solid empty\nendsolid empty\n")); err == nil {
		t.Error("an empty STL must be an error")
	}
	if _, _, err := stlStats(nil); err == nil {
		t.Error("no bytes must be an error")
	}
}

func TestCadRenderArgv(t *testing.T) {
	got := cadRenderArgv("/usr/bin/openscad", map[string]string{"w": "12", "a": "true"}, true)
	want := []string{"/usr/bin/openscad", "-o", "model.stl", "--export-format", "binstl", "-D", "a=true", "-D", "w=12", "model.scad"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	if got := cadRenderArgv("openscad", nil, false); len(got) != 4 {
		t.Fatalf("old OpenSCAD argv: %v", got)
	}
	if tl := toolsInCommand("openscad -o a.stl a.scad"); !reflect.DeepEqual(tl, []string{"openscad"}) {
		t.Errorf("toolsInCommand: %v", tl)
	}
}

// cadTest makes a controller whose model and renderer are scripted.
type cadTest struct {
	c       *Controller
	mu      sync.Mutex
	replies []string
	seen    [][]ChatMessage
	renders []map[string]string
}

func newCadTest(t *testing.T, replies ...string) *cadTest {
	ct := &cadTest{c: &Controller{dataDir: t.TempDir()}, replies: replies}
	ct.c.loadGens()
	ct.c.loadCad()
	ct.c.cad.llmStub = func(ctx context.Context, rec *CadRec, msgs []ChatMessage) (string, error) {
		ct.mu.Lock()
		defer ct.mu.Unlock()
		ct.seen = append(ct.seen, append([]ChatMessage(nil), msgs...))
		if len(ct.replies) == 0 {
			return "", errors.New("no more scripted replies")
		}
		r := ct.replies[0]
		ct.replies = ct.replies[1:]
		return r, nil
	}
	ct.c.cad.renderStub = func(ctx context.Context, code string, defs map[string]string) ([]byte, string, error) {
		ct.mu.Lock()
		ct.renders = append(ct.renders, defs)
		ct.mu.Unlock()
		if strings.Contains(code, "broken") {
			return nil, "ERROR: Parser error in file model.scad, line 2: syntax error", nil
		}
		w := float32(10)
		if v, ok := defs["w"]; ok && v == "20" {
			w = 20
		} else if strings.Contains(code, "w = 30") {
			w = 30
		}
		return binarySTL(w, 5, 2), "", nil
	}
	return ct
}

func (ct *cadTest) call(t *testing.T, h http.HandlerFunc, method, path, id, body string) map[string]any {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if id != "" {
		r.SetPathValue("id", id)
	}
	w := httptest.NewRecorder()
	h(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code >= 300 {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	return out
}

func (ct *cadTest) wait(t *testing.T, id string) *CadRec {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ct.c.cad.mu.Lock()
		rec := ct.c.cad.recs[id]
		busy := rec.busy()
		v := *rec
		ct.c.cad.mu.Unlock()
		if !busy {
			return &v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the 3D model never finished")
	return nil
}

func TestCadFixRoundsAndCheck(t *testing.T) {
	ct := newCadTest(t,
		"```openscad\nw = 10; // [1:50]\nbroken(\n```",
		"Fixed.\n```openscad\nw = 10; // [1:50]\ncube([w, 5, 2]);\n```",
		"OK",
	)
	out := ct.call(t, ct.c.handleNewCad, "POST", "/api/cad", "", `{"prompt":"a 10 mm block","rounds":4}`)
	id := out["model"].(map[string]any)["id"].(string)
	rec := ct.wait(t, id)
	if rec.Status != "done" || rec.Round != 2 || rec.Tris != 12 || rec.Size != [3]float64{10, 5, 2} {
		t.Fatalf("status %s round %d tris %d size %v err %q", rec.Status, rec.Round, rec.Tris, rec.Size, rec.Error)
	}
	if len(ct.seen) != 3 {
		t.Fatalf("%d model calls, want 3 (write, fix, size check)", len(ct.seen))
	}
	if last := ct.seen[1][len(ct.seen[1])-1].Content; !strings.Contains(last, "Parser error") || !strings.Contains(last, "broken(") {
		t.Errorf("the fix round did not get the error and the file: %q", last)
	}
	if last := ct.seen[2][len(ct.seen[2])-1].Content; !strings.Contains(last, "10.00 mm in X") {
		t.Errorf("the size check did not get the size: %q", last)
	}
	dir := filepath.Join(ct.c.cadDir(), id)
	if b, _ := os.ReadFile(filepath.Join(dir, "model.scad")); !strings.Contains(string(b), "cube([w, 5, 2])") {
		t.Errorf("model.scad: %q", b)
	}
	if st, err := os.Stat(filepath.Join(dir, "model.stl")); err != nil || st.Size() != 84+50*12 {
		t.Errorf("model.stl: %v", err)
	}
	if len(rec.Params) != 1 || rec.Params[0].Name != "w" {
		t.Errorf("params: %+v", rec.Params)
	}

	// Change a parameter: render again without the model.
	calls := len(ct.seen)
	ct.call(t, ct.c.handleCadParams, "POST", "/api/cad/"+id+"/params", id, `{"values":{"w":"20"}}`)
	rec = ct.wait(t, id)
	if rec.Status != "done" || rec.Size[0] != 20 || rec.Version != 2 || len(ct.seen) != calls {
		t.Fatalf("after params: %s size %v version %d calls %d", rec.Status, rec.Size, rec.Version, len(ct.seen))
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "model.scad")); !strings.Contains(string(b), "w = 10;") {
		t.Errorf("a parameter render must not rewrite the file: %q", b)
	}
	r := httptest.NewRequest("POST", "/x", strings.NewReader(`{"values":{"w":"1); import(\"x\""}}`))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	ct.c.handleCadParams(w, r)
	if w.Code != 400 {
		t.Errorf("a non-number override must be refused, got %d", w.Code)
	}

	// Ask for a change: the model gets the current file.
	ct.replies = []string{"```openscad\nw = 30;\ncube([w, 5, 2]);\n```"}
	ct.call(t, ct.c.handleCadAsk, "POST", "/api/cad/"+id+"/ask", id, `{"text":"make it 30 mm"}`)
	rec = ct.wait(t, id)
	if rec.Status != "done" || rec.Size[0] != 30 || len(rec.Asks) != 2 || len(rec.Overrides) != 0 {
		t.Fatalf("after ask: %s size %v asks %d err %q", rec.Status, rec.Size, len(rec.Asks), rec.Error)
	}
	if ask := ct.seen[len(ct.seen)-1][1].Content; !strings.Contains(ask, "w = 20; // [1:50]") || !strings.Contains(ask, "make it 30 mm") {
		t.Errorf("the change request lacks the file or the ask: %q", ask)
	}

	// A restart marks unfinished work and keeps the rest.
	ct.c.cad.mu.Lock()
	ct.c.cad.recs[id].Status = "working"
	ct.c.saveCadLocked(ct.c.cad.recs[id])
	ct.c.cad.mu.Unlock()
	ct.c.loadCad()
	if got := ct.c.cad.recs[id]; got == nil || got.Status != "failed" || !strings.Contains(got.Error, "restart") {
		t.Fatalf("after restart: %+v", got)
	}
}

func TestCadGivesUpAfterRounds(t *testing.T) {
	ct := newCadTest(t, "```openscad\nbroken(\n```", "```openscad\nbroken(again\n```")
	out := ct.call(t, ct.c.handleNewCad, "POST", "/api/cad", "", `{"prompt":"x","rounds":2}`)
	rec := ct.wait(t, out["model"].(map[string]any)["id"].(string))
	if rec.Status != "failed" || !strings.Contains(rec.Error, "after 2 rounds") {
		t.Fatalf("status %s err %q", rec.Status, rec.Error)
	}
	if ct.c.cadCode(rec.ID) == "" {
		t.Error("the last file should be kept so the owner can see it")
	}
}

func TestCadStopAndRetention(t *testing.T) {
	ct := newCadTest(t)
	block := make(chan struct{})
	ct.c.cad.llmStub = func(ctx context.Context, rec *CadRec, msgs []ChatMessage) (string, error) {
		close(block)
		<-ctx.Done()
		return "", ctx.Err()
	}
	out := ct.call(t, ct.c.handleNewCad, "POST", "/api/cad", "", `{"prompt":"x"}`)
	id := out["model"].(map[string]any)["id"].(string)
	<-block
	ct.call(t, ct.c.handleCadStop, "POST", "/api/cad/"+id+"/stop", id, "")
	if rec := ct.wait(t, id); rec.Status != "stopped" {
		t.Fatalf("status %s", rec.Status)
	}
	// Old and unkept goes, kept stays.
	ct.c.cad.mu.Lock()
	old := time.Now().AddDate(0, 0, -genDefaultDays-1)
	ct.c.cad.recs[id].Updated = old
	ct.c.cad.recs["cad_kept"] = &CadRec{ID: "cad_kept", Keep: true, Status: "done", Updated: old}
	ct.c.cad.mu.Unlock()
	ct.c.pruneCad()
	if _, ok := ct.c.cad.recs[id]; ok {
		t.Error("an old model was not pruned")
	}
	if _, ok := ct.c.cad.recs["cad_kept"]; !ok {
		t.Error("a kept model was pruned")
	}
}

func TestCadNeedsOpenSCAD(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), nodes: map[string]*NodeRec{}, lastHB: map[string]*Heartbeat{}, conns: map[string]*nodeConn{}, lastSeen: map[string]time.Time{}}
	c.loadGens()
	c.loadCad()
	r := httptest.NewRequest("POST", "/api/cad", strings.NewReader(`{"prompt":"x"}`))
	w := httptest.NewRecorder()
	c.handleNewCad(w, r)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "OpenSCAD") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

// TestCadRealOpenSCAD runs the render command line with a real OpenSCAD when
// one is installed (skipped otherwise), to check the argv and the STL reader.
func TestCadRealOpenSCAD(t *testing.T) {
	bin, err := exec.LookPath("openscad")
	if err != nil {
		t.Skip("openscad is not installed")
	}
	dir := t.TempDir()
	code := "w = 70; // [20:200]\nslot = 12;\ndifference() {\n  cube([w, 80, 8]);\n  translate([10, 10, 4]) cube([w - 20, slot, 20]);\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "model.scad"), []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	argv := cadRenderArgv(bin, map[string]string{"w": "90"}, true)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "model.stl"))
	if err != nil {
		t.Fatal(err)
	}
	size, tris, err := stlStats(b)
	if err != nil || size != [3]float64{90, 80, 8} || tris < 12 {
		t.Fatalf("size %v tris %d err %v", size, tris, err)
	}
	// A broken file fails with a message the fix round can use.
	_ = os.WriteFile(filepath.Join(dir, "model.scad"), []byte("cube(10\n"), 0o644)
	os.Remove(filepath.Join(dir, "model.stl"))
	cmd = exec.Command(argv[0], cadRenderArgv(bin, nil, true)[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "syntax error") {
		t.Fatalf("broken file: %v %s", err, out)
	}
}

func TestCadLookupMeasurements(t *testing.T) {
	ct := newCadTest(t,
		"GL-AR750 dimensions mm\n10 inch rack panel width",
		`Here: [{"name":"GL-AR750 body","value":"100 × 68 × 24 mm","source":"example.com"},{"name":"","value":"skip"}]`,
		"```openscad\nrouter_w = 100;\ncube([router_w, 5, 2]);\n```",
		"OK",
	)
	var fetched, searched []string
	ct.c.cad.fetchStub = func(ctx context.Context, u string) (string, error) {
		fetched = append(fetched, u)
		if strings.Contains(u, "bad") {
			return "", errors.New("404")
		}
		return "Wall mount for a small router. Printed in PETG.", nil
	}
	ct.c.cad.searchStub = func(ctx context.Context, q string) (string, []string, error) {
		searched = append(searched, q)
		return "1. GL-AR750 specs\n   https://example.com/ar750\n   Size 100 x 68 x 24 mm", []string{"https://bad.example/x", "https://example.com/ar750"}, nil
	}
	body := `{"prompt":"modify this 3d model https://makerworld.example/models/1 for a GL.iNet GL-AR750","read_links":true,"lookup":true}`
	out := ct.call(t, ct.c.handleNewCad, "POST", "/api/cad", "", body)
	rec := ct.wait(t, out["model"].(map[string]any)["id"].(string))
	if rec.Status != "done" {
		t.Fatalf("status %s: %s", rec.Status, rec.Error)
	}
	if len(rec.Refs) != 1 || rec.Refs[0].Value != "100 × 68 × 24 mm" {
		t.Fatalf("refs %+v", rec.Refs)
	}
	if len(searched) != 2 || searched[0] != "GL-AR750 dimensions mm" {
		t.Errorf("searches %q", searched)
	}
	if len(fetched) == 0 || fetched[0] != "https://makerworld.example/models/1" {
		t.Errorf("fetched %q", fetched)
	}
	if src := ct.seen[1][0].Content; !strings.Contains(src, "Wall mount for a small router") || !strings.Contains(src, "Size 100 x 68") {
		t.Errorf("the measurement call did not get the sources: %q", src)
	}
	write := ct.seen[2][len(ct.seen[2])-1].Content
	if !strings.Contains(write, "GL-AR750 body: 100 × 68 × 24 mm (example.com)") || !strings.Contains(write, "named parameter") {
		t.Errorf("the writing call did not get the measurements: %q", write)
	}
}

func TestCadLookupNothingFound(t *testing.T) {
	ct := newCadTest(t, "NONE", "```openscad\ncube([10, 5, 2]);\n```", "OK")
	ct.c.cad.searchStub = func(ctx context.Context, q string) (string, []string, error) {
		t.Errorf("searched although the model said NONE: %q", q)
		return "", nil, nil
	}
	out := ct.call(t, ct.c.handleNewCad, "POST", "/api/cad", "", `{"prompt":"a 3d printable 10 mm block","lookup":true}`)
	rec := ct.wait(t, out["model"].(map[string]any)["id"].(string))
	if rec.Status != "done" || len(rec.Refs) != 0 {
		t.Fatalf("status %s refs %v err %s", rec.Status, rec.Refs, rec.Error)
	}
	if w := ct.seen[1][len(ct.seen[1])-1].Content; !strings.Contains(w, "No measurements were found") {
		t.Errorf("writing call: %q", w)
	}
}

func TestCadQueriesAndRefs(t *testing.T) {
	if q := cadQueries("1. \"router size mm\"\n- 10 inch rack width\nthird one"); len(q) != 2 || q[0] != "router size mm" || q[1] != "10 inch rack width" {
		t.Errorf("queries %q", q)
	}
	if r := parseCadRefs("no json here"); r != nil {
		t.Errorf("refs %v", r)
	}
}
