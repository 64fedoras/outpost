package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWriteFileAtomicKeepsBackup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	if err := writeFileAtomicBackup(p, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".bak"); err == nil {
		t.Fatal("first save has nothing to back up")
	}
	if err := writeFileAtomicBackup(p, []byte(`{"v":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != `{"v":2}` {
		t.Fatalf("got %s", b)
	}
	if b, _ := os.ReadFile(p + ".bak"); string(b) != `{"v":1}` {
		t.Fatalf("backup %s", b)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode())
		}
	}
	// no temp files left behind
	ents, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("left %s", e.Name())
		}
	}
}

func TestReadWithBackupFallsBack(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	os.WriteFile(p, []byte(`{"v":`), 0o600) // cut off mid-write
	os.WriteFile(p+".bak", []byte(`{"v":7}`), 0o600)
	var v struct{ V int }
	used, err := readWithBackup(p, func(b []byte) error { return json.Unmarshal(b, &v) })
	if err != nil || !used || v.V != 7 {
		t.Fatalf("used=%v err=%v v=%d", used, err, v.V)
	}
	os.WriteFile(p+".bak", []byte(`nope`), 0o600)
	if _, err := readWithBackup(p, func(b []byte) error { return json.Unmarshal(b, &v) }); err != errBothDamaged {
		t.Fatalf("want errBothDamaged, got %v", err)
	}
}

func TestDamagedTaskIsRecoveredOrMovedAside(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), projects: map[string]*Project{}}
	mk := func(id, main, bak string) {
		d := filepath.Join(c.tasksDir(), id)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "task.json"), []byte(main), 0o644)
		if bak != "" {
			os.WriteFile(filepath.Join(d, "task.json.bak"), []byte(bak), 0o644)
		}
	}
	good := `{"id":"%s","title":"T","prompt":"x","status":"done","subtasks":[],"events":[],"messages":[]}`
	mk("t_ok", strings.Replace(good, "%s", "t_ok", 1), "")
	mk("t_bak", `{"id":"t_bak",`, strings.Replace(good, "%s", "t_bak", 1))
	mk("t_bad", `garbage`, "")
	c.loadTasks()
	if c.tasks["t_ok"] == nil || c.tasks["t_bak"] == nil {
		t.Fatalf("loaded %v", c.tasks)
	}
	if c.tasks["t_bad"] != nil {
		t.Fatal("garbage task loaded")
	}
	if _, err := os.Stat(filepath.Join(c.tasksDir(), "t_bad")); err == nil {
		t.Fatal("damaged task left in place")
	}
	ents, _ := os.ReadDir(filepath.Join(c.tasksDir(), damagedDir))
	if len(ents) != 1 || !strings.HasPrefix(ents[0].Name(), "t_bad-") {
		t.Fatalf("quarantine: %v", ents)
	}
	notes := c.recoveredList()
	if len(notes) != 2 {
		t.Fatalf("notes: %q", notes)
	}
	// a second start does not treat the quarantine folder as a task
	c.recovered = recoveredNotes{}
	c.loadTasks()
	if len(c.recoveredList()) != 0 || len(c.tasks) != 2 {
		t.Fatalf("second load: %q %d", c.recoveredList(), len(c.tasks))
	}
}

func TestLogLinesSaveOnceAfterDelay(t *testing.T) {
	tk := &Task{ID: "t1", Status: "running"}
	tk.dir = t.TempDir()
	tk.logf("info", "one")
	tk.logf("info", "two")
	p := filepath.Join(tk.dir, "task.json")
	if _, err := os.Stat(p); err == nil {
		t.Fatal("log lines should not save at once")
	}
	tk.flush()
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), `"two"`) {
		t.Fatalf("flush: %v %s", err, b)
	}
	// a state change saves immediately
	tk.logf("info", "three")
	tk.setStatus("done")
	b, _ = os.ReadFile(p)
	if !strings.Contains(string(b), `"three"`) || !strings.Contains(string(b), `"done"`) {
		t.Fatalf("setStatus did not save: %s", b)
	}
	time.Sleep(saveDelay + 100*time.Millisecond) // the timer was cancelled; nothing else happens
}

func TestRotatingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "node.log")
	r, err := openRotating(p, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i := 0; i < 30; i++ {
		r.Write([]byte("0123456789\n"))
	}
	st, _ := os.Stat(p)
	old, err := os.Stat(p + ".old")
	if err != nil || st.Size() > 100 || old.Size() > 100 {
		t.Fatalf("sizes %d / %v", st.Size(), err)
	}
}

func TestHealthz(t *testing.T) {
	c := &Controller{conns: map[string]*nodeConn{"n1": {}}, tasks: map[string]*Task{"a": {Status: "running"}, "b": {Status: "done"}}}
	w := httptest.NewRecorder()
	c.handleHealth(w, httptest.NewRequest("GET", "/healthz", nil))
	var v map[string]any
	json.Unmarshal(w.Body.Bytes(), &v)
	if w.Code != 200 || v["nodes_online"] != 1.0 || v["tasks_active"] != 1.0 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), Version) {
		t.Fatal("healthz must not reveal the version")
	}
	controllerClosing.Store(true)
	defer controllerClosing.Store(false)
	w = httptest.NewRecorder()
	c.handleHealth(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 503 {
		t.Fatalf("closing: %d", w.Code)
	}
}

func TestDamagedSettingsUseBackup(t *testing.T) {
	c := &Controller{dataDir: t.TempDir()}
	os.WriteFile(c.settingsPath(), []byte(`{"claude_model":`), 0o600)
	os.WriteFile(c.settingsPath()+".bak", []byte(`{"default_cap_usd": 5}`), 0o600)
	c.loadSettings()
	if c.settings.DefaultCapUSD != 5 || len(c.recoveredList()) != 1 {
		t.Fatalf("cap %v notes %q", c.settings.DefaultCapUSD, c.recoveredList())
	}
}
