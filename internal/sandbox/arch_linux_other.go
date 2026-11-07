//go:build linux && !amd64 && !arm64

package sandbox

const seccompArch = 0

var seccompDeny []uint32
