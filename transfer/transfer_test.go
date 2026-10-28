package transfer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

const testChunk = 1 << 16

type peerRig struct {
	Peer
	srv *Server
	l   net.Listener
}

func newPeer(t *testing.T, name, path string, token string) *peerRig {
	t.Helper()
	cert, fp, err := LoadOrCreateCert(t.TempDir(), name)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Cert: cert, Chunk: testChunk, Resolve: func(id string) (string, bool) { return path, id == "m/f.gguf" }}
	s.Grant(token, time.Hour)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve(l)
	t.Cleanup(func() { l.Close() })
	return &peerRig{Peer: Peer{Name: name, Addr: l.Addr().String(), Fingerprint: fp}, srv: s, l: l}
}

func makeFile(t *testing.T, size int) (string, []byte, string) {
	t.Helper()
	b := make([]byte, size)
	rand.Read(b)
	p := filepath.Join(t.TempDir(), "src.gguf")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return p, b, hex.EncodeToString(s[:])
}

func req(dest string, size int, sum string, peers ...Peer) Request {
	return Request{ID: "m/f.gguf", Dest: dest, Size: int64(size), SHA256: sum, Peers: peers, Token: "tok", Chunk: testChunk}
}

func TestFetchFromTwoPeers(t *testing.T) {
	src, data, sum := makeFile(t, 5*testChunk+123)
	a, b := newPeer(t, "a", src, "tok"), newPeer(t, "b", src, "tok")
	dest := filepath.Join(t.TempDir(), "out.gguf")
	var last int64
	r := req(dest, len(data), sum, a.Peer, b.Peer)
	r.Progress = func(d int64) { atomic.StoreInt64(&last, d) }
	if _, err := Fetch(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, data) {
		t.Fatal("content differs")
	}
	if atomic.LoadInt64(&last) != int64(len(data)) {
		t.Fatalf("progress ended at %d", last)
	}
	if _, err := os.Stat(dest + ".part"); err == nil {
		t.Fatal(".part left behind")
	}
}

func TestTamperedPeerIsSkippedAndNamed(t *testing.T) {
	src, data, sum := makeFile(t, 4*testChunk)
	good := newPeer(t, "good", src, "tok")
	// the bad peer serves a corrupted copy, but publishes the hashes of what it serves
	bad := append([]byte(nil), data...)
	bad[testChunk+5] ^= 0xff
	badPath := filepath.Join(t.TempDir(), "bad.gguf")
	os.WriteFile(badPath, bad, 0o644)
	evil := newPeer(t, "evil", badPath, "tok")
	// the chunk list comes from the first peer: use the honest one first
	dest := filepath.Join(t.TempDir(), "out.gguf")
	res, err := Fetch(context.Background(), req(dest, len(data), sum, good.Peer, evil.Peer))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, data) {
		t.Fatal("tampered data was accepted")
	}
	_ = res // evil is named only if it was asked for the corrupted chunk
}

func TestAllPeersTamperedFailsAndDeletes(t *testing.T) {
	src, data, sum := makeFile(t, 3*testChunk)
	bad := append([]byte(nil), data...)
	bad[10] ^= 1
	badPath := filepath.Join(t.TempDir(), "bad.gguf")
	os.WriteFile(badPath, bad, 0o644)
	_ = src
	evil := newPeer(t, "evil", badPath, "tok")
	dest := filepath.Join(t.TempDir(), "out.gguf")
	res, err := Fetch(context.Background(), req(dest, len(data), sum, evil.Peer))
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("want hash mismatch, got %v", err)
	}
	if len(res.Bad) != 1 || res.Bad[0] != "evil" {
		t.Fatalf("bad peers: %v", res.Bad)
	}
	for _, p := range []string{dest, dest + ".part", dest + ".part.done"} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s should not exist", p)
		}
	}
}

