package main

// Project artifacts: files and folders the owner uploads to a project's Artifacts,
// kept on the controller in <project>/artifacts (never on a node). The file browser
// lists, opens, uploads and deletes them; task build outputs can be deleted the same way.
// Pictures, sound and video are served with their own type so the browser shows them;
// PDF, DOCX, XLSX and ODT can be read as extracted text; everything else is plain text.
// Nothing is ever served as HTML, so an uploaded page cannot run on the controller's
// origin (pages go through the sandboxed preview panel instead).

import (
	"fmt"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	maxArtifactUpload = 4 << 30   // one upload request, all its files together
	minArtifactFree   = 512 << 20 // keep this much disk free on the controller after an upload
	maxArtifactList   = 5000      // files listed at once
)

func (p *Project) artifactsDir() string { return filepath.Join(p.dir, "artifacts") }

// projectArtifactRoutes adds the routes, each wrapped in auth (c.auth; tests pass none).
func (c *Controller) projectArtifactRoutes(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/projects/{id}/artifacts", auth(c.handleProjectArtifactList))
	mux.HandleFunc("POST /api/projects/{id}/artifacts", auth(c.handleProjectArtifactUpload))
	mux.HandleFunc("DELETE /api/projects/{id}/artifacts", auth(c.handleProjectArtifactDelete))
	mux.HandleFunc("GET /api/projects/{id}/artifacts/file", auth(c.handleProjectArtifactFile))
	mux.HandleFunc("DELETE /api/tasks/{id}/artifact", auth(c.handleTaskArtifactDelete))
}

// uploadedFile is one uploaded artifact.
type uploadedFile struct {
	Path  string    `json:"path"`
	Bytes int64     `json:"bytes"`
	Time  time.Time `json:"time"`
}

// listUploads walks the artifacts folder: files only, sorted by path, at most maxArtifactList.
func listUploads(root string) (files []uploadedFile, total int64, cut bool) {
	files = []uploadedFile{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".upload-") { // a write in progress
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		if len(files) >= maxArtifactList {
			cut = true
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		files = append(files, uploadedFile{Path: filepath.ToSlash(rel), Bytes: info.Size(), Time: info.ModTime()})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, total, cut
}

func (c *Controller) handleProjectArtifactList(w http.ResponseWriter, r *http.Request) {
	p := c.getProject(r.PathValue("id"))
	if p == nil {
		writeJSON(w, 404, map[string]string{"error": "no such project"})
		return
	}
	files, total, cut := listUploads(p.artifactsDir())
	writeJSON(w, 200, map[string]any{"files": files, "bytes": total, "cut": cut})
}

// cleanUploadPath turns a browser's file name (with webkitRelativePath for a folder)
// and an optional target folder into a safe relative path, or "" when it is unusable.
func cleanUploadPath(dir, name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	dir = strings.ReplaceAll(dir, "\\", "/")
	p := path.Clean("/" + dir + "/" + name)
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." || strings.Contains(p, "\x00") {
		return ""
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".upload-") {
			return ""
		}
	}
	return p
}

// handleProjectArtifactUpload takes multipart "files" parts (a part's file name may
// hold a folder path, as a folder upload sends) and an optional "dir" field naming
// the folder to put them in, which must come before the files. Each file is written
// beside its target and renamed into place, so a broken upload leaves no half file.
func (c *Controller) handleProjectArtifactUpload(w http.ResponseWriter, r *http.Request) {
	p := c.getProject(r.PathValue("id"))
	if p == nil {
		writeJSON(w, 404, map[string]string{"error": "no such project"})
		return
	}
	root := p.artifactsDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if r.ContentLength > 0 && uint64(r.ContentLength)+minArtifactFree > diskFree(root) {
		writeJSON(w, 507, map[string]string{"error": "not enough free disk on the controller for this upload"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxArtifactUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "send the files as multipart/form-data"})
		return
	}
	dir, saved := "", []string{}
	fail := func(code int, msg string) {
		writeJSON(w, code, map[string]any{"error": msg, "saved": saved})
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(400, fmt.Sprintf("the upload was cut off or is larger than %d GB", maxArtifactUpload>>30))
			return
		}
		switch part.FormName() {
		case "dir":
			b, _ := io.ReadAll(io.LimitReader(part, 1024))
			dir = strings.Trim(strings.TrimSpace(string(b)), "/")
		case "files":
			name := rawFileName(part)
			rel := cleanUploadPath(dir, name)
			if rel == "" {
				fail(400, fmt.Sprintf("%q is not a usable file name", name))
				return
			}
			dst, err := safeJoin(root, rel)
			if err == nil {
				err = writeUpload(dst, part)
			}
			if err != nil {
				fail(500, rel+": "+err.Error())
				return
			}
			saved = append(saved, rel)
		}
	}
	if len(saved) == 0 {
		fail(400, "no files in the upload")
		return
	}
	writeJSON(w, 200, map[string]any{"saved": saved})
}

// rawFileName is the part's file name with its folders: Part.FileName keeps only
// the last element, and a folder upload sends "folder/sub/file.txt".
func rawFileName(part *multipart.Part) string {
	_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	if err != nil {
		return part.FileName()
	}
	return params["filename"]
}

