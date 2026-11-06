//go:build linux && amd64

package outpost

import _ "embed"

// Libgomp is GCC's OpenMP runtime (GPLv3 with the GCC Runtime Library Exception; see
// bundled/LICENSE-libgomp.txt), gzipped. llama.cpp's Linux builds need it and minimal
// Ubuntu/Debian installs lack it. Only the linux/amd64 binary carries it.
//
//go:embed bundled/linux-amd64/libgomp.so.1.gz
var Libgomp []byte
