package main

// Size-capped log files. node.log (Windows service) and tray.log (all
// desktops) used to grow forever on machines that run for months. A rotating
// file keeps the current log under logMaxBytes and one previous copy (.old).

import (
	"io"
	"log"
	"os"
	"sync"
	"time"
)

const logMaxBytes = 10 << 20

type rotatingFile struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
	max  int64
	held time.Time // a rename failed (file held open elsewhere): don't retry until then
}

func openRotating(path string, max int64) (*rotatingFile, error) {
	r := &rotatingFile{path: path, max: max}
	if fi, err := os.Stat(path); err == nil && fi.Size() > max {
		_ = os.Rename(path, path+".old")
	}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	r.f = f
	r.size = 0
	if fi, err := f.Stat(); err == nil {
		r.size = fi.Size()
	}
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(p)) > r.max && r.size > 0 && time.Now().After(r.held) {
		_ = r.f.Close()
		_ = os.Remove(r.path + ".old") // Windows cannot rename over an existing file
		if os.Rename(r.path, r.path+".old") != nil {
			r.held = time.Now().Add(10 * time.Minute)
		}
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

var _ io.WriteCloser = (*rotatingFile)(nil)

// logToFileFromEnv sends this process's log to a rotating file when the
// process that started it (service, tray, LaunchAgent) set OUTPOST_LOG_FILE.
func logToFileFromEnv() {
	p := os.Getenv("OUTPOST_LOG_FILE")
	if p == "" {
		return
	}
	if r, err := openRotating(p, logMaxBytes); err == nil {
		log.SetOutput(r)
	}
}
