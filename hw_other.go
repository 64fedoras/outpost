//go:build !windows

package main

type winStats struct{}

func windowsCPU(*CPUInfo)                       {}
func windowsGPUs() []GPUInfo                    { return []GPUInfo{} }
func (s *statSampler) sampleWindows(*Heartbeat) {}
