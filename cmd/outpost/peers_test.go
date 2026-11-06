package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func peerNode(models ...string) *NodeRec {
	h := &Hello{Peer: &PeerInfo{Port: 8444, Fingerprint: "ab"}}
	for _, m := range models {
		h.Models = append(h.Models, ModelInfo{Name: m})
	}
	return &NodeRec{Hello: h}
}

func TestPickPeers(t *testing.T) {
	c := &Controller{
		nodes: map[string]*NodeRec{
			"self": peerNode("m"), "idle": peerNode("m"), "busy": peerNode("m"),
			"nomodel": peerNode("other"), "offline": peerNode("m"), "bad": peerNode("m"),
			"nopeer": {Hello: &Hello{Models: []ModelInfo{{Name: "m"}}}},
		},
		conns:     map[string]*nodeConn{},
		pipelines: map[string]*pipeline{},
		peerBad:   map[string]time.Time{"bad": time.Now().Add(time.Hour)},
	}
	for _, id := range []string{"self", "idle", "busy", "nomodel", "bad", "nopeer"} {
		c.conns[id] = &nodeConn{addr: "10.0.0.5:5555"}
	}
	c.pipelines["busy"] = &pipeline{}
	got := c.pickPeers("self", "m")
	if len(got) != 2 || got[0].Name != "idle" || got[1].Name != "busy" {
		t.Fatalf("peers: %+v", got)
	}
	if got[0].Addr != "10.0.0.5:8444" {
		t.Fatalf("addr %s", got[0].Addr)
	}
	c.peerBad["bad"] = time.Now().Add(-time.Second) // skip period over
	if len(c.pickPeers("self", "m")) != 3 {
		t.Fatal("expired skip should allow the node again")
	}
}

func TestCopyFromPeersNeedsHashes(t *testing.T) {
	c := &Controller{}
	m := &ModelRec{Name: "m", Files: []ModelFile{{Path: "a.gguf", Bytes: 5}}} // no SHA-256: never trust peers
	if err := c.copyFromPeers(context.Background(), "x", m); err != nil {
		t.Fatal(err)
	}
}

// A node only serves files listed in a model's manifest, by validated name.
func TestResolvePeerFile(t *testing.T) {
	dir := t.TempDir()
	n := &Node{dataDir: dir}
	md := filepath.Join(n.modelsDir(), "m")
	os.MkdirAll(md, 0o755)
	os.WriteFile(filepath.Join(md, "a.gguf"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(md, "manifest.json"), []byte(`{"name":"m","files":[{"path":"a.gguf","sha256":"","bytes":1}]}`), 0o644)
	os.WriteFile(filepath.Join(md, "secret.txt"), []byte("s"), 0o644)
	if p, ok := n.resolvePeerFile("m/a.gguf"); !ok || p != filepath.Join(md, "a.gguf") {
		t.Fatalf("got %q %v", p, ok)
	}
	for _, id := range []string{"m/manifest.json", "m/secret.txt", "m/../../x", "../m/a.gguf", "m", "nope/a.gguf", "m/a.gguf.part"} {
		if _, ok := n.resolvePeerFile(id); ok {
			t.Fatalf("%q should not be served", id)
		}
	}
}

// End to end between two Node structs: node A serves, node B pushes from it,
// and a tampered copy on A is rejected and reported by name.
func TestPushModelFromPeer(t *testing.T) {
	data := make([]byte, 3<<20+17)
	for i := range data {
		data[i] = byte(i * 7)
	}
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])

	serve := func(content []byte) (*Node, PeerSource) {
		a := newTestPeerNode(t)
		md := filepath.Join(a.modelsDir(), "m")
		os.MkdirAll(md, 0o755)
		os.WriteFile(filepath.Join(md, "a.gguf"), content, 0o644)
		os.WriteFile(filepath.Join(md, "manifest.json"), []byte(`{"name":"m","files":[{"path":"a.gguf","bytes":1}]}`), 0o644)
		t.Setenv("OUTPOST_PEER_PORT", "0")
		a.startPeerServer()
		info := a.peerInfo()
		if info == nil {
			t.Fatal("peer server did not start")
		}
		a.peerGrant(PeerGrant{Token: "tok", TTLSeconds: 60})
		return a, PeerSource{Name: "A", Addr: "127.0.0.1:" + strconv.Itoa(info.Port), Fingerprint: info.Fingerprint}
	}

	_, good := serve(data)
	b := newTestPeerNode(t)
	files := []ModelFile{{Path: "a.gguf", SHA256: hexSum, Bytes: int64(len(data))}}
	b.pushModel(ModelPush{Name: "m", Files: files, Peers: []PeerSource{good}, PeerToken: "tok"})
	got, err := os.ReadFile(filepath.Join(b.modelsDir(), "m", "a.gguf"))
	if err != nil || string(got) != string(data) {
		t.Fatalf("model not copied: %v", err)
	}

	bad := append([]byte(nil), data...)
	bad[1000] ^= 1
	_, evil := serve(bad)
	evil.Name = "evil"
	c := newTestPeerNode(t)
	c.pushModel(ModelPush{Name: "m", Files: files, Peers: []PeerSource{evil}, PeerToken: "tok"})
	if _, err := os.Stat(filepath.Join(c.modelsDir(), "m", "a.gguf")); err == nil {
		t.Fatal("tampered file was stored")
	}
	if _, err := os.Stat(filepath.Join(c.modelsDir(), "m", "manifest.json")); err == nil {
		t.Fatal("tampered model was registered")
	}
}

func newTestPeerNode(t *testing.T) *Node {
	n := &Node{dataDir: t.TempDir(), workRoot: t.TempDir(), stats: newStatSampler()}
	n.llama = newLlamaMgr(n)
	n.rpc = newRPCMgr(n)
	return n
}
