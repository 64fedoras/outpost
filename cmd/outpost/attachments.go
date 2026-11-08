package main

// Task attachments. The composer can send files with a new task:
//
//   - a .zip becomes the task's starting files (extracted into the task's repo,
//     which for a project task is the project's repo); a single top-level folder
//     in the archive is stripped, as GitHub's "Download ZIP" adds one;
//   - any other file must be text and is kept under <task>/attachments, to be shown
//     to the planner as context (not to the writing nodes).
//
// Everything together may be at most maxAttachBytes. Images and other binary
// files are refused for now (roadmap row 19).

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxAttachBytes    = 50 << 20
	maxZipEntries     = 20000
	maxZipUnpacked    = 500 << 20
	maxTextAttachment = 1 << 20
)

// Attachment records a file sent with a task. Kind is "zip" or "text".
type Attachment struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Bytes int64  `json:"bytes"`
	Files int    `json:"files,omitempty"` // zip: how many files were extracted
}

type createReq struct {
	Prompt      string        `json:"prompt"`
	Planner     string        `json:"planner"`
	ClaudeModel string        `json:"claude_model"`
	ORModel     string        `json:"openrouter_model"`
	CapUSD      float64       `json:"cap_usd"`
	Review      *bool         `json:"review"`
	Project     string        `json:"project"`
	Mockup      bool          `json:"mockup"`       // plan design variants to pick from first (visual.go)
	TestFirst   bool          `json:"test_first"`   // the plan starts with a subtask that writes the tests (tasks_testfirst.go)
	FixRounds   int           `json:"fix_rounds"`   // 0 = project or default
	TestMinutes int           `json:"test_minutes"` // 0 = project or default
	Images      []imageUpload `json:"images"`       // screenshots for the planner (tasks_images.go)
	NodeLabel   string        `json:"node_label"`   // only nodes with this label work on it; "" = project default, then any
}

type attachedFile struct {
	Name string
	Data []byte
}

// readCreateRequest reads a new-task request: plain JSON, or multipart/form-data
// with a "payload" field (the JSON) and any number of "files" parts.
func readCreateRequest(w http.ResponseWriter, r *http.Request) (createReq, []attachedFile, error) {
	var req createReq
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxImageBodyBytes)).Decode(&req); err != nil {
			return req, nil, errors.New("describe the task")
		}
		return req, nil, nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachBytes+maxImageBodyBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		return req, nil, err
	}
	var files []attachedFile
	var total int64
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return req, nil, fmt.Errorf("attachments are larger than %d MB or could not be read", maxAttachBytes>>20)
		}
		switch part.FormName() {
		case "payload":
			if err := json.NewDecoder(io.LimitReader(part, maxImageBodyBytes)).Decode(&req); err != nil {
				return req, nil, errors.New("describe the task")
			}
		case "files":
			name := filepath.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
			if name == "." || name == "/" || name == "" {
				continue
			}
			data, err := io.ReadAll(io.LimitReader(part, maxAttachBytes+1-total))
			total += int64(len(data))
			if err != nil || total > maxAttachBytes {
				return req, nil, fmt.Errorf("attachments are larger than %d MB", maxAttachBytes>>20)
			}
			files = append(files, attachedFile{Name: name, Data: data})
		}
	}
	return req, files, nil
}

// applyAttachments stores a task's attachments; on error nothing is kept by the caller.
func (c *Controller) applyAttachments(t *Task, files []attachedFile) error {
	for _, f := range files { // check everything before writing anything
		if !strings.HasSuffix(strings.ToLower(f.Name), ".zip") && !looksLikeText(f.Data) {
			return fmt.Errorf("%s is not a text file. Attach text files or a .zip (images are not supported yet)", f.Name)
		}
		if !strings.HasSuffix(strings.ToLower(f.Name), ".zip") && len(f.Data) > maxTextAttachment {
			return fmt.Errorf("%s is larger than 1 MB; put big files in a .zip", f.Name)
		}
	}
	used := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(f.Name), ".zip") {
			n, err := unpackAttachedZip(f.Data, t.repoDir())
			if err != nil {
				return fmt.Errorf("%s: %w", f.Name, err)
			}
			t.Attachments = append(t.Attachments, Attachment{Name: f.Name, Kind: "zip", Bytes: int64(len(f.Data)), Files: n})
			if n > 0 {
				t.Seeded = true
			}
			continue
		}
		name := f.Name
		for i := 2; used[name]; i++ {
			name = fmt.Sprintf("%d-%s", i, f.Name)
		}
		used[name] = true
		dir := filepath.Join(t.dir, "attachments")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), f.Data, 0o644); err != nil {
			return err
		}
		t.Attachments = append(t.Attachments, Attachment{Name: name, Kind: "text", Bytes: int64(len(f.Data))})
	}
	return nil
}

