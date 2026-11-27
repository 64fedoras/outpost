package main

// The Stash: named collections of Resources (files and pasted text) that chats
// and tasks can search. Resources live on the controller only (data/stash/<id>/);
// a node never receives one: it is only handed passages of text to embed, in
// memory, and keeps nothing (node_embed.go). Indexing and search: stash_chunk.go,
// stash_search.go, stash_http.go; chat use: stash_chat.go.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxStashes                                  = 50
	maxStashResources                           = 500
	maxResourceBytes                            = 50 << 20
	maxResourcePassages                         = 6000
	maxStashPassages                            = 200000
	stashEmbedGroup                             = 64 // passages per embedding call while indexing
	stashNameMax                                = 80
	stashDescMax                                = 300
	resQueued, resIndexing, resReady, resFailed = "queued", "indexing", "ready", "failed"
)

// Resource is one file or pasted text in a stash.
type Resource struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind"` // pdf | docx | xlsx | odt | csv | text
	Bytes    int64     `json:"bytes"`
	Passages int       `json:"passages"`
	Status   string    `json:"status"`
	Done     int       `json:"done,omitempty"` // passages embedded so far while indexing
	Total    int       `json:"total,omitempty"`
	Error    string    `json:"error,omitempty"`
	Warning  string    `json:"warning,omitempty"`
	Added    time.Time `json:"added"`
	File     string    `json:"file"` // stored name in files/
}

// Stash is a collection of resources and its search index.
type Stash struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	ProjectID   string      `json:"project_id,omitempty"` // a project's own stash
	Model       string      `json:"model,omitempty"`      // embedding model the passages were made with
	Dim         int         `json:"dim,omitempty"`
	ChunkChars  int         `json:"chunk_chars,omitempty"`
	Overlap     int         `json:"overlap,omitempty"`
	Created     time.Time   `json:"created"`
	Updated     time.Time   `json:"updated"`
	Resources   []*Resource `json:"resources"`

	mu      sync.RWMutex
	dir     string
	idx     *stashIndex
	working bool
	gone    bool
	curRes  string
	cancel  context.CancelFunc
}

type stashStore struct {
	once sync.Once
	mu   sync.Mutex
	m    map[string]*Stash
}

func (c *Controller) stashDir() string { return filepath.Join(c.dataDir, "stash") }

// stashAll loads the stashes from disk the first time it is called.
func (c *Controller) stashAll() map[string]*Stash {
	c.stash.once.Do(func() {
		c.stash.m = map[string]*Stash{}
		ents, _ := os.ReadDir(c.stashDir())
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(c.stashDir(), e.Name())
			b, err := os.ReadFile(filepath.Join(dir, "stash.json"))
			if err != nil {
				continue
			}
			s := &Stash{}
			if json.Unmarshal(b, s) != nil || s.ID != e.Name() {
				continue
			}
			s.dir = dir
			s.loadIndex()
			// A restart interrupted indexing: those resources start again with Reindex.
			for _, r := range s.Resources {
				if r.Status == resQueued || r.Status == resIndexing {
					r.Status, r.Error, r.Done = resFailed, "interrupted by a restart; use Re-index to try again", 0
				}
			}
			c.stash.m[s.ID] = s
		}
	})
	return c.stash.m
}

func (c *Controller) stashGet(id string) *Stash {
	m := c.stashAll()
	c.stash.mu.Lock()
	defer c.stash.mu.Unlock()
	return m[id]
}

func (c *Controller) stashList() []*Stash {
	m := c.stashAll()
	c.stash.mu.Lock()
	out := make([]*Stash, 0, len(m))
	for _, s := range m {
		out = append(out, s)
	}
	c.stash.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func cleanStashText(s string, max int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(strings.ToValidUTF8(s, "")), " "))
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

func (c *Controller) stashCreate(name, desc, projectID string) (*Stash, error) {
	name = cleanStashText(name, stashNameMax)
	if name == "" {
		return nil, errors.New("give the stash a name")
	}
	m := c.stashAll()
	c.stash.mu.Lock()
	defer c.stash.mu.Unlock()
	if len(m) >= maxStashes {
		return nil, fmt.Errorf("too many stashes (%d); delete one first", maxStashes)
	}
	for _, s := range m {
		if s.ProjectID == projectID && strings.EqualFold(s.Name, name) {
			return nil, fmt.Errorf("a stash called %q already exists", name)
		}
	}
	now := time.Now()
	s := &Stash{ID: newID("stash"), Name: name, Description: cleanStashText(desc, stashDescMax), ProjectID: projectID, Created: now, Updated: now, Resources: []*Resource{}}
	s.dir = filepath.Join(c.stashDir(), s.ID)
	if err := os.MkdirAll(filepath.Join(s.dir, "files"), 0o700); err != nil {
		return nil, err
	}
	s.mu.Lock()
	err := s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	m[s.ID] = s
	return s, nil
}

