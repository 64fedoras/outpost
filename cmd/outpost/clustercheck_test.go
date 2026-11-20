package main

import "testing"

func TestControllerHostFromURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://192.0.2.10:8443": "192.0.2.10",
		"https://example.local":   "example.local",
		"":                        "",
		"::bad":                   "",
	} {
		if got := controllerHostFromURL(in); got != want {
			t.Errorf("controllerHostFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}
