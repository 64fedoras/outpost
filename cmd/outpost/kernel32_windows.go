//go:build windows

package main

import "syscall"

// kernel32 is shared by the hardware sampler and the tray code.
var kernel32 = syscall.NewLazyDLL("kernel32.dll")
