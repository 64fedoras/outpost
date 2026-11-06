package main

// llama.cpp runtime install and archive helpers. Split out of node_llama.go.

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ---------- llama.cpp runtime ----------

func (n *Node) installRuntime(ri RuntimeInstall) {
	op := "runtime"
	fail := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		log.Printf("runtime %s %s: %s", ri.Build, ri.Variant, msg)
		n.opStatus(op, ri.Variant, "failed", 0, ri.Bytes, msg)
	}
	if !validName(ri.Build) || !validName(ri.Variant) || strings.ContainsAny(ri.Archive, `/\`) {
		fail("bad runtime name")
		return
	}
	if cur := n.currentRuntime(); cur != nil && cur.Build == ri.Build && cur.Variant == ri.Variant {
		n.opStatus(op, ri.Variant, "done", ri.Bytes, ri.Bytes, "")
		return
	}
	if err := os.MkdirAll(n.llamaDir(), 0o755); err != nil {
		fail("%v", err)
		return
	}
	archive := filepath.Join(n.llamaDir(), ri.Archive)
	n.opStatus(op, ri.Variant, "downloading", 0, ri.Bytes, "")
	err := n.fetchFile("/v1/runtime/"+ri.Variant, archive, ri.SHA256, ri.Bytes, func(d int64) {
		n.opStatus(op, ri.Variant, "downloading", d, ri.Bytes, "")
	})
	if err != nil {
		fail("%s", withDefenderHint("download: "+err.Error(), n.dataDir))
		return
	}
	n.opStatus(op, ri.Variant, "installing", ri.Bytes, ri.Bytes, "")
	// The running llama-server lives in the old build's folder; stop it before
	// replacing that folder (the controller loads the model again afterwards).
	n.llama.stop()
	prev := ""
	if cur := n.currentRuntime(); cur != nil {
		prev = filepath.Join(n.llamaDir(), cur.Build+"-"+cur.Variant)
	}
	dest := filepath.Join(n.llamaDir(), ri.Build+"-"+ri.Variant)
	os.RemoveAll(dest)
	if strings.HasSuffix(ri.Archive, ".zip") {
		err = extractZip(archive, dest)
	} else {
		err = extractTarGz(archive, dest)
	}
	os.Remove(archive)
	if err != nil {
		os.RemoveAll(dest)
		fail("extract: %v", err)
		return
	}
	server := findFile(dest, "llama-server"+exeSuffix(runtime.GOOS))
	if server == "" {
		os.RemoveAll(dest)
		fail("llama-server not found in the archive")
		return
	}
	// Self-check: the binary must start here. Libraries Outpost carries
	// (bundledLibs) are dropped next to llama-server when the system lacks them.
	if msg := checkLlamaRuns(server); msg != "" {
		os.RemoveAll(dest)
		fail("%s", msg)
		return
	}
	rec := runtimeRecord{RuntimeInfo: RuntimeInfo{Build: ri.Build, Variant: ri.Variant}, ServerPath: server}
	b, _ := json.MarshalIndent(rec, "", "  ")
	if err := writeFileAtomic(filepath.Join(n.llamaDir(), "current.json"), b, 0o644); err != nil {
		fail("%v", err)
		return
	}
	pruneLlamaBuilds(n.llamaDir(), dest, prev)
	log.Printf("llama.cpp %s (%s) installed at %s", ri.Build, ri.Variant, server)
	n.opStatus(op, ri.Variant, "done", ri.Bytes, ri.Bytes, "")
	n.resendHello()
}

func validName(s string) bool {
	if s == "" || len(s) > 120 || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if !(r == '_' || r == '-' || r == '.' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func lastLines(s string, k int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > k {
		lines = lines[len(lines)-k:]
	}
	return strings.Join(lines, " | ")
}

func findFile(root, name string) string {
	var found string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == name && found == "" {
			found = p
		}
		return nil
	})
	return found
}

func safeArchivePath(dest, name string) (string, error) {
	p := filepath.Join(dest, filepath.FromSlash(name))
	if r, err := filepath.Rel(dest, p); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the target directory", name)
	}
	return p, nil
}

func extractTarGz(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		p, err := safeArchivePath(dest, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(h.Mode)&0o755|0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		case tar.TypeSymlink:
			// Shared-library version links (libllama.so -> libllama.so.0). Only
			// relative targets inside the archive are allowed.
			if filepath.IsAbs(h.Linkname) {
				return fmt.Errorf("absolute symlink %q in archive", h.Linkname)
			}
			if _, err := safeArchivePath(dest, filepath.Join(filepath.Dir(h.Name), h.Linkname)); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			os.Remove(p)
			if err := os.Symlink(h.Linkname, p); err != nil {
				return err
			}
		}
	}
}

func extractZip(src, dest string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, zf := range zr.File {
		p, err := safeArchivePath(dest, zf.Name)
		if err != nil {
			return err
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func llamaEnv(dir string) []string {
	env := os.Environ()
	switch runtime.GOOS {
	case "linux":
		env = append(env, "LD_LIBRARY_PATH="+dir+string(os.PathListSeparator)+os.Getenv("LD_LIBRARY_PATH"))
	case "darwin":
		env = append(env, "DYLD_LIBRARY_PATH="+dir)
	}
	return env
}
