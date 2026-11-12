package main

// Build caches for exec jobs that keep no job data on the node.
//
// Every task gets its own cache folder, <data>/cache/<task id>, used as the Go
// build and module cache and the npm, pip and Cargo cache. It is deleted when
// the task ends (the controller sends "cache.close"), after cacheScopeTTL
// without use, or at node start. A job sent without a task gets a cache of its
// own that goes when the job ends.
//
// Before a cache is deleted, the node copies verified third-party packages
// from it into a clean package store, <data>/package-store, so later tasks do
// not download them again:
//   - Go modules whose go.mod and zip hashes match the public checksum
//     database (sum.golang.org). The store is served to jobs as a read-only
//     file:// GOPROXY in front of the normal proxy, and the go command checks
//     each module again against go.sum and the checksum database when it uses it.
//   - npm tarballs from npm's content-addressed cache whose hash matches their
//     file name. A new task's npm cache starts as a copy of these.
// Nothing built from the task's code is copied: not Go's build cache, not pip
// or Cargo, not anything else. The store is capped at pkgStoreMax, oldest
// files first, and can be cleared from the node card.

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"outpost/internal/sandbox"
)

const (
	cacheScopeTTL = time.Hour
	pkgStoreMax   = int64(5) << 30
)

// sumDBURL is the Go checksum database the store trusts (tests point it at a stub).
var sumDBURL = "https://sum.golang.org"

// CacheClose ends a task's build cache on a node.
type CacheClose struct {
	Scope string `json:"scope"`
}

type cacheScope struct {
	active  int
	last    time.Time
	closing bool          // the task ended while a job still ran; retire on release
	ready   chan struct{} // closed once the folder is set up
}

func (n *Node) cacheRoot() string { return filepath.Join(n.dataDir, "cache") }
func (n *Node) pkgStoreDir() string {
	p := filepath.Join(n.dataDir, "package-store")
	if abs, err := filepath.Abs(p); err == nil {
		p = abs // GOPROXY and the sandbox rules need an absolute path
	}
	return p
}

// acquireCache returns the build cache folder for a job and a func to call
// when the job ends.
func (n *Node) acquireCache(scope, jobID string) (string, func()) {
	oneShot := false
	if scope == "" || !validWSID(scope) {
		scope, oneShot = "job_"+jobID, true
	}
	dir := filepath.Join(n.cacheRoot(), scope)
	n.mu.Lock()
	if n.scopes == nil {
		n.scopes = map[string]*cacheScope{}
	}
	s := n.scopes[scope]
	fresh := s == nil
	if fresh {
		s = &cacheScope{ready: make(chan struct{})}
		n.scopes[scope] = s
	}
	s.active++
	s.last = time.Now()
	s.closing = false
	n.mu.Unlock()
	if fresh {
		_ = os.MkdirAll(dir, 0o700)
		n.seedCache(dir)
		close(s.ready)
	} else {
		<-s.ready
	}
	return dir, func() {
		n.mu.Lock()
		s.active--
		s.last = time.Now()
		done := s.active == 0 && (oneShot || s.closing)
		if done && n.scopes[scope] == s {
			delete(n.scopes, scope)
		}
		n.mu.Unlock()
		if done {
			go n.retireCache(dir)
		}
	}
}

// closeCacheScope ends a task's cache: now, or when its last job ends.
func (n *Node) closeCacheScope(scope string) {
	if !validWSID(scope) {
		return
	}
	n.mu.Lock()
	s := n.scopes[scope]
	if s != nil && s.active > 0 {
		s.closing = true
		n.mu.Unlock()
		return
	}
	delete(n.scopes, scope)
	n.mu.Unlock()
	dir := filepath.Join(n.cacheRoot(), scope)
	if _, err := os.Stat(dir); err == nil {
		go n.retireCache(dir)
	}
}

// expireCacheScopes retires caches no job used for cacheScopeTTL (a task whose
// end the node never heard about).
func (n *Node) expireCacheScopes() {
	n.mu.Lock()
	var old []string
	for id, s := range n.scopes {
		if s.active == 0 && time.Since(s.last) > cacheScopeTTL {
			old = append(old, id)
			delete(n.scopes, id)
		}
	}
	n.mu.Unlock()
	for _, id := range old {
		n.retireCache(filepath.Join(n.cacheRoot(), id))
	}
}

// sweepCaches runs at node start: it removes the shared cache older versions
// kept and retires every task cache left from before the restart.
func (n *Node) sweepCaches() {
	if _, err := os.Stat(n.sandboxCacheDir()); err == nil {
		removeTree(n.sandboxCacheDir())
		log.Printf("removed the old shared build cache %s", n.sandboxCacheDir())
	}
	entries, _ := os.ReadDir(n.cacheRoot())
	for _, e := range entries {
		n.retireCache(filepath.Join(n.cacheRoot(), e.Name()))
	}
	n.storeSize()
}

