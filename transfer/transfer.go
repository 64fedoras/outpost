// Package transfer moves large files between Outpost machines in hash-checked
// chunks. A Server hands out files to holders of a granted token over TLS with
// its own self-signed certificate; Fetch downloads one file from several
// peers at once, pins every peer's certificate fingerprint, resumes after a
// dropped connection, and only publishes the file if the whole-file SHA-256
// matches the hash the caller got from a trusted place (the controller).
// It depends on nothing but the standard library.
package transfer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ChunkSize is the unit of work split across peers and retried on failure.
	ChunkSize = 64 << 20
	// DefaultPort is the port peers serve on.
	DefaultPort = 8444
)

// ErrHashMismatch is returned when the finished file does not match the
// expected SHA-256. The partial file is deleted.
var ErrHashMismatch = errors.New("transfer: file does not match the expected SHA-256")

// ---------- certificates ----------

// Fingerprint is the lowercase hex SHA-256 of a DER certificate.
func Fingerprint(der []byte) string {
	s := sha256.Sum256(der)
	return hex.EncodeToString(s[:])
}

// LoadOrCreateCert keeps a self-signed certificate in dir (peer.crt, peer.key)
// and returns it with its fingerprint.
func LoadOrCreateCert(dir, name string) (tls.Certificate, string, error) {
	crt, key := filepath.Join(dir, "peer.crt"), filepath.Join(dir, "peer.key")
	if c, err := tls.LoadX509KeyPair(crt, key); err == nil {
		return c, Fingerprint(c.Certificate[0]), nil
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "Outpost peer " + name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	kb, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(crt, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return tls.Certificate{}, "", err
	}
	c, err := tls.LoadX509KeyPair(crt, key)
	return c, Fingerprint(der), err
}

// PinTLS trusts exactly the certificate with this fingerprint.
func PinTLS(fp string) *tls.Config {
	want := strings.ToLower(fp)
	return &tls.Config{
		InsecureSkipVerify: true, // replaced by the pin check
		MinVersion:         tls.VersionTLS12,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 || Fingerprint(raw[0]) != want {
				return errors.New("peer certificate does not match the pinned fingerprint")
			}
			return nil
		},
	}
}

// ---------- server ----------

// Resolver maps a file ID to a local path. ok=false means "not served".
type Resolver func(id string) (path string, ok bool)

// Server serves files to clients that present a granted token.
type Server struct {
	Resolve Resolver
	Cert    tls.Certificate
	Chunk   int64 // chunk size for the hash list; 0 = ChunkSize

	mu     sync.Mutex
	tokens map[string]time.Time
	hashes map[string]*hashList
}

type hashList struct {
	size  int64
	mtime time.Time
	hs    []string
}

// ChunkList is the response of GET /v1/chunks/<id>.
type ChunkList struct {
	Size   int64    `json:"size"`
	Chunk  int64    `json:"chunk"`
	Hashes []string `json:"hashes"`
}

// Grant lets the holder of token download until ttl passes. ttl <= 0 revokes it.
func (s *Server) Grant(token string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokens == nil {
		s.tokens = map[string]time.Time{}
	}
	if ttl <= 0 {
		delete(s.tokens, token)
		return
	}
	s.tokens[token] = time.Now().Add(ttl)
	for t, exp := range s.tokens { // drop expired grants
		if time.Now().After(exp) {
			delete(s.tokens, t)
		}
	}
}

func (s *Server) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || got == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	okAny := false
	for t, exp := range s.tokens { // compare all, so timing does not reveal a prefix
		if subtle.ConstantTimeCompare([]byte(t), []byte(got)) == 1 && time.Now().Before(exp) {
			okAny = true
		}
	}
	return okAny
}

// Handler returns the HTTP handler: /v1/file/<id> (Range supported) and /v1/chunks/<id>.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/file/{id...}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.lookup(w, r)
		if !ok {
			return
		}
		f, err := os.Open(p)
		if err != nil {
			http.Error(w, "unavailable", http.StatusNotFound)
			return
		}
		defer f.Close()
		st, _ := f.Stat()
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, "", st.ModTime(), f)
	})
	mux.HandleFunc("GET /v1/chunks/{id...}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.lookup(w, r)
		if !ok {
			return
		}
		cl, err := s.chunkList(p)
		if err != nil {
			http.Error(w, "unavailable", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cl)
	})
	return mux
}

func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return "", false
	}
	p, ok := s.Resolve(r.PathValue("id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return "", false
	}
	return p, true
}

