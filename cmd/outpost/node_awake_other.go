//go:build !darwin

package main

// keepAwake is macOS only: Linux and Windows nodes are servers or desktops
// whose sleep settings the owner manages.
func keepAwake() {}
