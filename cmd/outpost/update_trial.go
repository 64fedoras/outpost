package main

// Update trial: after an update is installed, the new version is "on trial"
// until it has run for trialHealthyAfter. If it starts more than
// trialMaxStarts times without getting that far (it crashes on start), it
// rolls back to the version that installed it, the same way the Rollback
// button does. Catches crash loops, not hangs: a node that cannot reach its
// controller is not blamed on the update. State lives in agent/trial.json.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const (
	trialMaxStarts    = 3
	trialHealthyAfter = 90 * time.Second
)

type updateTrial struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Starts int    `json:"starts"`
}

func trialPath(agentDir string) string { return filepath.Join(agentDir, "trial.json") }

func readTrial(agentDir string) (updateTrial, bool) {
	var t updateTrial
	b, err := os.ReadFile(trialPath(agentDir))
	if err != nil || json.Unmarshal(b, &t) != nil || t.To == "" || t.From == "" {
		return t, false
	}
	return t, true
}

func writeTrial(agentDir string, t updateTrial) {
	if b, err := json.Marshal(t); err == nil {
		_ = writeFileAtomic(trialPath(agentDir), b, 0o644)
	}
}

// beginTrial is called when an update has been installed.
func beginTrial(agentDir, from, to string) {
	if from == "" || from == to {
		return
	}
	writeTrial(agentDir, updateTrial{From: from, To: to})
}

// checkTrial runs first thing at start-up. It returns the version to roll
// back to when this (trial) version has failed to start too often; it has
// then already pinned that version, and the caller restarts into it.
func trialRollbackTarget(agentDir, running string) (agentCopy, bool) {
	t, ok := readTrial(agentDir)
	if !ok || t.To != running {
		return agentCopy{}, false
	}
	t.Starts++
	if t.Starts <= trialMaxStarts {
		writeTrial(agentDir, t)
		return agentCopy{}, false
	}
	os.Remove(trialPath(agentDir))
	target, err := prepareRollback(agentDir, t.From)
	if err != nil {
		log.Printf("update trial: %s failed to start %d times but cannot roll back to %s: %v", running, t.Starts-1, t.From, err)
		return agentCopy{}, false
	}
	log.Printf("update trial: %s did not stay up %d times in a row; rolling back to %s", running, t.Starts-1, t.From)
	return target, true
}

// checkTrial rolls back and restarts when the trial failed. On Windows only the
// child process counts (the launcher process starts it each time).
func checkTrial(agentDir string, args []string) {
	if runtime.GOOS == "windows" && os.Getenv("OUTPOST_CHILD") != "1" {
		return
	}
	if target, ok := trialRollbackTarget(agentDir, Version); ok {
		restartInto(target.path, args)
	}
}

// trialHealthy ends the trial once this version has stayed up long enough.
func trialHealthy(agentDir string) {
	time.Sleep(trialHealthyAfter)
	if t, ok := readTrial(agentDir); ok && t.To == Version {
		os.Remove(trialPath(agentDir))
		log.Printf("update trial: %s has stayed up %s; it is the kept version", Version, trialHealthyAfter)
	}
}
