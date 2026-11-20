//go:build e2e

package main

import (
	"os"
	"strconv"
)

// Test builds only (go build -tags e2e): point Hugging Face at a local stand-in.
func init() {
	if v := os.Getenv("OUTPOST_TEST_HF"); v != "" {
		setHFBase(v)
	}
	// Point DuckDuckGo at a local stand-in (research mode and chat web search tests).
	if v := os.Getenv("OUTPOST_TEST_DDG"); v != "" {
		ddgURL, searchAllowPrivate = v, true
	}
	// Report this memory bandwidth (GB/s) instead of measuring, to see slow and fast nodes in the UI.
	if v, err := strconv.ParseFloat(os.Getenv("OUTPOST_TEST_MEMBW"), 64); err == nil && v > 0 {
		memBWFixed = v
	}
}
