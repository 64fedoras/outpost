package main

import (
	"errors"
	"strings"
	"testing"
)

func TestHFAccessHint(t *testing.T) {
	url := hfBase() + "/black-forest-labs/FLUX.1-schnell/resolve/main/ae.safetensors"
	e := errors.New("Hugging Face: 401 Unauthorized")
	if h := hfAccessHint(e, false, url); !strings.Contains(h, "black-forest-labs/FLUX.1-schnell is gated") || !strings.Contains(h, "token") {
		t.Fatalf("no token: %q", h)
	}
	if h := hfAccessHint(e, true, url); !strings.Contains(h, "accept the licence") {
		t.Fatalf("with token: %q", h)
	}
	if h := hfAccessHint(errors.New("Hugging Face: 404 Not Found"), false, url); h != "" {
		t.Fatalf("404 should have no hint: %q", h)
	}
}