func (c *Controller) stashDelete(id string) bool {
	m := c.stashAll()
	c.stash.mu.Lock()
	s := m[id]
	delete(m, id)
	c.stash.mu.Unlock()
	if s == nil {
		return false
	}
	s.mu.Lock()
	s.gone = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	os.RemoveAll(s.dir)
	return true
}

// stashDetach turns a project's own stash into an ordinary one (kept when the
// project is deleted and the owner chose to keep its artifacts).
func (c *Controller) stashDetach(id, projectName string) {
	s := c.stashGet(id)
	if s == nil {
		return
	}
	s.mu.Lock()
	s.ProjectID = ""
	s.Name = clip("Archived: "+projectName, stashNameMax)
	s.saveLocked()
	s.mu.Unlock()
}

// saveLocked writes stash.json (s.mu held).
func (s *Stash) saveLocked() error {
	b, _ := json.MarshalIndent(s, "", "  ")
	return writeFileAtomic(filepath.Join(s.dir, "stash.json"), b, 0o600)
}

// saveIndexLocked writes the passages and their vectors (s.mu held).
func (s *Stash) saveIndexLocked() error {
	ix := s.idx
	if ix == nil {
		ix = &stashIndex{}
	}
	pb, _ := json.Marshal(ix.ps)
	if err := writeFileAtomic(filepath.Join(s.dir, "passages.json"), pb, 0o600); err != nil {
		return err
	}
	vb := make([]byte, 4*len(ix.vecs))
	for i, x := range ix.vecs {
		binary.LittleEndian.PutUint32(vb[4*i:], math.Float32bits(x))
	}
	return writeFileAtomic(filepath.Join(s.dir, "vectors.bin"), vb, 0o600)
}

func (s *Stash) loadIndex() {
	ix := &stashIndex{dim: s.Dim}
	if b, err := os.ReadFile(filepath.Join(s.dir, "passages.json")); err == nil {
		_ = json.Unmarshal(b, &ix.ps)
	}
	if vb, err := os.ReadFile(filepath.Join(s.dir, "vectors.bin")); err == nil && s.Dim > 0 && len(vb) == 4*s.Dim*len(ix.ps) {
		ix.vecs = make([]float32, len(vb)/4)
		for i := range ix.vecs {
			ix.vecs[i] = math.Float32frombits(binary.LittleEndian.Uint32(vb[4*i:]))
		}
	}
	s.idx = ix
}

// vectorsOK says whether every passage has a vector.
func (s *Stash) vectorsOK() bool {
	ix := s.idx
	return ix != nil && (len(ix.ps) == 0 || (ix.dim > 0 && len(ix.vecs) == ix.dim*len(ix.ps)))
}

func (s *Stash) passageCount() int {
	if s.idx == nil {
		return 0
	}
	return len(s.idx.ps)
}

// stashStatus is empty, indexing, needs_reindex or ready (s.mu held for reading).
func (c *Controller) stashStatusLocked(s *Stash) string {
	busy := false
	for _, r := range s.Resources {
		if r.Status == resQueued || r.Status == resIndexing {
			busy = true
		}
	}
	switch {
	case busy:
		return "indexing"
	case len(s.Resources) == 0:
		return "empty"
	case s.passageCount() > 0 && (!s.vectorsOK() || c.stashModelStale(s)):
		return "needs_reindex"
	}
	return "ready"
}

// stashModelStale: the passages were embedded by another model than the one used now.
func (c *Controller) stashModelStale(s *Stash) bool {
	m, err := c.embedModel("")
	return err == nil && s.Model != "" && s.Model != m.Name
}

// ---- resources ----

// textLike says whether data can be read as plain text (a Markdown, text or code file).
func textLike(name string, data []byte) bool {
	if docKind(name) != "" {
		return false
	}
	head := data[:min(len(data), 8192)]
	for i := 0; i < 3 && len(head) > 0 && !utf8.Valid(head); i++ {
		head = head[:len(head)-1] // the cut may fall inside a character
	}
	return utf8.Valid(head) && !strings.ContainsRune(string(head), 0)
}

func cleanResourceName(name string) string {
	name = path.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == "/" {
		return "Pasted text.txt"
	}
	if r := []rune(name); len(r) > 120 {
		name = string(r[:120])
	}
	return name
}

