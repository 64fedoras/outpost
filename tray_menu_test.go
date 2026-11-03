package main

import (
	"encoding/json"
	"testing"
)

func TestMenuModel(t *testing.T) {
	changes := 0
	m := &menuModel{onChange: func() { changes++ }}
	a := m.add("Status", "", false, false)
	a.Disable()
	m.addSeparator()
	w := m.add("Accept new work", "tip", true, true)
	q := m.add("Quit", "", false, false)
	q.Hide()
	m.setIcon([]byte("png"))
	m.setTip("Outpost node")
	if changes == 0 {
		t.Fatal("no change callbacks")
	}
	var st menuStateJSON
	if err := json.Unmarshal(m.state(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Tip != "Outpost node" || st.Icon != "cG5n" {
		t.Fatalf("state: %+v", st)
	}
	if len(st.Menu) != 3 { // Quit is hidden
		t.Fatalf("menu: %+v", st.Menu)
	}
	if !st.Menu[0].Disabled || !st.Menu[1].Sep || !st.Menu[2].Checked {
		t.Fatalf("menu flags: %+v", st.Menu)
	}
	w.Uncheck()
	q.Show()
	q.SetTitle("Quit Outpost")
	st = menuStateJSON{}
	json.Unmarshal(m.state(), &st)
	if len(st.Menu) != 4 || st.Menu[2].Checked || st.Menu[3].Title != "Quit Outpost" {
		t.Fatalf("after changes: %+v", st.Menu)
	}
	m.click(w.id)
	m.click(w.id) // a second click while one is pending is dropped
	select {
	case <-w.ClickedCh:
	default:
		t.Fatal("click not delivered")
	}
	select {
	case <-w.ClickedCh:
		t.Fatal("duplicate click queued")
	default:
	}
	m.click(2) // separator and unknown ids are ignored
	m.click(99)
}
