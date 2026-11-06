package main

// The menu model behind the macOS menu bar item. It mirrors the few
// fyne.io/systray calls tray.go uses, keeps the state in plain Go, and renders
// it to one JSON line that the helper script (tray_darwin.js) applies. Kept
// free of macOS code so it is tested on every platform.

import (
	"encoding/base64"
	"encoding/json"
	"sync"
)

type menuItem struct {
	m         *menuModel
	id        int
	title     string
	tip       string
	sep       bool
	checkbox  bool
	checked   bool
	hidden    bool
	disabled  bool
	ClickedCh chan struct{}
}

type menuModel struct {
	mu       sync.Mutex
	items    []*menuItem
	icon     []byte
	tip      string
	onChange func() // called (without the lock) after any change
}

type menuEntryJSON struct {
	ID       int    `json:"id"`
	Title    string `json:"title,omitempty"`
	Tip      string `json:"tip,omitempty"`
	Sep      bool   `json:"sep,omitempty"`
	Checked  bool   `json:"checked,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

type menuStateJSON struct {
	Icon string          `json:"icon,omitempty"` // base64 PNG
	Tip  string          `json:"tip"`
	Menu []menuEntryJSON `json:"menu"`
}

func (m *menuModel) changed() {
	if m.onChange != nil {
		m.onChange()
	}
}

func (m *menuModel) add(title, tip string, checkbox, checked bool) *menuItem {
	m.mu.Lock()
	it := &menuItem{m: m, id: len(m.items) + 1, title: title, tip: tip, checkbox: checkbox, checked: checked, ClickedCh: make(chan struct{}, 1)}
	m.items = append(m.items, it)
	m.mu.Unlock()
	m.changed()
	return it
}

func (m *menuModel) addSeparator() {
	m.mu.Lock()
	m.items = append(m.items, &menuItem{m: m, id: len(m.items) + 1, sep: true})
	m.mu.Unlock()
	m.changed()
}

func (m *menuModel) setIcon(b []byte) {
	m.mu.Lock()
	m.icon = b
	m.mu.Unlock()
	m.changed()
}

func (m *menuModel) setTip(s string) {
	m.mu.Lock()
	m.tip = s
	m.mu.Unlock()
	m.changed()
}

// click delivers a menu click to the item's channel (dropped if one is pending).
func (m *menuModel) click(id int) {
	m.mu.Lock()
	var it *menuItem
	for _, x := range m.items {
		if x.id == id && !x.sep {
			it = x
		}
	}
	m.mu.Unlock()
	if it == nil {
		return
	}
	select {
	case it.ClickedCh <- struct{}{}:
	default:
	}
}

// state renders the whole menu as one JSON line (no trailing newline).
func (m *menuModel) state() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := menuStateJSON{Tip: m.tip, Menu: []menuEntryJSON{}}
	if len(m.icon) > 0 {
		st.Icon = base64.StdEncoding.EncodeToString(m.icon)
	}
	for _, it := range m.items {
		if it.hidden {
			continue
		}
		st.Menu = append(st.Menu, menuEntryJSON{ID: it.id, Title: it.title, Tip: it.tip, Sep: it.sep, Checked: it.checked, Disabled: it.disabled})
	}
	b, _ := json.Marshal(st)
	return b
}

func (it *menuItem) set(f func()) {
	it.m.mu.Lock()
	f()
	it.m.mu.Unlock()
	it.m.changed()
}

func (it *menuItem) SetTitle(s string) { it.set(func() { it.title = s }) }
func (it *menuItem) Show()             { it.set(func() { it.hidden = false }) }
func (it *menuItem) Hide()             { it.set(func() { it.hidden = true }) }
func (it *menuItem) Enable()           { it.set(func() { it.disabled = false }) }
func (it *menuItem) Disable()          { it.set(func() { it.disabled = true }) }
func (it *menuItem) Check()            { it.set(func() { it.checked = true }) }
func (it *menuItem) Uncheck()          { it.set(func() { it.checked = false }) }
