package main

// The Stash's HTTP API (the web UI's Stash page and the project Stash dialog use it).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (c *Controller) stashRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/stash", c.auth(c.handleStashList))
	mux.HandleFunc("POST /api/stash", c.authSession(c.handleStashCreate))
	mux.HandleFunc("GET /api/stash/{id}", c.auth(c.handleStashGet))
	mux.HandleFunc("POST /api/stash/{id}", c.authSession(c.handleStashUpdate))
	mux.HandleFunc("DELETE /api/stash/{id}", c.authSession(c.handleStashDelete))
	mux.HandleFunc("POST /api/stash/{id}/resources", c.authSession(c.handleStashUpload))
	mux.HandleFunc("DELETE /api/stash/{id}/resources/{rid}", c.authSession(c.handleStashRemoveResource))
	mux.HandleFunc("GET /api/stash/{id}/resources/{rid}/file", c.auth(c.handleStashFile))
	mux.HandleFunc("POST /api/stash/{id}/search", c.auth(c.handleStashSearch))
	mux.HandleFunc("GET /api/stash/{id}/passages/{n}", c.auth(c.handleStashPassage))
	mux.HandleFunc("POST /api/stash/{id}/reindex", c.authSession(c.handleStashReindex))
}

type stashView struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	ProjectID   string    `json:"project_id,omitempty"`
	Status      string    `json:"status"` // empty | indexing | needs_reindex | ready
	Resources   int       `json:"resources"`
	Passages    int       `json:"passages"`
	Bytes       int64     `json:"bytes"`
	Model       string    `json:"model,omitempty"`
	Progress    int       `json:"progress,omitempty"` // percent, while indexing
	Failed      int       `json:"failed,omitempty"`
	ChunkChars  int       `json:"chunk_chars"`
	Overlap     int       `json:"overlap"`
	Updated     time.Time `json:"updated"`
}

func (c *Controller) stashViewOf(s *Stash) stashView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := stashView{ID: s.ID, Name: s.Name, Description: s.Description, ProjectID: s.ProjectID, Status: c.stashStatusLocked(s), Resources: len(s.Resources),
		Passages: s.passageCount(), Model: s.Model, ChunkChars: firstInt(s.ChunkChars, stashChunkDefault), Overlap: firstInt(s.Overlap, stashOverlapDefault), Updated: s.Updated}
	var done, total int
	for _, r := range s.Resources {
		v.Bytes += r.Bytes
		if r.Status == resFailed {
			v.Failed++
		}
		if r.Status == resIndexing || r.Status == resQueued {
			done += r.Done
			total += max(r.Total, 1)
		}
	}
	if total > 0 {
		v.Progress = 100 * done / total
	}
	return v
}

func firstInt(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

func (c *Controller) handleStashList(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	var out []stashView
	for _, s := range c.stashList() {
		if project != "" && s.ProjectID != project {
			continue
		}
		if project == "" && s.ProjectID != "" && r.URL.Query().Get("all") == "" {
			continue // a project's own stash is managed on its project page
		}
		out = append(out, c.stashViewOf(s))
	}
	if out == nil {
		out = []stashView{}
	}
	cur := ""
	if m, err := c.embedModel(""); err == nil {
		cur = m.Name
	}
	writeJSON(w, 200, map[string]any{"stashes": out, "embed_model": cur, "embed_ready": cur != ""})
}

func (c *Controller) handleStashCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	s, err := c.stashCreate(req.Name, req.Description, "")
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, c.stashViewOf(s))
}

type resourceView struct {
	*Resource
	Page string `json:"-"`
}

func (c *Controller) handleStashGet(w http.ResponseWriter, r *http.Request) {
	s := c.stashGet(r.PathValue("id"))
	if s == nil {
		writeJSON(w, 404, map[string]string{"error": "no such stash"})
		return
	}
	v := c.stashViewOf(s)
	s.mu.RLock()
	res := make([]Resource, len(s.Resources))
	for i, x := range s.Resources {
		res[i] = *x
	}
	s.mu.RUnlock()
	cur := ""
	if m, err := c.embedModel(""); err == nil {
		cur = m.Name
	}
	writeJSON(w, 200, map[string]any{"stash": v, "resources": res, "embed_model": cur, "embed_ready": cur != ""})
}

