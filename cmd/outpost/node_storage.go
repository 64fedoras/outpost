package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// Storage use and the per-node storage limit.
//
// The node adds up the folders it owns under its data directory and reports
// them in each heartbeat (Heartbeat.Storage). Job folders (work/) are left out
// because they are deleted when a job ends. The owner can set a limit per node
// in the web UI (NodeSettings.StorageLimit); a model copy or download that
// would take Outpost past it either removes least recently used models first
// (the same rules as the disk-full cleanup in node_reclaim.go) or is refused.
// Nothing is ever deleted just because the limit was lowered.

// storageEvery is how often the node measures its folders without a reason to.
var storageEvery = 5 * time.Minute

// storageKind maps a top-level folder of the data directory to the group the
// web UI shows it in. Anything not listed counts as "other".
var storageKind = map[string]string{
	"models":        "models",
	"package-store": "packages",
	"cache":         "packages",
	"sandbox-cache": "packages",
	"llama":         "runtimes",
	"sd":            "runtimes",
	"toolchains":    "runtimes",
}

// measureStorage walks the data directory once and returns its use by kind.
func (n *Node) measureStorage() StorageUse {
	var u StorageUse
	ents, _ := os.ReadDir(n.dataDir)
	for _, e := range ents {
		p := filepath.Join(n.dataDir, e.Name())
		if p == n.workRoot || e.Name() == "work" {
			continue
		}
		var b int64
		if e.Type().IsRegular() {
			if info, err := e.Info(); err == nil {
				b = info.Size()
			}
		} else if e.IsDir() {
			b = dirBytes(p)
		}
		switch storageKind[e.Name()] {
		case "models":
			u.Models += b
		case "packages":
			u.Packages += b
		case "runtimes":
			u.Runtimes += b
		default:
			u.Other += b
		}
	}
	u.Total = u.Models + u.Packages + u.Runtimes + u.Other
	u.DiskFree, u.DiskTotal = diskFree(n.dataDir), diskTotal(n.dataDir)
	u.At = time.Now().Unix()
	return u
}

// refreshStorage measures now and keeps the result for the next heartbeat.
func (n *Node) refreshStorage() StorageUse {
	u := n.measureStorage()
	n.mu.Lock()
	n.storage = &u
	n.mu.Unlock()
	return u
}

// storageChanged asks the storage loop to measure again soon (a model was
// added or removed). It never blocks.
func (n *Node) storageChanged() {
	n.mu.Lock()
	ch := n.storageKick
	n.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// storageLoop measures every storageEvery and shortly after storageChanged,
// at most once every 10 seconds.
func (n *Node) storageLoop() {
	kick := make(chan struct{}, 1)
	n.mu.Lock()
	n.storageKick = kick
	n.mu.Unlock()
	for {
		n.refreshStorage()
		select {
		case <-time.After(storageEvery):
		case <-kick:
			time.Sleep(10 * time.Second)
			select { // fold kicks that came in while waiting
			case <-kick:
			default:
			}
		}
	}
}

// storageLimit is the limit and the over-limit choice the controller sent.
func (n *Node) storageLimit() (int64, string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.storageMax, n.storageFull
}

// makeRoomUnderLimit checks that need more bytes keep Outpost within this
// node's storage limit. When they would not, and the limit is set to remove
// old models, it deletes the least recently used models the controller offers
// in rp (whether or not the disk-full cleanup in Settings is on, because this
// is the per-node choice) until the copy fits. When that would still not be
// enough, or the limit is set to refuse, it deletes nothing, notes the refusal
// on the node card and returns an error that says what to do.
func (n *Node) makeRoomUnderLimit(need int64, target string, rp *Reclaim) error {
	limit, full := n.storageLimit()
	if limit <= 0 || need <= 0 {
		return nil
	}
	// Bytes that copies already in progress have still to write count as used.
	used := n.refreshStorage().Total + n.pendingBytes(target)
	if used+need <= limit {
		return nil
	}
	over := used + need - limit
	var cands []reclaimCand
	var reclaimable int64
	if full != "refuse" && rp != nil {
		cands, reclaimable = n.reclaimCands(target, rp.Names, rp.Direct)
	}
	if full == "refuse" || reclaimable < over {
		why := "it is set to refuse"
		if full != "refuse" {
			why = fmt.Sprintf("removing every stored model that can be restored (%s) would not be enough", humanBytes(reclaimable))
		}
		msg := fmt.Sprintf("storage limit reached: %s needs %s, Outpost already uses %s of this node's %s limit, and %s; nothing was deleted. Raise the limit or clean up models on the node's card",
			target, humanBytes(need), humanBytes(used), humanBytes(limit), why)
		log.Printf("refused %s: %s", target, msg)
		n.mu.Lock()
		n.limitNote, n.limitAt = fmt.Sprintf("Refused %s: needs %s, %s left under the %s limit. Nothing was deleted.",
			target, humanBytes(need), humanBytes(max(limit-used, 0)), humanBytes(limit)), time.Now().Unix()
		n.mu.Unlock()
		n.resendHello()
		return fmt.Errorf("%s", msg)
	}
	n.removeUntil(cands, func(freed int64) bool { return freed >= over }, target, need,
		fmt.Sprintf(" to stay under the %s storage limit", humanBytes(limit)))
	if u := n.refreshStorage().Total + n.pendingBytes(target); u+need > limit {
		return fmt.Errorf("storage limit reached: %s needs %s and Outpost uses %s of this node's %s limit after cleanup", target, humanBytes(need), humanBytes(u), humanBytes(limit))
	}
	return nil
}

// pendingCopy is a model copy that passed the room check and is still being
// written: dir is its folder and final its size when complete.
type pendingCopy struct {
	dir   string
	final int64
}

// pendingBytes is what the copies in progress (other than except) have still
// to write. Without it two copies that start together would each see the same
// free room and could pass the storage limit, or fill the disk, between them.
func (n *Node) pendingBytes(except string) int64 {
	n.mu.Lock()
	list := make([]pendingCopy, 0, len(n.pendingRoom))
	for name, p := range n.pendingRoom {
		if name != except {
			list = append(list, p)
		}
	}
	n.mu.Unlock()
	var sum int64
	for _, p := range list {
		if left := p.final - dirBytes(p.dir); left > 0 {
			sum += left
		}
	}
	return sum
}

// reserveRoom is makeRoom for a copy that is about to start: it checks that
// need more bytes fit (counting what other copies in progress will still
// write), and on success books the room until the returned function is called.
// total is the model's full size and target its name.
func (n *Node) reserveRoom(need, total int64, target string, rp *Reclaim) (func(), error) {
	n.roomMu.Lock()
	defer n.roomMu.Unlock()
	if err := n.makeRoom(need, target, rp); err != nil {
		return nil, err
	}
	n.mu.Lock()
	if n.pendingRoom == nil {
		n.pendingRoom = map[string]pendingCopy{}
	}
	n.pendingRoom[target] = pendingCopy{dir: filepath.Join(n.modelsDir(), target), final: total}
	n.mu.Unlock()
	return func() {
		n.mu.Lock()
		delete(n.pendingRoom, target)
		n.mu.Unlock()
	}, nil
}
