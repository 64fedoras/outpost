//go:build linux && !amd64 && !arm64

package main

const seccompArch = 0

var seccompDeny []uint32