func (c *Controller) handleStashUpdate(w http.ResponseWriter, r *http.Request) {
	s := c.stashGet(r.PathValue("id"))
	if s == nil {
		writeJSON(w, 404, map[string]string{"error": "no such stash"})
		return
	}
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		ChunkChars  *int    `json:"chunk_chars"`
		Overlap     *int    `json:"overlap"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	if e := c.stashApplyUpdate(s, req.Name, req.Description, req.ChunkChars, req.Overlap); e != nil {
		writeJSON(w, e.code, map[string]string{"error": e.msg})
		return
	}
	writeJSON(w, 200, c.stashViewOf(s))
}

type httpErr struct {
	code int
	msg  string
}

func (c *Controller) stashApplyUpdate(s *Stash, name, desc *string, chunk, overlap *int) *httpErr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name != nil {
		n := cleanStashText(*name, stashNameMax)
		if n == "" {
			return &httpErr{400, "give the stash a name"}
		}
		s.Name = n
	}
	if desc != nil {
		s.Description = cleanStashText(*desc, stashDescMax)
	}
	rebuild := false
	if chunk != nil || overlap != nil {
		size, ov := firstInt(s.ChunkChars, stashChunkDefault), firstInt(s.Overlap, stashOverlapDefault)
		if chunk != nil {
			size = *chunk
		}
		if overlap != nil {
			ov = *overlap
		}
		if size < stashChunkMin || size > stashChunkMax || ov < 0 || ov > size/3 {
			return &httpErr{400, fmt.Sprintf("passage size must be %d to %d characters and the overlap at most a third of it", stashChunkMin, stashChunkMax)}
		}
		rebuild = size != firstInt(s.ChunkChars, stashChunkDefault) || ov != firstInt(s.Overlap, stashOverlapDefault)
		s.ChunkChars, s.Overlap = size, ov
	}
	if rebuild && len(s.Resources) > 0 {
		// New passage sizes: every resource is cut and embedded again from its stored file.
		if s.working {
			return &httpErr{409, "indexing is running; change the passage size when it is done"}
		}
		s.idx = &stashIndex{}
		s.Model, s.Dim = "", 0
		for _, x := range s.Resources {
			x.Status, x.Passages, x.Done, x.Error = resQueued, 0, 0, ""
		}
		_ = s.saveIndexLocked()
		c.stashKick(s)
	}
	s.Updated = time.Now()
	if err := s.saveLocked(); err != nil {
		return &httpErr{500, err.Error()}
	}
	return nil
}

func (c *Controller) handleStashDelete(w http.ResponseWriter, r *http.Request) {
	if !c.stashDelete(r.PathValue("id")) {
		writeJSON(w, 404, map[string]string{"error": "no such stash"})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// POST /api/stash/{id}/resources?name=file.pdf with the file's bytes as the body (pasted text: any .txt or .md name).
func (c *Controller) handleStashUpload(w http.ResponseWriter, r *http.Request) {
	s := c.stashGet(r.PathValue("id"))
	if s == nil {
		writeJSON(w, 404, map[string]string{"error": "no such stash"})
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxResourceBytes+1))
	if err != nil {
		writeJSON(w, 413, map[string]string{"error": fmt.Sprintf("the file is larger than %d MB", maxResourceBytes>>20)})
		return
	}
	res, err := c.stashAddResource(s, r.URL.Query().Get("name"), data)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	s.mu.RLock()
	cp := *res
	s.mu.RUnlock()
	writeJSON(w, 200, cp)
}

func (c *Controller) handleStashRemoveResource(w http.ResponseWriter, r *http.Request) {
	s := c.stashGet(r.PathValue("id"))
	if s == nil || !c.stashRemoveResource(s, r.PathValue("rid")) {
		writeJSON(w, 404, map[string]string{"error": "no such resource"})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleStashFile(w http.ResponseWriter, r *http.Request) {
	s := c.stashGet(r.PathValue("id"))
	if s == nil {
		writeJSON(w, 404, map[string]string{"error": "no such stash"})
		return
	}
	s.mu.RLock()
	var res *Resource
	for _, x := range s.Resources {
		if x.ID == r.PathValue("rid") {
			cp := *x
			res = &cp
		}
	}
	s.mu.RUnlock()
	if res == nil {
		writeJSON(w, 404, map[string]string{"error": "no such resource"})
		return
	}
	f, err := os.Open(filepath.Join(s.dir, "files", res.File))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "the file is gone"})
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", res.Name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, f)
}

func (c *Controller) handleStashReindex(w http.ResponseWriter, r *http.Request) {
	s := c.stashGet(r.PathValue("id"))
	if s == nil {
		writeJSON(w, 404, map[string]string{"error": "no such stash"})
		return
	}
	if err := c.stashReindex(s); err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, c.stashViewOf(s))
}

// hitView is a search hit for the UI: the passage with its resource and stash names.
type hitView struct {
	StashHit
	ResName string `json:"resource"`
}

func (c *Controller) handleStashSearch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Q string `json:"q"`
		K int    `json:"k"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil || strings.TrimSpace(req.Q) == "" {
		writeJSON(w, 400, map[string]string{"error": "type something to search for"})
		return
	}
	if c.stashGet(id) == nil {
		writeJSON(w, 404, map[string]string{"error": "no such stash"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	hits, note, err := c.stashSearch(ctx, []string{id}, req.Q, min(max(req.K, 1), 20))
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"hits": hits, "note": note})
}

func (c *Controller) handleStashPassage(w http.ResponseWriter, r *http.Request) {
	s := c.stashGet(r.PathValue("id"))
	n, _ := strconv.Atoi(r.PathValue("n"))
	if s == nil {
		writeJSON(w, 404, map[string]string{"error": "no such stash"})
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.idx == nil || n < 0 || n >= len(s.idx.ps) {
		writeJSON(w, 404, map[string]string{"error": "no such passage"})
		return
	}
	p := s.idx.ps[n]
	out := map[string]any{"passage": p, "index": n, "resource": s.resourceNameLocked(p.Res), "stash": s.Name}
	if n > 0 && s.idx.ps[n-1].Res == p.Res {
		out["before"] = s.idx.ps[n-1].Text
	}
	if n+1 < len(s.idx.ps) && s.idx.ps[n+1].Res == p.Res {
		out["after"] = s.idx.ps[n+1].Text
	}
	writeJSON(w, 200, out)
}

func (s *Stash) resourceNameLocked(id string) string {
	for _, r := range s.Resources {
		if r.ID == id {
			return r.Name
		}
	}
	return "(removed)"
}

// stashSearch searches the named stashes for q and returns up to k hits, best first. note says
// when words only could be used (no embedding model, or a stash that needs re-indexing).
func (c *Controller) stashSearch(ctx context.Context, ids []string, q string, k int) ([]hitView, string, error) {
	var stashes []*Stash
	for _, id := range ids {
		if s := c.stashGet(id); s != nil {
			stashes = append(stashes, s)
		}
	}
	if len(stashes) == 0 {
		return nil, "", errors.New("no such stash")
	}
	var qvec []float32
	cur := ""
	var embedErr error
	if m, err := c.embedModel(""); err == nil {
		cur = m.Name
		need := false
		for _, s := range stashes {
			s.mu.RLock()
			need = need || (s.Model == cur && s.passageCount() > 0)
			s.mu.RUnlock()
		}
		if need {
			res, err := c.Embed(ctx, "", "query", []string{q})
			if err != nil {
				embedErr = err
			} else {
				qvec = res.Vectors[0]
			}
		}
	}
	var all []hitView
	words := 0
	for _, s := range stashes {
		s.mu.Lock()
		if s.idx != nil && s.idx.post == nil {
			s.idx.build()
		}
		ix := s.idx
		var hits []StashHit
		if ix != nil {
			use := qvec
			if s.Model != cur {
				use = nil
			}
			if use == nil && len(ix.ps) > 0 {
				words++
			}
			hits = ix.search(q, use, k)
		}
		for _, h := range hits {
			h.Stash = s.ID
			all = append(all, hitView{StashHit: h, ResName: s.resourceNameLocked(h.Res)})
		}
		s.mu.Unlock()
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	if len(all) > k {
		all = all[:k]
	}
	note := ""
	switch {
	case embedErr != nil:
		note = "Searched by words only: the embedding model could not be reached (" + embedErr.Error() + ")."
	case words > 0 && cur == "":
		note = "Searched by words only: there is no embedding model in the library yet."
	case words > 0:
		note = "Searched by words only: a stash needs re-indexing for the current embedding model."
	}
	return all, note, nil
}
