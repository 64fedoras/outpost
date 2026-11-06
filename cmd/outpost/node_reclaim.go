package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// modelsDiskFree is the free space on the models disk; tests replace it.
var modelsDiskFree = diskFree

// reclaimHeadroomMin is the least spare room left after a cleanup.
var reclaimHeadroomMin int64 = 1 << 30

const lastUsedFile = ".lastused"

// touchModel records that a model was just loaded or stored, so automatic
// cleanup can remove the least recently used first.
func (n *Node) touchModel(name string) {
	if !validName(name) {
		return
	}
	p := filepath.Join(n.modelsDir(), name, lastUsedFile)
	now := time.Now()
	if err := os.WriteFile(p, nil, 0o644); err == nil {
		_ = os.Chtimes(p, now, now)
	}
}

// modelLastUsed is the model's last-use stamp, falling back to its manifest's
// age for models stored before the stamp existed.
func (n *Node) modelLastUsed(name string) time.Time {
	dir := filepath.Join(n.modelsDir(), name)
	if st, err := os.Stat(filepath.Join(dir, lastUsedFile)); err == nil {
		return st.ModTime()
	}
	if st, err := os.Stat(filepath.Join(dir, "manifest.json")); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}

// diskShort is the standard "not enough disk space" error.
func diskShort(need int64, free uint64) error {
	return fmt.Errorf("not enough disk space: need %s, have %s", humanBytes(need), humanBytes(int64(free)))
}

// makeRoom checks that need more bytes fit on the models disk. When they do
// not and rp allows it, it deletes the least recently used stored models that
// the controller can copy back (rp.Names), never a protected one and never
// target itself, until the new model fits with some headroom (the larger of
// 1 GiB and 5%). If deleting every eligible model would still not make room it
// deletes nothing and returns the error. It returns nil when need fits.
func (n *Node) makeRoom(need int64, target string, rp *Reclaim) error {
	free := modelsDiskFree(n.modelsDir())
	if free == 0 || need <= 0 || uint64(need) <= free {
		return nil // fits, or the free space is unknown
	}
	short := diskShort(need, free)
	if rp == nil || !rp.Enabled || len(rp.Names)+len(rp.Direct) == 0 {
		return short
	}
	prot := n.protectedModels()
	type cand struct {
		name   string
		size   int64
		used   time.Time
		direct bool
	}
	var cands []cand
	var reclaimable int64
	seen := map[string]bool{}
	collect := func(names []string, direct bool) {
		for _, name := range names {
			if seen[name] || !validName(name) || name == target || prot[name] {
				continue
			}
			seen[name] = true
			dir := filepath.Join(n.modelsDir(), name)
			if st, err := os.Lstat(dir); err != nil || !st.IsDir() {
				continue
			}
			if _, err := n.modelManifest(name); err != nil {
				continue // incomplete: not a copy anyone can vouch for
			}
			size := dirBytes(dir)
			cands = append(cands, cand{name, size, n.modelLastUsed(name), direct})
			reclaimable += size
		}
	}
	collect(rp.Names, false)
	collect(rp.Direct, true)
	if uint64(need) > free+uint64(reclaimable) {
		return fmt.Errorf("%w; removing every stored model that can be restored (%s) would still not make room, so nothing was deleted",
			short, humanBytes(reclaimable))
	}
	// controller-held copies first, then Hugging Face-only ones; least recently used within each
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].direct != cands[j].direct {
			return !cands[i].direct
		}
		if !cands[i].used.Equal(cands[j].used) {
			return cands[i].used.Before(cands[j].used)
		}
		return cands[i].name < cands[j].name
	})
	headroom := need / 20
	if headroom < reclaimHeadroomMin {
		headroom = reclaimHeadroomMin
	}
	goal := uint64(need + headroom)
	var removed []string
	var freed int64
	for _, c := range cands {
		if modelsDiskFree(n.modelsDir()) >= goal {
			break
		}
		if err := os.RemoveAll(filepath.Join(n.modelsDir(), c.name)); err != nil {
			log.Printf("auto cleanup: could not remove %s: %v", c.name, err)
			break
		}
		if c.direct {
			removed = append(removed, c.name+" (not on controller, can be re-downloaded)")
		} else {
			removed = append(removed, c.name)
		}
		freed += c.size
	}
	if len(removed) > 0 {
		note := fmt.Sprintf("Freed %s by removing %s", humanBytes(freed), strings.Join(removed, ", "))
		log.Printf("model %s needs %s: %s", target, humanBytes(need), note)
		n.mu.Lock()
		n.freedNote, n.freedAt = note, time.Now().Unix()
		n.mu.Unlock()
		n.resendHello()
	}
	if free = modelsDiskFree(n.modelsDir()); uint64(need) > free {
		return diskShort(need, free)
	}
	return nil
}
