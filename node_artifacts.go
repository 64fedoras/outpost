package main

// Moving job files between the controller and a node. Small files travel
// inline in the job messages. Files over inlineMax move as blobs: inputs are
// downloaded by the node from the controller (fetchFile, SHA-256 checked,
// resumable), and collected outputs stay in the job's workspace until the
// controller fetches them from the node's peer server in hash-checked chunks
// (package transfer), then closes the workspace.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// maxJobFiles caps the blob bytes moved in either direction for one job.
const maxJobFiles = 2 << 30

func (n *Node) writeInputs(dir, jobID string, inputs []FileEntry) *ErrInfo {
	var blobs int64
	for _, f := range inputs {
		p, err := safeJoin(dir, f.Path)
		if err != nil {
			return err.(*ErrInfo)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return errf("disk_full", true, "%v", err)
		}
		if f.Blob {
			if blobs += f.Bytes; blobs > maxJobFiles {
				return errf("too_large", false, "input files exceed %d bytes", int64(maxJobFiles))
			}
			if free := diskFree(dir); free > 0 && uint64(f.Bytes) > free {
				return errf("disk_full", true, "not enough disk space for %s", f.Path)
			}
			u := "/v1/jobs/" + url.PathEscape(jobID) + "/inputs?path=" + url.QueryEscape(f.Path)
			if err := n.fetchFile(u, p, f.SHA256, f.Bytes, nil); err != nil {
				return errf("fetch_failed", true, "file %s: %v", f.Path, err)
			}
			continue
		}
		data, err := base64.StdEncoding.DecodeString(f.InlineB64)
		if err != nil {
			return errf("bad_request", false, "file %s: bad base64", f.Path)
		}
		sum := sha256.Sum256(data)
		if f.SHA256 != "" && !strings.EqualFold(hex.EncodeToString(sum[:]), f.SHA256) {
			return errf("checksum_mismatch", true, "file %s failed sha256 check", f.Path)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return errf("disk_full", true, "%v", err)
		}
	}
	return nil
}

// collectFiles gathers the files matching patterns. Small ones are returned
// inline; larger ones are hashed and registered for the controller to fetch.
func (n *Node) collectFiles(dir, jobID string, patterns []string) ([]FileEntry, *ErrInfo) {
	out := []FileEntry{}
	seen := map[string]bool{}
	var inline, blobs int64
	for _, pat := range patterns {
		if _, err := safeJoin(dir, pat); err != nil {
			return out, err.(*ErrInfo)
		}
		matches, _ := filepath.Glob(filepath.Join(dir, filepath.FromSlash(pat)))
		for _, m := range matches {
			info, err := os.Lstat(m)
			if err != nil || !info.Mode().IsRegular() || seen[m] {
				continue
			}
			rel, _ := filepath.Rel(dir, m)
			rel = filepath.ToSlash(rel)
			if info.Size() > inlineMax {
				if n.peerInfo() == nil {
					return out, errf("collect_too_large", false, "%s is %d bytes and this node is not sharing files (peer server is off)", rel, info.Size())
				}
				if blobs += info.Size(); blobs > maxJobFiles {
					return out, errf("collect_too_large", false, "collected files exceed %d bytes", int64(maxJobFiles))
				}
				sum, err := hashFile(m)
				if err != nil {
					return out, errf("collect_failed", true, "%s: %v", rel, err)
				}
				seen[m] = true
				n.mu.Lock()
				if n.artifacts == nil {
					n.artifacts = map[string]string{}
				}
				n.artifacts[jobID+"/"+rel] = m
				n.mu.Unlock()
				out = append(out, FileEntry{Path: rel, SHA256: sum, Bytes: info.Size(), Blob: true})
				continue
			}
			if inline += info.Size(); inline > collectCap {
				return out, errf("collect_too_large", false, "small collected files exceed %d bytes", collectCap)
			}
			data, err := os.ReadFile(m)
			if err != nil {
				continue
			}
			sum := sha256.Sum256(data)
			seen[m] = true
			out = append(out, FileEntry{Path: rel, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data)), InlineB64: base64.StdEncoding.EncodeToString(data)})
		}
	}
	return out, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// dropArtifacts forgets the files registered for a job.
func (n *Node) dropArtifacts(jobID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for k := range n.artifacts {
		if strings.HasPrefix(k, jobID+"/") {
			delete(n.artifacts, k)
		}
	}
}

// resolveArtifact maps "art/<job>/<path>" to a collected file.
func (n *Node) resolveArtifact(id string) (string, bool) {
	key, ok := strings.CutPrefix(id, "art/")
	if !ok {
		return "", false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	p, ok := n.artifacts[key]
	return p, ok
}
