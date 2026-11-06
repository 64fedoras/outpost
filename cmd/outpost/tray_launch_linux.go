//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// userSessionEnv returns the environment a process needs to reach a logged-in
// user's desktop session bus from outside it (for example from sudo). ok is
// false when that user has no session bus, i.e. is not logged in.
func userSessionEnv(uid int) (env []string, ok bool) {
	rt := "/run/user/" + strconv.Itoa(uid)
	if _, err := os.Stat(rt + "/bus"); err != nil {
		return nil, false
	}
	return []string{
		"XDG_RUNTIME_DIR=" + rt,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + rt + "/bus",
	}, true
}

// launchTrayNow starts the tray icon in the given user's desktop session so
// no re-login is needed. It does nothing (and says why) if the user is not
// logged in to a desktop.
func launchTrayNow(uid, gid int, home, dataDir string) string {
	env, ok := userSessionEnv(uid)
	if !ok {
		return "not logged in to a desktop; the icon appears at the next login"
	}
	cmd := exec.Command(installedBinary, "tray", "--data", dataDir)
	cmd.Env = append([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}, env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("could not start now (%v); it appears at the next login", err)
	}
	_ = cmd.Process.Release()
	return "started now"
}