// wipeCaches deletes every cache and the package store (node removed).
func (n *Node) wipeCaches() {
	n.storeMu.Lock()
	defer n.storeMu.Unlock()
	removeTree(n.cacheRoot())
	removeTree(n.pkgStoreDir())
	removeTree(n.sandboxCacheDir())
}

// retireCache copies the verified packages out of a task cache, then deletes it.
func (n *Node) retireCache(dir string) {
	n.storeMu.Lock()
	defer n.storeMu.Unlock()
	goN := promoteGoModules(filepath.Join(dir, "gomod"), filepath.Join(n.pkgStoreDir(), "go"))
	npmN := promoteNPM(filepath.Join(dir, "npm", "_cacache"), filepath.Join(n.pkgStoreDir(), "npm", "_cacache"))
	if goN+npmN > 0 {
		capStore(n.pkgStoreDir(), pkgStoreMax)
		log.Printf("package store: added %d Go and %d npm files from %s", goN, npmN, filepath.Base(dir))
	}
	removeTree(dir)
	n.setStoreBytes(dirSize(n.pkgStoreDir()))
}

// clearPackageStore empties the store ("Clear" on the node card).
func (n *Node) clearPackageStore() {
	n.storeMu.Lock()
	removeTree(n.pkgStoreDir())
	n.storeMu.Unlock()
	n.setStoreBytes(0)
	log.Printf("package store cleared from the web UI")
}

func (n *Node) setStoreBytes(b int64) {
	n.mu.Lock()
	n.storeBytes = b
	n.mu.Unlock()
}

func (n *Node) storeSize() {
	n.storeMu.Lock()
	b := dirSize(n.pkgStoreDir())
	n.storeMu.Unlock()
	n.setStoreBytes(b)
}

// seedCache gives a new task cache the store's npm packages (a copy, so a job
// cannot change the store). It takes no lock, so a job never waits for a
// promotion that is checking hashes; the store only gains files by rename,
// and a file the size cap removes mid-copy is just skipped.
func (n *Node) seedCache(dir string) {
	_ = os.MkdirAll(filepath.Join(n.pkgStoreDir(), "go"), 0o755)
	src := filepath.Join(n.pkgStoreDir(), "npm", "_cacache", "content-v2")
	if _, err := os.Stat(src); err == nil {
		if err := copyTree(src, filepath.Join(dir, "npm", "_cacache", "content-v2")); err != nil {
			log.Printf("package store: seeding npm cache: %v", err)
		}
	}
}

// cacheEnv is the environment that points a job without the sandbox at its
// task cache and the store. (The sandbox sets the same through its policy.)
func (n *Node) cacheEnv(dir string, network bool) []string {
	env := []string{
		"GOCACHE=" + filepath.Join(dir, "go-build"),
		"GOMODCACHE=" + filepath.Join(dir, "gomod"),
		"npm_config_cache=" + filepath.Join(dir, "npm"),
		"PIP_CACHE_DIR=" + filepath.Join(dir, "pip"),
	}
	if px := sandbox.GoProxy(n.pkgStoreDir(), os.Getenv("GOPROXY"), network); px != "" {
		env = append(env, "GOPROXY="+px)
	}
	return env
}

// ---- Go modules ----

// promoteGoModules copies modules from a task's module cache into the store's
// proxy layout when the checksum database vouches for them. It returns the
// number of files added.
func promoteGoModules(modCache, store string) int {
	if v := os.Getenv("GOSUMDB"); v != "" && v != "sum.golang.org" {
		return 0 // a custom or disabled checksum database: nothing to check against
	}
	dl := filepath.Join(modCache, "cache", "download")
	if _, err := os.Stat(dl); err != nil {
		return 0
	}
	private := os.Getenv("GONOSUMDB")
	if private == "" {
		private = os.Getenv("GOPRIVATE")
	}
	added := 0
	offline := false
	_ = filepath.WalkDir(dl, func(p string, d fs.DirEntry, err error) error {
		if err != nil || offline || !d.IsDir() || d.Name() != "@v" {
			return nil
		}
		rel, _ := filepath.Rel(dl, filepath.Dir(p))
		escPath := filepath.ToSlash(rel)
		modPath, ok := unescapeModPath(escPath)
		if !ok || matchGlobs(private, modPath) {
			return filepath.SkipDir
		}
		files, _ := os.ReadDir(p)
		for _, f := range files {
			escVer, ok := strings.CutSuffix(f.Name(), ".mod")
			if !ok || f.IsDir() {
				continue
			}
			ver, ok := unescapeModPath(escVer)
			if !ok || strings.ContainsAny(ver, "/\\\n ") {
				continue
			}
			n, err := promoteGoVersion(p, filepath.Join(store, filepath.FromSlash(escPath), "@v"), modPath, escPath, ver, escVer)
			if err != nil {
				log.Printf("package store: %s@%s not kept: %v", modPath, ver, err)
				if errors.Is(err, errSumDBDown) {
					offline = true
					return filepath.SkipAll
				}
			}
			added += n
		}
		return filepath.SkipDir
	})
	if added > 0 {
		rebuildGoLists(store)
	}
	return added
}