// chunkList hashes the file's chunks once and caches the result until it changes.
func (s *Server) chunkList(path string) (*ChunkList, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	chunk := s.Chunk
	if chunk <= 0 {
		chunk = ChunkSize
	}
	s.mu.Lock()
	if s.hashes == nil {
		s.hashes = map[string]*hashList{}
	}
	if h := s.hashes[path]; h != nil && h.size == st.Size() && h.mtime.Equal(st.ModTime()) {
		s.mu.Unlock()
		return &ChunkList{Size: h.size, Chunk: chunk, Hashes: h.hs}, nil
	}
	s.mu.Unlock()
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var hs []string
	for off := int64(0); off < st.Size(); off += chunk {
		h := sha256.New()
		if _, err := io.CopyN(h, f, min(chunk, st.Size()-off)); err != nil {
			return nil, err
		}
		hs = append(hs, hex.EncodeToString(h.Sum(nil)))
	}
	s.mu.Lock()
	s.hashes[path] = &hashList{size: st.Size(), mtime: st.ModTime(), hs: hs}
	s.mu.Unlock()
	return &ChunkList{Size: st.Size(), Chunk: chunk, Hashes: hs}, nil
}

// Serve serves TLS on l until it is closed.
func (s *Server) Serve(l net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{s.Cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 15 * time.Second,
	}
	return srv.ServeTLS(l, "", "")
}

// ---------- client ----------

// Peer is one machine to fetch from.
type Peer struct {
	Name        string `json:"name"`
	Addr        string `json:"addr"` // host:port
	Fingerprint string `json:"fingerprint"`
}

// Request describes one file to fetch.
type Request struct {
	ID       string // file ID on the peers
	Dest     string // final path; Dest+".part" and Dest+".part.done" hold progress
	Size     int64
	SHA256   string // expected whole-file hash (required)
	Peers    []Peer
	Token    string
	Chunk    int64            // 0 = ChunkSize; must match the peers' chunk list to be used
	Progress func(done int64) // called after each finished chunk
}

// Result lists peers that served data failing its chunk hash or the final hash.
type Result struct {
	Bad []string
}

type fetchState struct {
	req    Request
	chunk  int64
	n      int
	hashes []string // chunk hashes from a peer (untrusted; the final hash is the authority)

	mu       sync.Mutex
	pending  []int
	inflight int // chunks a worker is fetching right now
	attempts map[int]int
	done     map[int]bool
	doneSize int64
	by       map[int]string // chunk -> peer that served it
	bad      map[string]bool
	f        *os.File
	log      *os.File
}

// Fetch downloads req.ID from the peers into req.Dest.
func Fetch(ctx context.Context, req Request) (Result, error) {
	if req.SHA256 == "" || req.Size <= 0 || len(req.Peers) == 0 {
		return Result{}, errors.New("transfer: need a SHA-256, a size and at least one peer")
	}
	st := &fetchState{req: req, chunk: req.Chunk, attempts: map[int]int{}, done: map[int]bool{}, by: map[int]string{}, bad: map[string]bool{}}
	if st.chunk <= 0 {
		st.chunk = ChunkSize
	}
	st.n = int((req.Size + st.chunk - 1) / st.chunk)
	part := req.Dest + ".part"
	if err := os.MkdirAll(filepath.Dir(req.Dest), 0o755); err != nil {
		return Result{}, err
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	if err := f.Truncate(req.Size); err != nil {
		return Result{}, err
	}
	st.f = f
	st.loadDone(part + ".done")
	lg, err := os.OpenFile(part+".done", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return Result{}, err
	}
	defer lg.Close()
	st.log = lg
	for i := 0; i < st.n; i++ {
		if st.done[i] {
			st.doneSize += min(st.chunk, req.Size-int64(i)*st.chunk)
		} else {
			st.pending = append(st.pending, i)
		}
	}
	clients := make([]*http.Client, len(req.Peers))
	for i, p := range req.Peers {
		clients[i] = &http.Client{Transport: &http.Transport{TLSClientConfig: PinTLS(p.Fingerprint), DisableKeepAlives: false}}
	}
	st.hashes = st.chunkHashes(ctx, clients)

	if len(st.pending) > 0 {
		if err := st.run(ctx, clients); err != nil {
			return st.result(), err
		}
	}
	// whole-file check against the trusted hash
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return st.result(), err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return st.result(), err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), req.SHA256) {
		f.Close()
		lg.Close()
		os.Remove(part)
		os.Remove(part + ".done")
		st.mu.Lock()
		for _, p := range st.by { // without chunk hashes we cannot tell who sent the bad bytes
			st.bad[p] = true
		}
		st.mu.Unlock()
		return st.result(), ErrHashMismatch
	}
	f.Close()
	lg.Close()
	if err := os.Rename(part, req.Dest); err != nil {
		return st.result(), err
	}
	os.Remove(part + ".done")
	return st.result(), nil
}

func (s *fetchState) result() Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	var r Result
	for p := range s.bad {
		r.Bad = append(r.Bad, p)
	}
	return r
}

func (s *fetchState) loadDone(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, ln := range strings.Fields(string(b)) {
		if i, err := strconv.Atoi(ln); err == nil && i >= 0 && i < s.n {
			s.done[i] = true
		}
	}
}

