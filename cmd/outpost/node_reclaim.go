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

// reclaimCand is a stored model that may be deleted to make room.
type reclaimCand struct {
	name   string
	size   int64
	used   time.Time
	direct bool
}

// reclaimCands lists the stored models in names and direct that may be
// deleted for target: complete, not protected (loaded, set to reload, being
// copied) and not target itself. Controller-held copies come first, then
// Hugging Face-only ones; least recently used first within each. The second
// result is their total size.
func (n *Node) reclaimCands(target string, names, direct []string) ([]reclaimCand, int64) {
	prot := n.protectedModels()
	var cands []reclaimCand
	var total int64
	seen := map[string]bool{}
	collect := func(list []string, isDirect bool) {
		for _, name := range list {
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
			cands = append(cands, reclaimCand{name, size, n.modelLastUsed(name), isDirect})
			total += size
		}
	}
	collect(names, false)
	collect(direct, true)
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].direct != cands[j].direct {
			return !cands[i].direct
		}
		if !cands[i].used.Equal(cands[j].used) {
			return cands[i].used.Before(cands[j].used)
		}
		return cands[i].name < cands[j].name
	})
	return cands, total
}

// removeUntil deletes cands in order until enough reports true, and notes what
// it removed on the node card. why says what the room was for.
func (n *Node) removeUntil(cands []reclaimCand, enough func(freed int64) bool, target string, need int64, why string) {
	var removed []string
	var freed int64
	for _, c := range cands {
		if enough(freed) {
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
	if len(removed) == 0 {
		return
	}
	note := fmt.Sprintf("Freed %s by removing %s%s", humanBytes(freed), strings.Join(removed, ", "), why)
	log.Printf("model %s needs %s: %s", target, humanBytes(need), note)
	n.mu.Lock()
	n.freedNote, n.freedAt = note, time.Now().Unix()
	n.mu.Unlock()
	n.resendHello()
}

// makeRoom checks that need more bytes fit, first under this node's storage
// limit (makeRoomUnderLimit), then on the models disk. When they do not fit on
// the disk and rp allows it, it deletes the least recently used stored models
// that the controller can copy back (rp.Names), never a protected one and never
// target itself, until the new model fits with some headroom (the larger of
// 1 GiB and 5%). If deleting every eligible model would still not make room it
// deletes nothing and returns the error. It returns nil when need fits.
func (n *Node) makeRoom(need int64, target string, rp *Reclaim) error {
	if err := n.makeRoomUnderLimit(need, target, rp); err != nil {
		return err
	}
	pending := uint64(n.pendingBytes(target)) // room other copies in progress will still take
	freeNow := func() uint64 {
		f := modelsDiskFree(n.modelsDir())
		if f == 0 {
			return 0 // unknown
		}
		if f <= pending {
			return 1 // known, and none of it is left for this copy
		}
		return f - pending
	}
	free := freeNow()
	if free == 0 || need <= 0 || uint64(need) <= free {
		return nil // fits, or the free space is unknown
	}
	short := diskShort(need, free)
	if rp == nil || !rp.Enabled || len(rp.Names)+len(rp.Direct) == 0 {
		return short
	}
	cands, reclaimable := n.reclaimCands(target, rp.Names, rp.Direct)
	if uint64(need) > free+uint64(reclaimable) {
		return fmt.Errorf("%w; removing every stored model that can be restored (%s) would still not make room, so nothing was deleted",
			short, humanBytes(reclaimable))
	}
	headroom := need / 20
	if headroom < reclaimHeadroomMin {
		headroom = reclaimHeadroomMin
	}
	goal := uint64(need + headroom)
	n.removeUntil(cands, func(int64) bool { return freeNow() >= goal }, target, need, "")
	if free = freeNow(); uint64(need) > free {
		return diskShort(need, free)
	}
	return nil
}
