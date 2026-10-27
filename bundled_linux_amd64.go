//go:build linux && amd64

package main

import _ "embed"

// libgomp (GCC's OpenMP runtime, GPLv3 with the GCC Runtime Library
// Exception; see bundled/LICENSE-libgomp.txt) is needed by llama.cpp's Linux
// builds but missing from minimal Ubuntu/Debian installs.
//
//go:embed bundled/linux-amd64/libgomp.so.1.gz
var libgompGz []byte

var bundledLibs = map[string][]byte{"libgomp.so.1": libgompGz}
