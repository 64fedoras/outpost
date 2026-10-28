package main

// Node-to-node model sharing. Every node serves the model files it holds on a
// second TLS port (default 8444) with its own self-signed certificate. The
// controller tells a downloading node which peers hold a model, their
// certificate fingerprints, a token and the expected SHA-256; the node checks
// every file against that hash before it is stored (see package transfer).

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"outpost/transfer"
)

const peerPort = transfer.DefaultPort

type peerState struct {
	mu   sync.Mutex
	srv  *transfer.Server
	info *PeerInfo
}

func (n *Node) peerInfo() *PeerInfo {
	n.peer.mu.Lock()
	defer n.peer.mu.Unlock()
	return n.peer.info
}

// startPeerServer is best effort: without it the node can still download from
// peers, it just does not share. OUTPOST_PEER_PORT overrides the port (tests).
func (n *Node) startPeerServer() {
	port := peerPort
	if v := os.Getenv("OUTPOST_PEER_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &port)
	}
	host, _ := os.Hostname()
	cert, fp, err := transfer.LoadOrCreateCert(n.dataDir, host)
	if err != nil {
		log.Printf("peer sharing disabled: %v", err)
		return
	}
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Printf("peer sharing disabled: %v", err)
		return
	}
	srv := &transfer.Server{Cert: cert, Resolve: n.resolvePeerFile}
	n.peer.mu.Lock()
	n.peer.srv, n.peer.info = srv, &PeerInfo{Port: l.Addr().(*net.TCPAddr).Port, Fingerprint: fp}
	n.peer.mu.Unlock()
	log.Printf("sharing models with other nodes on port %d", port)
	go func() { log.Printf("peer server stopped: %v", srv.Serve(l)) }()
}

func (n *Node) peerGrant(pg PeerGrant) {
	n.peer.mu.Lock()
	srv := n.peer.srv
	n.peer.mu.Unlock()
	if srv != nil && pg.Token != "" {
		srv.Grant(pg.Token, time.Duration(pg.TTLSeconds)*time.Second)
	}
}

// resolvePeerFile maps "<model>/<file>" to a file listed in that model's
// manifest, so nothing else on the disk can be requested.
func (n *Node) resolvePeerFile(id string) (string, bool) {
	if strings.HasPrefix(id, "art/") {
		return n.resolveArtifact(id)
	}
	name, file, ok := strings.Cut(id, "/")
	if !ok || !validName(name) || !validName(file) {
		return "", false
	}
	m, err := n.modelManifest(name)
	if err != nil {
		return "", false
	}
	for _, f := range m.Files {
		if f.Path == file {
			return filepath.Join(n.modelsDir(), name, file), true
		}
	}
	return "", false
}

// fetchFromPeers copies one model file from the peers into dst and returns
// only once its whole-file SHA-256 matched f.SHA256.
func (n *Node) fetchFromPeers(mp ModelPush, f ModelFile, dst string, progress func(done int64)) ([]string, error) {
	if f.SHA256 == "" {
		return nil, errors.New("no trusted hash for this file")
	}
	peers := make([]transfer.Peer, len(mp.Peers))
	for i, p := range mp.Peers {
		peers[i] = transfer.Peer(p)
	}
	res, err := transfer.Fetch(context.Background(), transfer.Request{
		ID: mp.Name + "/" + f.Path, Dest: dst, Size: f.Bytes, SHA256: f.SHA256,
		Peers: peers, Token: mp.PeerToken, Progress: progress,
	})
	return res.Bad, err
}