func (s *fetchState) newReq(ctx context.Context, p Peer, path string) (*http.Request, error) {
	r, err := http.NewRequestWithContext(ctx, "GET", "https://"+p.Addr+path, nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+s.req.Token)
	return r, nil
}

// chunkHashes asks the peers in turn for the chunk hash list; nil if none answers.
func (s *fetchState) chunkHashes(ctx context.Context, clients []*http.Client) []string {
	for i, p := range s.req.Peers {
		r, err := s.newReq(ctx, p, "/v1/chunks/"+s.req.ID)
		if err != nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Minute) // peers hash the file on first request
		resp, err := clients[i].Do(r.WithContext(cctx))
		if err != nil {
			cancel()
			continue
		}
		var cl ChunkList
		err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&cl)
		resp.Body.Close()
		cancel()
		if resp.StatusCode == 200 && err == nil && cl.Size == s.req.Size && cl.Chunk == s.chunk && len(cl.Hashes) == s.n {
			return cl.Hashes
		}
	}
	return nil
}

// next hands out a pending chunk. With none pending it reports wait while
// another worker still has a chunk in flight, because that chunk may fail and
// come back: a worker that left at that point could leave it unfetched.
func (s *fetchState) next() (i int, ok, wait bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return 0, false, s.inflight > 0
	}
	i = s.pending[0]
	s.pending = s.pending[1:]
	s.inflight++
	return i, true, false
}

// finished ends a chunk handed out by next; failed puts it back in the queue.
func (s *fetchState) finished(i int, failed bool) {
	s.mu.Lock()
	if failed {
		s.pending = append(s.pending, i)
	}
	s.inflight--
	s.mu.Unlock()
}

// run starts one worker per peer and waits until every chunk is done.
func (s *fetchState) run(ctx context.Context, clients []*http.Client) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	fail := func(e error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		errMu.Unlock()
		cancel()
	}
	alive := len(s.req.Peers)
	var aliveMu sync.Mutex
	for pi := range s.req.Peers {
		wg.Add(1)
		go func(pi int) {
			defer wg.Done()
			p := s.req.Peers[pi]
			strikes := 0
			for ctx.Err() == nil && strikes < 3 {
				i, ok, wait := s.next()
				if wait {
					select {
					case <-ctx.Done():
					case <-time.After(50 * time.Millisecond):
					}
					continue
				}
				if !ok {
					return
				}
				s.mu.Lock()
				s.attempts[i]++
				tries := s.attempts[i]
				s.mu.Unlock()
				if tries > 2*len(s.req.Peers)+2 {
					s.finished(i, true)
					fail(fmt.Errorf("chunk %d failed too many times", i))
					return
				}
				err := s.getChunk(ctx, clients[pi], p, i)
				s.finished(i, err != nil)
				if err == nil {
					strikes = 0
					continue
				}
				var be badData
				if errors.As(err, &be) {
					s.mu.Lock()
					s.bad[p.Name] = true
					s.mu.Unlock()
					break // never ask a peer that sent bad data again
				}
				strikes++
				select {
				case <-ctx.Done():
				case <-time.After(time.Duration(strikes) * 500 * time.Millisecond):
				}
			}
			aliveMu.Lock()
			alive--
			left := alive
			aliveMu.Unlock()
			s.mu.Lock()
			rest := len(s.pending)
			s.mu.Unlock()
			if left == 0 && rest > 0 && ctx.Err() == nil {
				fail(errors.New("no peer could deliver the remaining chunks"))
			}
		}(pi)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

type badData struct{ msg string }

func (b badData) Error() string { return b.msg }

func (s *fetchState) getChunk(ctx context.Context, c *http.Client, p Peer, i int) error {
	off := int64(i) * s.chunk
	length := min(s.chunk, s.req.Size-off)
	r, err := s.newReq(ctx, p, "/v1/file/"+s.req.ID)
	if err != nil {
		return err
	}
	r.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+length-1))
	resp, err := c.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("%s returned %s", p.Name, resp.Status)
	}
	var h hash.Hash
	var w io.Writer = io.NewOffsetWriter(s.f, off)
	if s.hashes != nil {
		h = sha256.New()
		w = io.MultiWriter(w, h)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, length))
	if err != nil {
		return err
	}
	if n != length {
		return io.ErrUnexpectedEOF
	}
	if h != nil && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), s.hashes[i]) {
		return badData{fmt.Sprintf("chunk %d from %s failed its hash", i, p.Name)}
	}
	s.mu.Lock()
	s.done[i] = true
	s.by[i] = p.Name
	s.doneSize += length
	done := s.doneSize
	fmt.Fprintf(s.log, "%d\n", i)
	s.mu.Unlock()
	if s.req.Progress != nil {
		s.req.Progress(done)
	}
	return nil
}
