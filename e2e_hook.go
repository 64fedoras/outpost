//go:build e2e

package main

import "os"

// Test builds only (go build -tags e2e): point Hugging Face at a local stand-in.
func init() {
	if v := os.Getenv("OUTPOST_TEST_HF"); v != "" {
		hfBase = v
	}
}