var errSumDBDown = errors.New("checksum database unreachable")

func promoteGoVersion(srcV, dstV, modPath, escPath, ver, escVer string) (int, error) {
	modSrc := filepath.Join(srcV, escVer+".mod")
	zipSrc := filepath.Join(srcV, escVer+".zip")
	modDst := filepath.Join(dstV, escVer+".mod")
	zipDst := filepath.Join(dstV, escVer+".zip")
	_, zerr := os.Stat(zipSrc)
	haveZip := zerr == nil
	needMod := !fileExists(modDst)
	needZip := haveZip && !fileExists(zipDst)
	if !needMod && !needZip {
		touch(modDst)
		if haveZip {
			touch(zipDst)
		}
		return 0, nil
	}
	sums, err := sumLookup(escPath, escVer)
	if err != nil {
		return 0, err
	}
	added := 0
	if needMod {
		b, err := os.ReadFile(modSrc)
		if err != nil {
			return 0, err
		}
		if h := hash1Bytes("go.mod", b); h != sums[modPath+" "+ver+"/go.mod"] {
			return 0, fmt.Errorf("go.mod hash %s does not match the checksum database", h)
		}
		if err := os.MkdirAll(dstV, 0o755); err != nil {
			return 0, err
		}
		if err := writeAtomic(modDst, b); err != nil {
			return 0, err
		}
		info, _ := json.Marshal(map[string]string{"Version": ver})
		_ = writeAtomic(filepath.Join(dstV, escVer+".info"), info)
		added++
	}
	if needZip {
		h, err := hashZip(zipSrc)
		if err != nil {
			return added, err
		}
		if h != sums[modPath+" "+ver] {
			return added, fmt.Errorf("zip hash %s does not match the checksum database", h)
		}
		if err := copyFileAtomic(zipSrc, zipDst); err != nil {
			return added, err
		}
		added++
	}
	return added, nil
}

// sumLookup asks the checksum database for a module version's hashes, keyed
// "path version" (the zip) and "path version/go.mod".
func sumLookup(escPath, escVer string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", sumDBURL+"/lookup/"+escPath+"@"+escVer, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errSumDBDown, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("checksum database: HTTP %d", resp.StatusCode)
	}
	sums := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 64<<10))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 3 && strings.HasPrefix(f[2], "h1:") {
			sums[f[0]+" "+f[1]] = f[2]
		}
	}
	return sums, nil
}

// hash1 is the go command's "h1:" hash of a set of files (dirhash.Hash1).
func hash1(names []string, open func(string) (io.ReadCloser, error)) (string, error) {
	names = append([]string(nil), names...)
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		if strings.Contains(name, "\n") {
			return "", errors.New("file name with a newline")
		}
		r, err := open(name)
		if err != nil {
			return "", err
		}
		hf := sha256.New()
		_, err = io.Copy(hf, r)
		r.Close()
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%x  %s\n", hf.Sum(nil), name)
	}
	return "h1:" + base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}

func hash1Bytes(name string, b []byte) string {
	h, _ := hash1([]string{name}, func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(string(b))), nil
	})
	return h
}

// hashZip is dirhash.HashZip: the h1 hash of every file in a module zip.
func hashZip(p string) (string, error) {
	z, err := zip.OpenReader(p)
	if err != nil {
		return "", err
	}
	defer z.Close()
	files := map[string]*zip.File{}
	var names []string
	for _, f := range z.File {
		if _, dup := files[f.Name]; dup {
			return "", errors.New("duplicate file in zip")
		}
		files[f.Name] = f
		names = append(names, f.Name)
	}
	return hash1(names, func(name string) (io.ReadCloser, error) { return files[name].Open() })
}

