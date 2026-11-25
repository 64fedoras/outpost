package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanUploadPath(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"", "a.txt"}:               "a.txt",
		{"", "site/css/a.css"}:      "site/css/a.css",
		{"docs", "a.txt"}:           "docs/a.txt",
		{"", `win\dir\a.txt`}:       "win/dir/a.txt",
		{"", "../../etc/passwd"}:    "etc/passwd", // cleaned inside the folder, never above it
		{"", ""}:                    "",
		{"", ".upload-123"}:         "",
		{"", "a/./b/../c.txt"}:      "a/c.txt",
		{"x/../..", "y.txt"}:        "y.txt",
		{"", "a\x00b"}:              "",
		{"", "dir/.upload-x/f.txt"}: "",
	} {
		if got := cleanUploadPath(in[0], in[1]); got != want {
			t.Errorf("cleanUploadPath(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

// Upload a folder and a file, list them, open them, see them in the Artifacts row,
// then delete a file and a folder.
func TestProjectArtifactUploads(t *testing.T) {
	c := testController(t)
	w := httptest.NewRecorder()
	c.handleCreateProject(w, httptest.NewRequest("POST", "/api/projects", strings.NewReader(`{"name":"up"}`)))
	var created struct{ ID string }
	json.Unmarshal(w.Body.Bytes(), &created)
	p := c.projects[created.ID]
	if p == nil {
		t.Fatalf("create: %s", w.Body)
	}
	mux := http.NewServeMux()
	c.projectArtifactRoutes(mux, func(h http.HandlerFunc) http.HandlerFunc { return h })
	mux.HandleFunc("GET /api/projects/{id}/overview", c.handleProjectOverview)
	do := func(method, target string, body *bytes.Buffer, ctype string) *httptest.ResponseRecorder {
		var r *http.Request
		if body != nil {
			r = httptest.NewRequest(method, target, body)
			r.Header.Set("Content-Type", ctype)
		} else {
			r = httptest.NewRequest(method, target, nil)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	upload := func(dir string, files map[string]string) *httptest.ResponseRecorder {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		if dir != "" {
			mw.WriteField("dir", dir)
		}
		for name, body := range files {
			fw, _ := mw.CreateFormFile("files", name)
			fw.Write([]byte(body))
		}
		mw.Close()
		return do("POST", "/api/projects/"+p.ID+"/artifacts", &b, mw.FormDataContentType())
	}
	base := "/api/projects/" + p.ID + "/artifacts"

	if w := upload("", map[string]string{"site/index.html": "<script>x</script>", "site/img/a.png": "\x89PNG", "notes.csv": "a,b\n1,2\n"}); w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	if w := upload("site", map[string]string{"more.txt": "more"}); w.Code != 200 {
		t.Fatalf("upload into a folder: %d %s", w.Code, w.Body)
	}
	if w := upload("", map[string]string{"site": "a file over a folder"}); w.Code == 200 {
		t.Fatal("a file replaced a folder")
	}
	if _, err := os.Stat(filepath.Join(p.artifactsDir(), "..", "repo", "x")); err == nil {
		t.Fatal("upload left its folder")
	}

	var list struct {
		Files []uploadedFile `json:"files"`
		Bytes int64          `json:"bytes"`
	}
	json.Unmarshal(do("GET", base, nil, "").Body.Bytes(), &list)
	var paths []string
	for _, f := range list.Files {
		paths = append(paths, f.Path)
	}
	if got := strings.Join(paths, ","); got != "notes.csv,site/img/a.png,site/index.html,site/more.txt" {
		t.Fatalf("list: %s", got)
	}

	if w := do("GET", base+"/file?path=site/index.html&view=1", nil, ""); w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Body.String() != "<script>x</script>" {
		t.Errorf("html as text: %q %q", w.Header().Get("Content-Type"), w.Body.String())
	}
	if w := do("GET", base+"/file?path=site/img/a.png&view=1", nil, ""); w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("picture: %q", w.Header().Get("Content-Type"))
	}
	if w := do("GET", base+"/file?path=notes.csv&download=1", nil, ""); !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("download: %q", w.Header().Get("Content-Disposition"))
	}
	if w := do("GET", base+"/file?path=notes.csv&text=1", nil, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "1") {
		t.Errorf("document text: %d %q", w.Code, w.Body.String())
	}
	if w := do("GET", base+"/file?path=../project.json&view=1", nil, ""); w.Code == 200 {
		t.Error("file route left its folder")
	}

	r := httptest.NewRequest("GET", "/api/projects/"+p.ID+"/overview", nil)
	r.SetPathValue("id", p.ID)
	w = httptest.NewRecorder()
	c.handleProjectOverview(w, r)
	var ov projectOverview
	json.Unmarshal(w.Body.Bytes(), &ov)
	cards := map[string]projectArtifact{}
	for _, a := range ov.Artifacts {
		cards[a.Kind+":"+a.Name] = a
	}
	if f := cards["folder:site/"]; f.Files != 3 || f.Path != "site" {
		t.Errorf("folder card: %+v", ov.Artifacts)
	}
	if f := cards["upload:notes.csv"]; f.Path != "notes.csv" || !strings.Contains(f.URL, "download=1") {
		t.Errorf("file card: %+v", ov.Artifacts)
	}

	if w := do("DELETE", base+"?path=site/img/a.png", nil, ""); w.Code != 200 {
		t.Fatalf("delete file: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(p.artifactsDir(), "site", "img")); err == nil {
		t.Error("an emptied folder stayed")
	}
	if w := do("DELETE", base+"?path=site", nil, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"removed":2`) {
		t.Fatalf("delete folder: %d %s", w.Code, w.Body)
	}
	for _, bad := range []string{"", "/", "..", "../repo"} {
		if w := do("DELETE", base+"?path="+bad, nil, ""); w.Code == 200 {
			t.Errorf("delete %q was allowed", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(p.artifactsDir(), "notes.csv")); err != nil {
		t.Error("the other upload went too")
	}
}

func TestTaskArtifactDelete(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"artifacts/dist/a", "artifacts/dist/b", "artifacts/c"} {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte("x"), 0o644)
	}
	tk := &Task{ID: "t1", Status: "done", dir: dir, Artifacts: []Artifact{{Path: "dist/a"}, {Path: "dist/b"}, {Path: "c"}}}
	c := &Controller{tasks: map[string]*Task{"t1": tk}}
	mux := http.NewServeMux()
	c.projectArtifactRoutes(mux, func(h http.HandlerFunc) http.HandlerFunc { return h })
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/tasks/t1/artifact?path=dist", nil))
	if w.Code != 200 || len(tk.Artifacts) != 1 || tk.Artifacts[0].Path != "c" {
		t.Fatalf("%d %s %+v", w.Code, w.Body, tk.Artifacts)
	}
	tk.Status = "running"
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/tasks/t1/artifact?path=c", nil))
	if w.Code != 409 {
		t.Fatalf("deleted while running: %d", w.Code)
	}
}