func writeUpload(dst string, in io.Reader) error {
	if st, err := os.Stat(dst); err == nil && st.IsDir() {
		return fmt.Errorf("a folder of that name is already there")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("a file of that name is in the way of its folder")
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".upload-*")
	if err != nil {
		return err
	}
	_, err = io.Copy(tmp, in)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

func (c *Controller) handleProjectArtifactFile(w http.ResponseWriter, r *http.Request) {
	p := c.getProject(r.PathValue("id"))
	if p == nil {
		writeJSON(w, 404, map[string]string{"error": "no such project"})
		return
	}
	serveArtifactFile(w, r, p.artifactsDir())
}

// serveArtifactFile serves one file under root: ?download=1 as an attachment,
// ?text=1 as a document's extracted text, ?view=1 for the in-app viewer.
func serveArtifactFile(w http.ResponseWriter, r *http.Request, root string) {
	fp, err := safeJoin(root, r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad path"})
		return
	}
	f, err := os.Open(fp)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "no such file"})
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		writeJSON(w, 404, map[string]string{"error": "no such file"})
		return
	}
	q := r.URL.Query()
	switch {
	case q.Get("download") != "":
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+strings.ReplaceAll(filepath.Base(fp), "\"", "")+"\"")
		http.ServeContent(w, r, "", st.ModTime(), f)
	case q.Get("text") != "":
		serveDocText(w, fp, f, st.Size())
	default:
		serveViewable(w, r, fp, f, st)
	}
}

// serveDocText answers with a document's text (PDF, DOCX, XLSX, ODT, CSV), for the viewer.
func serveDocText(w http.ResponseWriter, name string, f *os.File, size int64) {
	if docKind(name) == "" {
		writeJSON(w, 400, map[string]string{"error": "not a document"})
		return
	}
	if size > maxDocBytes {
		writeJSON(w, 413, map[string]string{"error": fmt.Sprintf("larger than %d MB, too big to read here; download it instead", maxDocBytes>>20)})
		return
	}
	b, err := io.ReadAll(f)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	text, info, err := extractDocument(name, b)
	if err != nil {
		writeJSON(w, 422, map[string]string{"error": err.Error()})
		return
	}
	if info.Warning != "" {
		text = "(" + info.Warning + ")\n\n" + text
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	io.WriteString(w, text)
}

func (c *Controller) handleProjectArtifactDelete(w http.ResponseWriter, r *http.Request) {
	p := c.getProject(r.PathValue("id"))
	if p == nil {
		writeJSON(w, 404, map[string]string{"error": "no such project"})
		return
	}
	n, err := removeUnder(p.artifactsDir(), r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]int{"removed": n})
}

// removeUnder deletes one file, or a folder with everything in it, under root
// (never root itself) and returns how many files went.
func removeUnder(root, rel string) (int, error) {
	rel = strings.Trim(rel, "/")
	fp, err := safeJoin(root, rel)
	if err != nil || filepath.Clean(fp) == filepath.Clean(root) {
		return 0, fmt.Errorf("bad path")
	}
	st, err := os.Lstat(fp)
	if err != nil {
		return 0, fmt.Errorf("nothing there")
	}
	n := 1
	if st.IsDir() {
		n = 0
		_ = filepath.WalkDir(fp, func(_ string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				n++
			}
			return nil
		})
	}
	if err := os.RemoveAll(fp); err != nil {
		return 0, err
	}
	// drop folders the delete left empty, up to root
	for d := filepath.Dir(fp); d != filepath.Clean(root) && strings.HasPrefix(d, filepath.Clean(root)); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			break
		}
	}
	return n, nil
}

// handleTaskArtifactDelete deletes one build output, or a folder of them (?path=dist).
func (c *Controller) handleTaskArtifactDelete(w http.ResponseWriter, r *http.Request) {
	t := c.getTask(r.PathValue("id"))
	if t == nil {
		writeJSON(w, 404, map[string]string{"error": "no such task"})
		return
	}
	rel := strings.Trim(r.URL.Query().Get("path"), "/")
	if taskActive(t) {
		writeJSON(w, 409, map[string]string{"error": "the task is still working; wait until it finishes"})
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	n, err := removeUnder(t.artifactsDir(), rel)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	kept := t.Artifacts[:0]
	for _, a := range t.Artifacts {
		if a.Path != rel && !strings.HasPrefix(a.Path, rel+"/") {
			kept = append(kept, a)
		}
	}
	t.Artifacts = kept
	t.saveLocked()
	writeJSON(w, 200, map[string]int{"removed": n})
}

// uploadsArtifacts gives the Artifacts row one card per top-level upload: a file,
// or a folder with its file count and size.
func uploadsArtifacts(files []uploadedFile) []projectArtifact {
	type agg struct {
		n     int
		bytes int64
		t     time.Time
	}
	dirs, order := map[string]*agg{}, []string{}
	var out []projectArtifact
	for _, f := range files {
		top, _, isDir := strings.Cut(f.Path, "/")
		if !isDir {
			out = append(out, projectArtifact{Kind: "upload", Name: f.Path, Path: f.Path, Bytes: f.Bytes, Time: f.Time})
			continue
		}
		a := dirs[top]
		if a == nil {
			a = &agg{}
			dirs[top] = a
			order = append(order, top)
		}
		a.n++
		a.bytes += f.Bytes
		if f.Time.After(a.t) {
			a.t = f.Time
		}
	}
	for _, d := range order {
		a := dirs[d]
		out = append(out, projectArtifact{Kind: "folder", Name: d + "/", Path: d, Bytes: a.bytes, Files: a.n, Time: a.t})
	}
	return out
}