// unescapeModPath undoes the module cache's case encoding ("!a" is "A").
func unescapeModPath(s string) (string, bool) {
	var b strings.Builder
	bang := false
	for _, r := range s {
		switch {
		case bang:
			if r < 'a' || r > 'z' {
				return "", false
			}
			b.WriteRune(r - 'a' + 'A')
			bang = false
		case r == '!':
			bang = true
		case r >= 'A' && r <= 'Z':
			return "", false
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), !bang && s != ""
}

// matchGlobs reports whether a module path matches GOPRIVATE-style patterns
// (comma separated globs, each matching a leading run of path elements).
func matchGlobs(globs, target string) bool {
	for _, g := range strings.Split(globs, ",") {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		n := strings.Count(g, "/")
		prefix := target
		for i := 0; i < len(target); i++ {
			if target[i] == '/' {
				if n == 0 {
					prefix = target[:i]
					break
				}
				n--
			}
		}
		if n > 0 {
			continue
		}
		if ok, _ := path.Match(g, prefix); ok {
			return true
		}
	}
	return false
}

// rebuildGoLists writes each module's @v/list from the versions it holds and
// drops .info and .zip files whose .mod is gone (after the size cap).
func rebuildGoLists(store string) {
	_ = filepath.WalkDir(store, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || d.Name() != "@v" {
			return nil
		}
		files, _ := os.ReadDir(p)
		have := map[string]bool{}
		for _, f := range files {
			if v, ok := strings.CutSuffix(f.Name(), ".mod"); ok {
				have[v] = true
			}
		}
		var vers []string
		for _, f := range files {
			name := f.Name()
			ext := filepath.Ext(name)
			v := strings.TrimSuffix(name, ext)
			switch ext {
			case ".mod":
				if uv, ok := unescapeModPath(v); ok {
					vers = append(vers, uv)
				}
			case ".info", ".zip":
				if !have[v] {
					_ = os.Remove(filepath.Join(p, name))
				}
			}
		}
		sort.Strings(vers)
		if len(vers) == 0 {
			_ = os.Remove(filepath.Join(p, "list"))
			return filepath.SkipDir
		}
		_ = writeAtomic(filepath.Join(p, "list"), []byte(strings.Join(vers, "\n")+"\n"))
		return filepath.SkipDir
	})
}

// ---- npm ----

// promoteNPM copies tarballs from npm's content-addressed cache whose content
// matches the hash in their path (content-v2/<algo>/<hex digest split 2/2/rest>).
func promoteNPM(cacache, storeCacache string) int {
	root := filepath.Join(cacache, "content-v2")
	if _, err := os.Stat(root); err != nil {
		return 0
	}
	added := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 4 {
			return nil
		}
		var h hash.Hash
		switch parts[0] {
		case "sha512":
			h = sha512.New()
		case "sha1":
			h = sha1.New()
		default:
			return nil
		}
		want := parts[1] + parts[2] + parts[3]
		dst := filepath.Join(storeCacache, "content-v2", filepath.FromSlash(filepath.ToSlash(rel)))
		if fileExists(dst) {
			touch(dst)
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil || hex.EncodeToString(h.Sum(nil)) != want {
			return nil
		}
		if copyFileAtomic(p, dst) == nil {
			added++
		}
		return nil
	})
	return added
}

// ---- store size cap and file helpers ----

// capStore deletes the oldest files until the store is under max (to 90% of it).
func capStore(store string, max int64) {
	type entry struct {
		p    string
		size int64
		mod  time.Time
	}
	var all []entry
	var total int64
	_ = filepath.WalkDir(store, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, e := d.Info(); e == nil {
				all = append(all, entry{p, info.Size(), info.ModTime()})
				total += info.Size()
			}
		}
		return nil
	})
	if total <= max {
		return
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod.Before(all[j].mod) })
	for _, e := range all {
		if total <= max*9/10 {
			break
		}
		if os.Remove(e.p) == nil {
			total -= e.size
		}
	}
	rebuildGoLists(filepath.Join(store, "go"))
}

func touch(p string) { now := time.Now(); _ = os.Chtimes(p, now, now) }

func writeAtomic(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func copyFileAtomic(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// copyTree copies regular files from src to dst (no symlinks), skipping files
// that disappear meanwhile. It returns the first other error.
func copyTree(src, dst string) error {
	var first error
	_ = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() || strings.HasSuffix(p, ".tmp") {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		if err := copyFileAtomic(p, filepath.Join(dst, rel)); err != nil && !errors.Is(err, fs.ErrNotExist) && first == nil {
			first = err
		}
		return nil
	})
	return first
}

// removeTree deletes a folder even when the go command made parts of it
// read-only (the module cache does).
func removeTree(p string) {
	_ = filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
		if err == nil && d.Type()&fs.ModeSymlink == 0 {
			if d.IsDir() {
				_ = os.Chmod(q, 0o700)
			} else {
				_ = os.Chmod(q, 0o600)
			}
		}
		return nil
	})
	if err := os.RemoveAll(p); err != nil {
		log.Printf("could not delete %s: %v", p, err)
	}
}
