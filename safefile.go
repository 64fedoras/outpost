package main

// Safe saves for state files (settings, nodes, sessions, tokens, tasks, models).
//
// writeFileAtomic writes to a temporary file in the same folder, flushes it to
// disk, renames it over the target and flushes the folder, so a power cut, a
// crash or a full disk leaves either the old file or the new one, never half
// of each. Plain os.WriteFile truncates the target first, so a crash in the
// middle used to leave an empty or cut-off file.

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// writeFileAtomic replaces path with b. The file gets mode (subject to umask
// on creation, then set exactly with Chmod).
func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	return writeAtomic(path, b, mode, false)
}

// writeFileAtomicBackup is writeFileAtomic that first keeps the current file as
// path+".bak", so a file damaged later (by the disk, or a bug) can still be
// recovered from the previous save. See loadJSONWithBackup.
func writeFileAtomicBackup(path string, b []byte, mode os.FileMode) error {
	return writeAtomic(path, b, mode, true)
}

func writeAtomic(path string, b []byte, mode os.FileMode, backup bool) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(b); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := f.Chmod(mode); err != nil {
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if backup {
		// Keep the current file as .bak. A hard link costs nothing and leaves
		// the target in place; where links are not supported (FAT, some
		// network shares) fall back to a copy.
		bak := path + ".bak"
		if st, err := os.Stat(path); err == nil && st.Size() > 0 {
			_ = os.Remove(bak)
			if os.Link(path, bak) != nil {
				if old, err := os.ReadFile(path); err == nil {
					_ = writeAtomic(bak, old, mode, false)
				}
			}
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	syncDir(dir)
	return nil
}

// syncDir flushes a folder's entries (the rename) to disk. Windows cannot
// open a folder for this and NTFS journals renames itself, so it is skipped.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// errBothDamaged is returned when neither a file nor its .bak could be parsed.
var errBothDamaged = errors.New("file and its backup are both unreadable")

// readWithBackup reads path, and if parse fails on it (or it is missing while
// a backup exists) tries path+".bak". usedBackup says which one was used.
func readWithBackup(path string, parse func([]byte) error) (usedBackup bool, err error) {
	b, rerr := os.ReadFile(path)
	if rerr == nil {
		if perr := parse(b); perr == nil {
			return false, nil
		} else {
			err = perr
		}
	} else {
		err = rerr
	}
	bb, berr := os.ReadFile(path + ".bak")
	if berr != nil {
		return false, err
	}
	if parse(bb) != nil {
		return false, errBothDamaged
	}
	return true, nil
}
