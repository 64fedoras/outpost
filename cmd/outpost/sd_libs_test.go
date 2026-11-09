//go:build linux && amd64

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A runtime that reports a missing libgomp gets it from the binary, for sd-server too.
func TestCheckSDRunsSuppliesBundledLib(t *testing.T) {
	dir := t.TempDir()
	server := filepath.Join(dir, "sd-server")
	script := "#!/bin/sh\nif [ ! -f \"$(dirname \"$0\")/libgomp.so.1\" ]; then\n echo 'sd-server: error while loading shared libraries: libgomp.so.1: cannot open shared object file' >&2; exit 127\nfi\necho 'sd version 1'\n"
	if err := os.WriteFile(server, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if msg := checkSDRuns(server); msg != "" {
		t.Fatalf("expected success, got %q", msg)
	}
	if _, err := os.Stat(filepath.Join(dir, "libgomp.so.1")); err != nil {
		t.Fatal("libgomp was not written next to sd-server")
	}
}

func TestCheckSDRunsNamesUnbundledLib(t *testing.T) {
	dir := t.TempDir()
	server := filepath.Join(dir, "sd-server")
	script := "#!/bin/sh\necho 'error while loading shared libraries: libfoo.so.9: no' >&2; exit 127\n"
	_ = os.WriteFile(server, []byte(script), 0o755)
	if msg := checkSDRuns(server); !strings.Contains(msg, "libfoo.so.9") || !strings.Contains(msg, "stable-diffusion.cpp") {
		t.Fatalf("unexpected message %q", msg)
	}
}
