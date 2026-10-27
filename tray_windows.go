//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	procCreateMutexW          = kernel32.NewProc("CreateMutexW")
	procGetConsoleWindow      = kernel32.NewProc("GetConsoleWindow")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	procFreeConsole           = kernel32.NewProc("FreeConsole")
	user32                    = syscall.NewLazyDLL("user32.dll")
	procShowWindow            = user32.NewProc("ShowWindow")
)

const createNoWindow = 0x08000000

// trayLock keeps one tray per node data folder (a named mutex).
func trayLock(dataDir string) (func(), error) {
	h := sha256.Sum256([]byte(dataDir))
	name, _ := syscall.UTF16PtrFromString(`Local\OutpostTray-` + hex.EncodeToString(h[:8]))
	r, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if r == 0 {
		return nil, err
	}
	if err == syscall.ERROR_ALREADY_EXISTS {
		syscall.CloseHandle(syscall.Handle(r))
		return nil, fmt.Errorf("the Outpost tray icon is already running")
	}
	return func() { syscall.CloseHandle(syscall.Handle(r)) }, nil
}

// trayDetachConsole hides the console window Windows opened for the tray at
// login. A console the user opened themselves (more than one process
// attached) is left alone. The tray then logs to <data>/tray.log.
func trayDetachConsole(dataDir string) {
	var pids [4]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), 4)
	if n != 1 {
		return
	}
	if f, err := os.OpenFile(filepath.Join(dataDir, "tray.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		log.SetOutput(f)
	}
	if hwnd, _, _ := procGetConsoleWindow.Call(); hwnd != 0 {
		procShowWindow.Call(hwnd, 0) // SW_HIDE
	}
	procFreeConsole.Call()
}

// startNodeProcess runs the node in the background with no window; its log
// goes to <data>/node.log.
func startNodeProcess(dataDir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	logf, err := openNodeLog(dataDir)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(self, "node", "--data", dataDir)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow | syscall.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func openURL(u string) {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}
