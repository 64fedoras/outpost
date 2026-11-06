package main

const seccompArch = 0xC00000B7 // AUDIT_ARCH_AARCH64

// same list as amd64, arm64 numbers
var seccompDeny = []uint32{117, 40, 39, 41, 224, 225, 142, 105, 106, 273, 104, 217, 218, 219, 268, 241, 271, 280}
