package main

import "testing"

func TestUpdatePreflight(t *testing.T) {
	rel := &Release{Bytes: 30 << 20}
	if p := updatePreflight(&Hello{WorkFreeBytes: 50 << 20}, rel); len(p) != 1 {
		t.Fatalf("50 MB free should be refused for a 30 MB release, got %v", p)
	}
	if p := updatePreflight(&Hello{WorkFreeBytes: 500 << 20}, rel); len(p) != 0 {
		t.Fatalf("plenty of space: %v", p)
	}
	if p := updatePreflight(&Hello{}, rel); len(p) != 0 {
		t.Fatalf("unknown free space must not block: %v", p)
	}
}
