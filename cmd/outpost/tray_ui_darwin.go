//go:build darwin

package main

// The macOS menu bar item. There is no cgo in Outpost's builds, so the item
// is drawn by a small JavaScript-for-Automation script (tray_darwin.js) run
// with /usr/bin/osascript, which can reach AppKit through the ObjC bridge.
// This file keeps the menu state (tray_menu.go), sends it to the script as
// JSON lines, and turns the script's click lines back into ClickedCh events.

import (
	"bufio"
	_ "embed"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

//go:embed tray_darwin.js
var trayHelperScript string

type trayItem = menuItem

var (
	macUI    = &menuModel{}
	macProc  *exec.Cmd
	macPipe  interface{ Write([]byte) (int, error) }
	macPipeM sync.Mutex
	macDone  = make(chan struct{})
	macOnce  sync.Once
)

func macSend() {
	macPipeM.Lock()
	defer macPipeM.Unlock()
	if macPipe == nil {
		return
	}
	if _, err := macPipe.Write(append(macUI.state(), '\n')); err != nil {
		log.Printf("tray: helper pipe: %v", err)
	}
}

func trayRunUI(onReady func()) {
	cmd := exec.Command("/usr/bin/osascript", "-l", "JavaScript", "-e", trayHelperScript)
	in, err := cmd.StdinPipe()
	if err != nil {
		log.Printf("tray: %v", err)
		return
	}
	out, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("tray: %v", err)
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("tray: cannot start the menu bar helper: %v", err)
		return
	}
	macProc = cmd
	macPipeM.Lock()
	macPipe = in
	macPipeM.Unlock()
	macUI.onChange = macSend
	go func() { // clicks (and any error text) come back on stderr
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			line := sc.Text()
			if id, ok := strings.CutPrefix(line, "OUTPOST-CLICK:"); ok {
				if n, err := strconv.Atoi(strings.TrimSpace(id)); err == nil {
					macUI.click(n)
				}
			} else if line != "OUTPOST-READY" {
				log.Printf("tray helper: %s", line)
			}
		}
	}()
	go func() {
		err := cmd.Wait()
		if err != nil {
			log.Printf("tray: the menu bar helper stopped: %v", err)
		}
		macOnce.Do(func() { close(macDone) })
	}()
	onReady()
	macSend()
	<-macDone
}

func trayQuitUI() {
	macPipeM.Lock()
	if c, ok := macPipe.(interface{ Close() error }); ok {
		c.Close() // end of input makes the script quit
	}
	macPipeM.Unlock()
	if macProc != nil && macProc.Process != nil {
		_ = macProc.Process.Kill()
	}
	macOnce.Do(func() { close(macDone) })
}

func traySetIcon(b []byte)    { macUI.setIcon(b) }
func traySetTitle(string)     {} // the menu bar item shows only its icon
func traySetTooltip(s string) { macUI.setTip(s) }
func trayAddSeparator()       { macUI.addSeparator() }

func trayAddItem(title, tip string) *trayItem { return macUI.add(title, tip, false, false) }
func trayAddCheckbox(title, tip string, checked bool) *trayItem {
	return macUI.add(title, tip, true, checked)
}
