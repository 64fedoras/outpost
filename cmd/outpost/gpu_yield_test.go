package main

import (
	"testing"
	"time"
)

func TestGPUYieldCycle(t *testing.T) {
	y := &gpuYield{on: true}
	now := time.Now()
	at := func(d time.Duration) time.Time { return now.Add(d) }
	if y.step(90, false, true, at(0)) != yieldNone {
		t.Fatal("unload too early")
	}
	if y.step(20, false, true, at(10*time.Second)) != yieldNone || !y.since.IsZero() {
		t.Fatal("a dip must reset the hot stretch")
	}
	y.step(90, false, true, at(20*time.Second))
	if y.step(90, true, true, at(60*time.Second)) != yieldNone {
		t.Fatal("never yield while our own work runs")
	}
	y.step(90, false, true, at(70*time.Second))
	if y.step(90, false, true, at(101*time.Second)) != yieldUnload || !y.yielding {
		t.Fatal("30 s of busy GPU should unload")
	}
	if y.step(80, false, false, at(200*time.Second)) != yieldNone {
		t.Fatal("still busy: stay unloaded")
	}
	y.step(2, false, false, at(300*time.Second))
	if y.step(2, false, false, at(400*time.Second)) != yieldNone {
		t.Fatal("reload too early")
	}
	if y.step(2, false, false, at(601*time.Second)) != yieldReload || y.yielding {
		t.Fatal("5 quiet minutes should reload")
	}
}

func TestGPUYieldSwitchedOffReloads(t *testing.T) {
	y := &gpuYield{on: true, yielding: true}
	y.on = false
	if y.step(90, false, false, time.Now()) != yieldReload || y.yielding {
		t.Fatal("turning the switch off while yielded must reload")
	}
	if y.step(90, false, true, time.Now()) != yieldNone {
		t.Fatal("off means nothing happens")
	}
}
