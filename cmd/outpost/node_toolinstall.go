package main

// Node side of the one-click Chromium and OpenSCAD install (controller side:
// controller_toolinstall.go). The archive is unpacked into toolchains/<tool>.new,
// started once, and only then swapped in, so a broken download never replaces a
// working copy. No admin rights are needed except to install system libraries.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// chromeLibPkgs are the Debian/Ubuntu packages a minimal install usually lacks for headless Chromium.
// A name with "|" lists alternatives (the first the system knows is used).
var chromeLibPkgs = []string{"libnss3", "libnspr4", "libatk1.0-0t64|libatk1.0-0", "libatk-bridge2.0-0t64|libatk-bridge2.0-0", "libcups2t64|libcups2",
	"libdrm2", "libxkbcommon0", "libxcomposite1", "libxdamage1", "libxfixes3", "libxrandr2", "libgbm1", "libasound2t64|libasound2",
	"libpango-1.0-0", "libcairo2", "libx11-6", "libxcb1", "libxext6", "fonts-liberation"}

var loaderMissingRe = regexp.MustCompile(`error while loading shared libraries: (\S+):`)

var chromeMissingRe = regexp.MustCompile(`(\S+\.so[\w.]*) => not found`)

// parseMissingLibs reads the "not found" lines of ldd output: every missing library, once.
func parseMissingLibs(out string) []string {
	var libs []string
	seen := map[string]bool{}
	for _, m := range chromeMissingRe.FindAllStringSubmatch(out, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			libs = append(libs, m[1])
		}
	}
	return libs
}

// missingLibs lists the shared libraries a binary needs and the system lacks (Linux, via ldd).
func missingLibs(path string) []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	out, _ := exec.Command("ldd", path).CombinedOutput()
	libs := parseMissingLibs(string(out))
	if len(libs) == 0 { // no ldd: the loader names the first one it misses when the program starts
		b, _ := exec.Command(path, "--version").CombinedOutput()
		if m := loaderMissingRe.FindStringSubmatch(string(b)); m != nil {
			libs = append(libs, m[1])
		}
	}
	return libs
}

// resolveChromePkgs picks, for each entry of chromeLibPkgs, the first package name
// this system's apt knows (Ubuntu 24.04 renamed several with a t64 suffix). Without
// apt-cache it returns the first name of each entry.
func resolveChromePkgs() []string {
	_, aptErr := exec.LookPath("apt-cache")
	var pkgs []string
	for _, alt := range chromeLibPkgs {
		names := strings.Split(alt, "|")
		if aptErr != nil {
			pkgs = append(pkgs, names[len(names)-1])
			continue
		}
		for _, p := range names {
			if exec.Command("apt-cache", "show", p).Run() == nil {
				pkgs = append(pkgs, p)
				break
			}
		}
	}
	return pkgs
}

// installChromeLibs installs every package headless Chromium needs with one apt-get
// call, so the node never needs a round trip per library. Only a root node on Debian
// or Ubuntu can.
func installChromeLibs() error {
	manual := "sudo apt-get install " + strings.Join(resolveChromePkgs(), " ")
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return fmt.Errorf("this node does not run as root, so it cannot install system libraries; run: %s", manual)
	}
	apt, err := exec.LookPath("apt-get")
	if err != nil {
		return errors.New("this node has no apt-get; install the libraries headless Chromium needs with its package manager (on Debian and Ubuntu: " + manual + ")")
	}
	pkgs := resolveChromePkgs()
	if len(pkgs) == 0 {
		return errors.New("apt does not know the Chromium libraries; run apt-get update first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, apt, append([]string{"install", "-y", "-q", "--no-install-recommends"}, pkgs...)...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("apt-get install failed: %v: %s", err, shortText(string(b), 400))
	}
	return nil
}

// chromeWrapper is the script that starts Chromium with the fetched libraries on its library path.
func chromeWrapper() string {
	var dirs []string
	for _, d := range chromeLibDirs {
		dirs = append(dirs, `"$d/libs/root/`+d+`"`)
	}
	return "#!/bin/sh\nd=\"$(cd \"$(dirname \"$0\")/..\" && pwd)\"\nLD_LIBRARY_PATH=" + strings.Join(dirs, ":") +
		"${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH} exec \"$d/chrome-linux64/chrome\" \"$@\"\n"
}

var debNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*$`)

// chromeLibDirs are the folders under libs/root that hold the unpacked libraries.
var chromeLibDirs = []string{"usr/lib/x86_64-linux-gnu", "lib/x86_64-linux-gnu", "usr/lib/x86_64-linux-gnu/nss"}

// installChromeLibsLocal fetches the libraries headless Chromium misses as .deb files,
// with their missing dependencies, and unpacks them under stage/libs/root with dpkg -x.
// That needs no root and nothing outside the node's data folder; a small wrapper in
// stage/bin starts Chromium with LD_LIBRARY_PATH pointing there. Debian and Ubuntu only.
func installChromeLibsLocal(stage string) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return errors.New("fetching libraries needs a Debian or Ubuntu (amd64) node")
	}
	for _, t := range []string{"apt-get", "apt-cache", "dpkg", "dpkg-query"} {
		if _, err := exec.LookPath(t); err != nil {
			return fmt.Errorf("no %s on this node (not Debian or Ubuntu)", t)
		}
	}
	// Everything the packages need, minus what is installed already.
	args := append([]string{"depends", "--recurse", "--no-recommends", "--no-suggests", "--no-conflicts", "--no-breaks", "--no-replaces", "--no-enhances"}, resolveChromePkgs()...)
	out, err := exec.Command("apt-cache", args...).Output()
	if err != nil {
		return fmt.Errorf("apt-cache depends: %v", err)
	}
	seen := map[string]bool{}
	var want []string
	for _, ln := range strings.Split(string(out), "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "<") || ln == "" || strings.ContainsAny(ln, " :") || !debNameRe.MatchString(ln) || seen[ln] {
			continue
		}
		seen[ln] = true
		if b, _ := exec.Command("dpkg-query", "-W", "-f=${db:Status-Abbrev}", ln).Output(); strings.HasPrefix(string(b), "ii") {
			continue
		}
		want = append(want, ln)
	}
	root := filepath.Join(stage, "libs")
	debs := filepath.Join(root, "debs")
	if err := os.MkdirAll(debs, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(debs)
	if len(want) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "apt-get", append([]string{"download", "-q"}, want...)...)
		cmd.Dir = debs
		if b, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("apt-get download: %v: %s (if the package lists are out of date, run sudo apt-get update once)", err, shortText(string(b), 300))
		}
		files, _ := filepath.Glob(filepath.Join(debs, "*.deb"))
		for _, f := range files {
			if b, err := exec.Command("dpkg", "-x", f, filepath.Join(root, "root")).CombinedOutput(); err != nil {
				return fmt.Errorf("dpkg -x %s: %v: %s", filepath.Base(f), err, shortText(string(b), 200))
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(stage, "bin"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stage, "bin", "chrome"), []byte(chromeWrapper()), 0o755)
}

func toolSpecFor(name string) toolSpec {
	for _, s := range toolSpecs {
		if s.name == name {
			return s
		}
	}
	return toolSpec{}
}

// toolTestPath is the program to start once to check an unpacked tool works.
func toolTestPath(tool, dir string) string {
	win := runtime.GOOS == "windows"
	switch {
	case tool == "chromium" && win:
		return filepath.Join(dir, "chrome-win64", "chrome.exe")
	case tool == "chromium":
		if w := filepath.Join(dir, "bin", "chrome"); fileExists(w) { // libraries were fetched: start it through the wrapper
			return w
		}
		return filepath.Join(dir, "chrome-linux64", "chrome")
	case win:
		for _, f := range []string{"openscad.com", "openscad.exe"} {
			if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
				return filepath.Join(dir, f)
			}
		}
		return filepath.Join(dir, "openscad.com")
	}
	return filepath.Join(dir, "bin", "openscad")
}

// unpackTool unpacks an archive downloaded for tool into stage.
func unpackTool(tool, archive, stage string) error {
	if strings.HasSuffix(archive, ".zip") {
		if err := extractZip(archive, stage); err != nil {
			return err
		}
		if tool == "openscad" { // the Windows zip holds one folder; lift its contents
			ents, _ := os.ReadDir(stage)
			if len(ents) == 1 && ents[0].IsDir() {
				top := filepath.Join(stage, ents[0].Name())
				inner, _ := os.ReadDir(top)
				for _, e := range inner {
					if err := os.Rename(filepath.Join(top, e.Name()), filepath.Join(stage, e.Name())); err != nil {
						return err
					}
				}
				os.Remove(top)
			}
		}
		return nil
	}
	// OpenSCAD's Linux build is an AppImage. Unpack it instead of running it so
	// the node does not need FUSE; a small wrapper starts it headless.
	img := filepath.Join(stage, "OpenSCAD.AppImage")
	src, err := os.Open(archive)
	if err != nil {
		return err
	}
	dst, err := os.OpenFile(img, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		src.Close()
		return err
	}
	_, err = io.Copy(dst, src)
	src.Close()
	dst.Close()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, img, "--appimage-extract")
	cmd.Dir = stage
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("unpack AppImage: %v: %s", err, shortText(string(b), 300))
	}
	os.Remove(img)
	if err := os.MkdirAll(filepath.Join(stage, "bin"), 0o755); err != nil {
		return err
	}
	wrapper := "#!/bin/sh\nQT_QPA_PLATFORM=${QT_QPA_PLATFORM:-offscreen} exec \"$(dirname \"$0\")/../squashfs-root/AppRun\" \"$@\"\n"
	return os.WriteFile(filepath.Join(stage, "bin", "openscad"), []byte(wrapper), 0o755)
}

// installTool installs Chromium or OpenSCAD from an archive the controller serves.
func (n *Node) installTool(ti ToolInstall) {
	const op = "toolchain"
	name := installableTools[ti.Tool]
	fail := func(hint, format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		log.Printf("toolchain %s %s: %s", ti.Tool, ti.Version, msg)
		st := OpStatus{Op: op, Target: ti.Tool, State: "failed", BytesTotal: ti.Bytes, Error: msg, Hint: hint}
		_ = n.send("node.op_status", st)
	}
	if name == "" || ti.Tool == "go" {
		fail("", "bad toolchain request")
		return
	}
	root := n.toolchainDir()
	final := filepath.Join(root, ti.Tool)
	stage := filepath.Join(root, ti.Tool+".new")
	if ti.Libs {
		if _, err := os.Stat(stage); err != nil || ti.Tool != "chromium" {
			fail("", "nothing is waiting for libraries; install %s again", name)
			return
		}
		n.opStatus(op, ti.Tool, "installing", 0, 0, "")
		if err := installChromeLibs(); err != nil {
			fail("", "%v", err)
			return
		}
	} else {
		if !validName(ti.Archive) || strings.ContainsAny(ti.Archive, `/\`) {
			fail("", "bad toolchain request")
			return
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			fail("", "%v", err)
			return
		}
		archive := filepath.Join(root, ti.Archive)
		n.opStatus(op, ti.Tool, "downloading", 0, ti.Bytes, "")
		err := n.fetchFile("/v1/toolchain/"+ti.Tool+"/"+ti.Archive, archive, ti.SHA256, ti.Bytes, func(d int64) {
			n.opStatus(op, ti.Tool, "downloading", d, ti.Bytes, "")
		})
		if err != nil {
			fail("", "%s", withDefenderHint("download: "+err.Error(), n.dataDir))
			return
		}
		n.opStatus(op, ti.Tool, "installing", ti.Bytes, ti.Bytes, "")
		os.RemoveAll(stage)
		if err := os.MkdirAll(stage, 0o755); err != nil {
			fail("", "%v", err)
			return
		}
		err = unpackTool(ti.Tool, archive, stage)
		os.Remove(archive)
		if err != nil {
			os.RemoveAll(stage)
			fail("", "unpack: %v", err)
			return
		}
	}
	// Start it once. Chromium on a bare Linux install usually fails here for want of libraries.
	test := toolTestPath(ti.Tool, stage)
	ver, ok := toolVersion(test, toolSpecFor(ti.Tool))
	if !ok && ti.Tool == "chromium" && !ti.Libs {
		if libs := missingLibs(test); len(libs) > 0 {
			// Fetch what it misses without root, into the tool's own folder, and try again.
			n.opStatus(op, ti.Tool, "installing", 0, 0, "")
			log.Printf("chromium misses %s; fetching them as .deb files into %s", strings.Join(libs, ", "), stage)
			if err := installChromeLibsLocal(stage); err != nil {
				log.Printf("chromium libraries: %v", err)
			}
			test = toolTestPath(ti.Tool, stage)
			ver, ok = toolVersion(test, toolSpecFor(ti.Tool))
		}
	}
	if !ok {
		if libs := missingLibs(test); len(libs) > 0 {
			fail("libs", "%s is unpacked but cannot start: missing %s. Outpost could not fetch them itself; all of them can be installed at once with: sudo apt-get install %s",
				name, strings.Join(libs, ", "), strings.Join(resolveChromePkgs(), " "))
			return // the unpacked copy stays for the retry
		}
		os.RemoveAll(stage)
		fail("", "the unpacked %s does not start on this node", name)
		return
	}
	old := filepath.Join(root, "."+ti.Tool+"-old")
	os.RemoveAll(old)
	if _, err := os.Stat(final); err == nil {
		if err := os.Rename(final, old); err != nil {
			os.RemoveAll(stage)
			fail("", "replace old %s: %v", name, err)
			return
		}
	}
	if err := os.Rename(stage, final); err != nil {
		os.Rename(old, final)
		os.RemoveAll(stage)
		fail("", "%v", err)
		return
	}
	os.RemoveAll(old)
	log.Printf("%s %s installed at %s", ti.Tool, ver, final)
	n.toolInventory(true)
	n.opStatus(op, ti.Tool, "done", ti.Bytes, ti.Bytes, "")
	n.resendHello()
}

// removeTool deletes a tool Outpost installed. Copies found on the system are never touched.
func (n *Node) removeTool(tool string) {
	if !n.removeToolFiles(tool) {
		return
	}
	log.Printf("removed %s installed by Outpost", tool)
	n.toolInventory(true)
	n.resendHello()
}

func (n *Node) removeToolFiles(tool string) bool {
	if installableTools[tool] == "" {
		return false
	}
	for _, d := range []string{tool, tool + ".new", "." + tool + "-old"} {
		os.RemoveAll(filepath.Join(n.toolchainDir(), d))
	}
	return true
}
