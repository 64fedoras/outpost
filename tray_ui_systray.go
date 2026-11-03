//go:build windows || linux

package main

// Windows and Linux draw the tray icon with fyne.io/systray; tray.go only
// uses the small API below so macOS can supply its own (tray_ui_darwin.go).

import "fyne.io/systray"

type trayItem = systray.MenuItem

func trayRunUI(onReady func()) { systray.Run(onReady, func() {}) }
func trayQuitUI()              { systray.Quit() }
func traySetIcon(b []byte)     { systray.SetIcon(b) }
func traySetTitle(s string)    { systray.SetTitle(s) }
func traySetTooltip(s string)  { systray.SetTooltip(s) }
func trayAddSeparator()        { systray.AddSeparator() }

func trayAddItem(title, tip string) *trayItem { return systray.AddMenuItem(title, tip) }
func trayAddCheckbox(title, tip string, checked bool) *trayItem {
	return systray.AddMenuItemCheckbox(title, tip, checked)
}