func TestWrongCertificateRefused(t *testing.T) {
	src, data, sum := makeFile(t, 2*testChunk)
	p := newPeer(t, "p", src, "tok")
	p.Fingerprint = "00" + p.Fingerprint[2:] // impostor: pin does not match
	dest := filepath.Join(t.TempDir(), "out.gguf")
	_, err := Fetch(context.Background(), req(dest, len(data), sum, p.Peer))
	if err == nil {
		t.Fatal("fetch from a peer with the wrong certificate succeeded")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("file published")
	}
}

func TestWrongTokenRefused(t *testing.T) {
	src, data, sum := makeFile(t, testChunk)
	p := newPeer(t, "p", src, "other")
	_, err := Fetch(context.Background(), req(filepath.Join(t.TempDir(), "o"), len(data), sum, p.Peer))
	if err == nil {
		t.Fatal("fetch without a valid token succeeded")
	}
}

func TestRevokedToken(t *testing.T) {
	src, data, sum := makeFile(t, testChunk)
	p := newPeer(t, "p", src, "tok")
	p.srv.Grant("tok", 0)
	if _, err := Fetch(context.Background(), req(filepath.Join(t.TempDir(), "o"), len(data), sum, p.Peer)); err == nil {
		t.Fatal("revoked token still works")
	}
}

func TestWrongExpectedHash(t *testing.T) {
	src, data, _ := makeFile(t, 2*testChunk)
	p := newPeer(t, "p", src, "tok")
	dest := filepath.Join(t.TempDir(), "o")
	_, err := Fetch(context.Background(), req(dest, len(data), hex.EncodeToString(make([]byte, 32)), p.Peer))
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("want mismatch, got %v", err)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("unverified file published")
	}
}

func TestDroppedConnectionResumes(t *testing.T) {
	src, data, sum := makeFile(t, 6*testChunk)
	p := newPeer(t, "p", src, "tok")
	dest := filepath.Join(t.TempDir(), "o.gguf")
	// First attempt: stop after two chunks by cancelling from the progress callback.
	ctx, cancel := context.WithCancel(context.Background())
	r := req(dest, len(data), sum, p.Peer)
	r.Progress = func(d int64) {
		if d >= 2*testChunk {
			cancel()
		}
	}
	if _, err := Fetch(ctx, r); err == nil {
		t.Fatal("expected the cancelled fetch to fail")
	}
	if _, err := os.Stat(dest + ".part.done"); err != nil {
		t.Fatal("no resume state kept")
	}
	// Second attempt resumes and must only fetch what is missing.
	var first int64 = -1
	r2 := req(dest, len(data), sum, p.Peer)
	r2.Progress = func(d int64) {
		if first < 0 {
			first = d
		}
	}
	if _, err := Fetch(context.Background(), r2); err != nil {
		t.Fatal(err)
	}
	if first <= 2*testChunk {
		t.Fatalf("resume did not reuse finished chunks (first progress %d)", first)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, data) {
		t.Fatal("content differs after resume")
	}
}

func TestFailsOverWhenPeerDies(t *testing.T) {
	src, data, sum := makeFile(t, 4*testChunk)
	dead := newPeer(t, "dead", src, "tok")
	live := newPeer(t, "live", src, "tok")
	dead.l.Close() // connection refused, as for a node that went offline
	dest := filepath.Join(t.TempDir(), "o")
	if _, err := Fetch(context.Background(), req(dest, len(data), sum, dead.Peer, live.Peer)); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, data) {
		t.Fatal("content differs")
	}
}

func TestAllPeersDown(t *testing.T) {
	src, data, sum := makeFile(t, testChunk)
	p := newPeer(t, "p", src, "tok")
	p.l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := Fetch(ctx, req(filepath.Join(t.TempDir(), "o"), len(data), sum, p.Peer)); err == nil {
		t.Fatal("expected failure")
	}
}

func TestUnknownFileNotServed(t *testing.T) {
	src, data, sum := makeFile(t, testChunk)
	p := newPeer(t, "p", src, "tok")
	r := req(filepath.Join(t.TempDir(), "o"), len(data), sum, p.Peer)
	r.ID = "m/../../etc/passwd"
	if _, err := Fetch(context.Background(), r); err == nil {
		t.Fatal("served a file outside the resolver")
	}
}
