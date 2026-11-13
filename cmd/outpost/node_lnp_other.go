//go:build !darwin

package main

import "time"

func askLocalNetwork(string) {}

func waitNodeOnline(string, time.Duration) *controlStatus { return nil }
