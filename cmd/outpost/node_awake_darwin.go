//go:build darwin

package main

import (
	"log"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// keepAwake stops macOS from idle-sleeping the Mac while the node runs, so it
// stays reachable for jobs and keeps its heartbeat (a sleeping Mac drops off
// the controller every few minutes). It runs `caffeinate -i -w <our pid>`: the
// idle-sleep assertion only, so the display can still sleep, and caffeinate
// exits by itself when the node does. If caffeinate quits early it is started
// again after a pause.
func keepAwake() {
	const bin = "/usr/bin/caffeinate"
	if _, err := os.Stat(bin); err != nil {
		log.Printf("keep awake: %s not found; the Mac may sleep and drop off the controller", bin)
		return
	}
	pid := strconv.Itoa(os.Getpid())
	logged := false
	for {
		start := time.Now()
		err := exec.Command(bin, "-i", "-w", pid).Run()
		if !logged || time.Since(start) > time.Hour {
			log.Printf("keep awake: caffeinate stopped (%v); starting it again", err)
			logged = true
		}
		time.Sleep(time.Minute)
	}
}