// stashAddResource stores a file and queues it for indexing.
func (c *Controller) stashAddResource(s *Stash, name string, data []byte) (*Resource, error) {
	name = cleanResourceName(name)
	if len(data) == 0 {
		return nil, errors.New("the file is empty")
	}
	if len(data) > maxResourceBytes {
		return nil, fmt.Errorf("%s is larger than %d MB", name, maxResourceBytes>>20)
	}
	kind := docKind(name)
	if kind == "" {
		if !textLike(name, data) {
			return nil, fmt.Errorf("%s is not a PDF, Word, Excel, OpenDocument or CSV file, and does not look like text", name)
		}
		kind = "text"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gone {
		return nil, errors.New("this stash was deleted")
	}
	if len(s.Resources) >= maxStashResources {
		return nil, fmt.Errorf("a stash holds at most %d resources", maxStashResources)
	}
	r := &Resource{ID: newID("res"), Name: name, Kind: kind, Bytes: int64(len(data)), Status: resQueued, Added: time.Now()}
	r.File = r.ID + strings.ToLower(path.Ext(name))
	if len(path.Ext(name)) > 8 {
		r.File = r.ID
	}
	if err := os.MkdirAll(filepath.Join(s.dir, "files"), 0o700); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(filepath.Join(s.dir, "files", r.File), data, 0o600); err != nil {
		return nil, err
	}
	s.Resources = append(s.Resources, r)
	s.Updated = time.Now()
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	c.stashKick(s)
	return r, nil
}

// stashKick starts the stash's indexing worker if it is not running (s.mu held).
func (c *Controller) stashKick(s *Stash) {
	if s.working {
		return
	}
	s.working = true
	go c.stashWork(s)
}

// stashWork indexes queued resources one at a time, then returns.
func (c *Controller) stashWork(s *Stash) {
	for {
		s.mu.Lock()
		var r *Resource
		for _, x := range s.Resources {
			if x.Status == resQueued {
				r = x
				break
			}
		}
		if r == nil || s.gone {
			s.working, s.curRes, s.cancel = false, "", nil
			s.mu.Unlock()
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		s.curRes, s.cancel = r.ID, cancel
		r.Status, r.Done, r.Error = resIndexing, 0, ""
		s.mu.Unlock()
		err := c.stashIndexResource(ctx, s, r)
		cancel()
		s.mu.Lock()
		if err != nil && r.Status != resReady {
			r.Status, r.Error = resFailed, err.Error()
			s.dropPassagesLocked(r.ID)
		}
		s.Updated = time.Now()
		_ = s.saveLocked()
		s.mu.Unlock()
	}
}

// stashIndexResource reads, cuts and embeds one resource and adds its passages.
func (c *Controller) stashIndexResource(ctx context.Context, s *Stash, r *Resource) error {
	data, err := os.ReadFile(filepath.Join(s.dir, "files", r.File))
	if err != nil {
		return err
	}
	var text string
	if r.Kind == "text" {
		text = strings.TrimSpace(strings.ToValidUTF8(string(data), ""))
		if len(text) > maxDocText {
			text = text[:maxDocText]
			r.Warning = "The file is longer than the text kept (400,000 characters); the rest was left out."
		}
	} else {
		var info DocInfo
		text, info, err = extractDocumentMax(r.Name, data, maxResourceBytes, maxDocText)
		if err != nil {
			return err
		}
		r.Warning = info.Warning
		if info.Cut {
			r.Warning = firstNonEmpty(r.Warning, "The document is longer than the text kept (400,000 characters); the rest was left out.")
		}
	}
	s.mu.RLock()
	size, overlap := s.ChunkChars, s.Overlap
	s.mu.RUnlock()
	ps := chunkText(r.ID, text, size, overlap)
	if len(ps) == 0 {
		return errors.New("no text found in it (a scan or an image?)")
	}
	if len(ps) > maxResourcePassages {
		ps = ps[:maxResourcePassages]
		r.Warning = firstNonEmpty(r.Warning, fmt.Sprintf("Only the first %d passages were kept.", maxResourcePassages))
	}
	s.mu.Lock()
	if s.passageCount()+len(ps) > maxStashPassages {
		s.mu.Unlock()
		return fmt.Errorf("the stash would pass %d passages", maxStashPassages)
	}
	r.Total = len(ps)
	s.mu.Unlock()
	var vecs []float32
	model, dim := "", 0
	for i := 0; i < len(ps); i += stashEmbedGroup {
		grp := ps[i:min(i+stashEmbedGroup, len(ps))]
		texts := make([]string, len(grp))
		for j, p := range grp {
			texts[j] = p.Text
		}
		res, err := c.Embed(ctx, "", "document", texts)
		if err != nil {
			return err
		}
		if model == "" {
			model, dim = res.Model, len(res.Vectors[0])
		}
		for _, v := range res.Vectors {
			vecs = append(vecs, v...)
		}
		s.mu.Lock()
		r.Done = i + len(grp)
		s.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gone {
		return errors.New("this stash was deleted")
	}
	if s.idx == nil {
		s.idx = &stashIndex{}
	}
	if s.passageCount() == 0 || s.Model == "" {
		s.Model, s.Dim, s.idx.dim, s.idx.vecs = model, dim, dim, nil
	} else if s.Model != model {
		return fmt.Errorf("this stash was indexed with %s and %s is in use now; use Re-index first", s.Model, model)
	}
	s.dropPassagesLocked(r.ID)
	for i := range ps {
		s.idx.ps = append(s.idx.ps, ps[i])
	}
	s.idx.vecs = append(s.idx.vecs, vecs...)
	s.idx.post = nil
	r.Status, r.Passages, r.Done = resReady, len(ps), len(ps)
	if err := s.saveIndexLocked(); err != nil {
		return err
	}
	return nil
}

// dropPassagesLocked removes a resource's passages and their vectors (s.mu held).
func (s *Stash) dropPassagesLocked(resID string) {
	ix := s.idx
	if ix == nil {
		return
	}
	var ps []Passage
	var vecs []float32
	for i, p := range ix.ps {
		if p.Res == resID {
			continue
		}
		ps = append(ps, p)
		if ix.dim > 0 && len(ix.vecs) == ix.dim*len(ix.ps) {
			vecs = append(vecs, ix.vecs[i*ix.dim:(i+1)*ix.dim]...)
		}
	}
	if len(ps) != len(ix.ps) {
		ix.ps, ix.vecs, ix.post = ps, vecs, nil
	}
}

// stashRemoveResource deletes a resource, its file and its passages.
func (c *Controller) stashRemoveResource(s *Stash, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.Resources {
		if r.ID != id {
			continue
		}
		if r.Status == resIndexing && s.curRes == id && s.cancel != nil {
			s.cancel()
		}
		s.Resources = append(s.Resources[:i], s.Resources[i+1:]...)
		os.Remove(filepath.Join(s.dir, "files", r.File))
		s.dropPassagesLocked(id)
		if s.passageCount() == 0 {
			s.Model, s.Dim = "", 0
			if s.idx != nil {
				s.idx.dim = 0
			}
		}
		s.Updated = time.Now()
		_ = s.saveLocked()
		_ = s.saveIndexLocked()
		return true
	}
	return false
}

// stashReindex retries failed resources and, when the embedding model changed, embeds every passage again.
func (c *Controller) stashReindex(s *Stash) error {
	cur, err := c.embedModel("")
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.working {
		s.mu.Unlock()
		return errors.New("indexing is already running")
	}
	for _, r := range s.Resources {
		if r.Status == resFailed {
			r.Status, r.Error = resQueued, ""
		}
	}
	redo := s.passageCount() > 0 && (s.Model != cur.Name || !s.vectorsOK())
	if redo {
		for _, r := range s.Resources {
			if r.Status == resReady {
				r.Status, r.Done, r.Total = resIndexing, 0, r.Passages
			}
		}
	}
	s.working = true
	s.mu.Unlock()
	go func() {
		if redo {
			if err := c.stashReembed(s); err != nil {
				log.Printf("stash %s: re-index failed: %v", s.ID, err)
				s.mu.Lock()
				for _, r := range s.Resources {
					if r.Status == resIndexing {
						r.Status, r.Error = resFailed, err.Error()
					}
				}
				_ = s.saveLocked()
				s.mu.Unlock()
			}
		}
		c.stashWork(s) // takes over s.working
	}()
	return nil
}

// stashReembed makes new vectors for every passage with the model in use now.
func (c *Controller) stashReembed(s *Stash) error {
	s.mu.RLock()
	ps := append([]Passage(nil), s.idx.ps...)
	s.mu.RUnlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	var vecs []float32
	model, dim := "", 0
	for i := 0; i < len(ps); i += stashEmbedGroup {
		grp := ps[i:min(i+stashEmbedGroup, len(ps))]
		texts := make([]string, len(grp))
		for j, p := range grp {
			texts[j] = p.Text
		}
		res, err := c.Embed(ctx, "", "document", texts)
		if err != nil {
			return err
		}
		if model == "" {
			model, dim = res.Model, len(res.Vectors[0])
		}
		for _, v := range res.Vectors {
			vecs = append(vecs, v...)
		}
		s.mu.Lock()
		for _, r := range s.Resources {
			if r.Status == resIndexing {
				r.Done = min(r.Total, r.Done+len(grp)*r.Total/max(len(ps), 1)+1)
			}
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.idx.ps) != len(ps) {
		return errors.New("the stash changed while it was being re-indexed; try again")
	}
	s.Model, s.Dim, s.idx.dim, s.idx.vecs, s.idx.post = model, dim, dim, vecs, nil
	for _, r := range s.Resources {
		if r.Status == resIndexing {
			r.Status, r.Done = resReady, r.Passages
		}
	}
	s.Updated = time.Now()
	_ = s.saveLocked()
	return s.saveIndexLocked()
}