func looksLikeText(b []byte) bool {
	return utf8.Valid(b) && !bytes.Contains(b, []byte{0})
}

// inGitDir reports whether a slash path has a .git component. Files there can run code
// (hooks, config), so neither models nor archives may write them.
func inGitDir(rel string) bool {
	for _, seg := range strings.Split(strings.ReplaceAll(rel, "\\", "/"), "/") {
		if strings.EqualFold(seg, ".git") {
			return true
		}
	}
	return false
}

// unpackAttachedZip unpacks data into dest and returns the number of files written.
// It skips symlinks, .git folders and macOS junk, and refuses paths that leave dest.
func unpackAttachedZip(data []byte, dest string) (int, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0, errors.New("not a valid zip file")
	}
	if len(zr.File) > maxZipEntries {
		return 0, fmt.Errorf("the archive has more than %d entries", maxZipEntries)
	}
	var entries []*zip.File
	for _, f := range zr.File {
		name := strings.TrimPrefix(strings.ReplaceAll(f.Name, "\\", "/"), "./")
		if f.FileInfo().IsDir() || f.Mode()&os.ModeSymlink != 0 || name == "" ||
			strings.HasPrefix(name, "__MACOSX/") || path.Base(name) == ".DS_Store" || inGitDir(name) {
			continue
		}
		entries = append(entries, f)
	}
	// Strip one shared top-level folder.
	strip := ""
	if len(entries) > 0 {
		top := strings.SplitN(strings.TrimPrefix(strings.ReplaceAll(entries[0].Name, "\\", "/"), "./"), "/", 2)
		if len(top) == 2 {
			strip = top[0] + "/"
			for _, f := range entries {
				if !strings.HasPrefix(strings.TrimPrefix(strings.ReplaceAll(f.Name, "\\", "/"), "./"), strip) {
					strip = ""
					break
				}
			}
		}
	}
	var unpacked int64
	n := 0
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	for _, f := range entries {
		rel := strings.TrimPrefix(strings.TrimPrefix(strings.ReplaceAll(f.Name, "\\", "/"), "./"), strip)
		p, err := safeJoin(dest, rel)
		if err != nil {
			return n, fmt.Errorf("unsafe path %q in the archive", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return n, err
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxZipUnpacked-unpacked+1))
		rc.Close()
		unpacked += int64(len(b))
		if err != nil || unpacked > maxZipUnpacked {
			return n, fmt.Errorf("the archive unpacks to more than %d MB", maxZipUnpacked>>20)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return n, err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// attachmentContext renders a task's text attachments for the planner, using at most budget bytes.
func attachmentContext(t *Task, budget int) string {
	t.mu.Lock()
	list := append([]Attachment(nil), t.Attachments...)
	t.mu.Unlock()
	var b strings.Builder
	for _, a := range list {
		if a.Kind == "zip" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(t.dir, "attachments", a.Name))
		if err != nil {
			continue
		}
		s := string(data)
		if budget -= len(s) + 40; budget < 0 {
			if b.Len() == 0 {
				b.WriteString("Attached files for context:\n")
			}
			fmt.Fprintf(&b, "<<<FILE %s>>>\n(left out: too long for this prompt)\n<<<END>>>\n", a.Name)
			continue
		}
		if b.Len() == 0 {
			b.WriteString("Attached files for context:\n")
		}
		fmt.Fprintf(&b, "<<<FILE %s>>>\n%s\n<<<END>>>\n", a.Name, s)
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	return b.String()
}
