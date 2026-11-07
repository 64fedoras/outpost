package main

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// The web UI is plain script files loaded by <script> tags, in order, with no
// build step. Nothing type-checks that, so a file that is not listed in
// index.html would silently never run. This keeps the page and the folder in step.
func TestWebScriptsAreAllLoadedInOrder(t *testing.T) {
	html, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, m := range regexp.MustCompile(`<script src="([^"]+)"></script>`).FindAllStringSubmatch(string(html), -1) {
		listed = append(listed, m[1])
	}
	files, err := fs.Glob(webFS, "web/*.js")
	if err != nil {
		t.Fatal(err)
	}
	onDisk := map[string]bool{}
	for _, f := range files {
		onDisk[path.Base(f)] = true
	}
	inPage := map[string]bool{}
	for _, n := range listed {
		if inPage[n] {
			t.Errorf("%s is loaded twice", n)
		}
		inPage[n] = true
		if !onDisk[n] {
			t.Errorf("index.html loads %s, which does not exist", n)
		}
	}
	for n := range onDisk {
		if !inPage[n] {
			t.Errorf("web/%s is not loaded by index.html, so it would never run", n)
		}
	}
	idx := func(n string) int {
		for i, s := range listed {
			if s == n {
				return i
			}
		}
		return -1
	}
	// guard.js reads app.js globals only when called; app.js defines the helpers
	// everything else uses; boot.js starts the UI and must come after every page.
	if idx("app.js") < 0 || idx("boot.js") != len(listed)-1 {
		t.Errorf("boot.js must be the last script and app.js must be loaded: %v", listed)
	}
	for _, n := range listed {
		if n != "theme.js" && n != "guard.js" && n != "app.js" && idx(n) < idx("app.js") {
			t.Errorf("%s is loaded before app.js", n)
		}
	}
	// Each file is its own script, so each opts into strict mode on its own.
	for _, f := range files {
		b, _ := webFS.ReadFile(f)
		// images.js predates the split and runs in sloppy mode; making it strict
		// is a separate, behaviour-affecting change.
		if f != "web/images.js" && !strings.HasPrefix(string(b), "'use strict';") {
			t.Errorf("%s does not start with 'use strict';", f)
		}
	}
}
