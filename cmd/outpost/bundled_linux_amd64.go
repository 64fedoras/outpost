//go:build linux && amd64

package main

import "outpost"

// libgomp (GCC's OpenMP runtime) is embedded by the root package; see embed_linux_amd64.go there.
var libgompGz = outpost.Libgomp

var bundledLibs = map[string][]byte{"libgomp.so.1": libgompGz}
