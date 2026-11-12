package main

// Self-update support shared by the controller and node:
//   - inspectBinary reads an uploaded Outpost binary's platform and version
//     without running it (it may be built for another OS).
//   - installRelease puts a verified copy in <data>/agent/outpost-<version>.
//   - launchNewest (at startup) and relaunch (after an update) switch to the
//     newest installed copy, so updates survive reboots and need no root.

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const relaunchExitCode = 75 // Windows: child asks its launcher to start the newest copy

func platformKey(goos, goarch string) string { return goos + "-" + goarch }

func thisPlatform() string { return platformKey(runtime.GOOS, runtime.GOARCH) }

func exeSuffix(goos string) string {
	if goos == "windows" {
		return ".exe"
	}
	return ""
}

// compareVersions compares dotted numeric versions ("0.2.10" > "0.2.9"). A suffix after a hyphen
// ("0.12.18-dev.57", a branch build) makes a pre-release: it sorts below the same number without
// a suffix and above any lower number. Two suffixes compare dot part by dot part, numbers
// numerically, so -dev.9 < -dev.10.
func compareVersions(a, b string) int {
	ca, sa, _ := strings.Cut(strings.TrimPrefix(a, "v"), "-")
	cb, sb, _ := strings.Cut(strings.TrimPrefix(b, "v"), "-")
	if c := compareDotted(ca, cb); c != 0 {
		return c
	}
	switch {
	case sa == sb:
		return 0
	case sa == "":
		return 1
	case sb == "":
		return -1
	}
	return compareDotted(sa, sb)
}

func compareDotted(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y string
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		nx, ex := strconv.Atoi(x)
		ny, ey := strconv.Atoi(y)
		if ex == nil && ey == nil || (x == "" && ey == nil) || (y == "" && ex == nil) {
			if nx != ny {
				if nx < ny {
					return -1
				}
				return 1
			}
			continue
		}
		if c := strings.Compare(x, y); c != 0 {
			return c
		}
	}
	return 0
}

// isPrerelease reports whether a version is a branch build ("0.12.18-dev.57").
func isPrerelease(v string) bool { return strings.Contains(v, "-") }

// inspectBinary returns the platform ("linux-amd64") and Outpost version of a binary.
func inspectBinary(path string) (platform, version string, err error) {
	goos, goarch := "", ""
	if f, e := elf.Open(path); e == nil {
		goos = "linux"
		switch f.Machine {
		case elf.EM_X86_64:
			goarch = "amd64"
		case elf.EM_AARCH64:
			goarch = "arm64"
		}
		f.Close()
	} else if f, e := macho.Open(path); e == nil {
		goos = "darwin"
		switch f.Cpu {
		case macho.CpuAmd64:
			goarch = "amd64"
		case macho.CpuArm64:
			goarch = "arm64"
		}
		f.Close()
	} else if f, e := pe.Open(path); e == nil {
		goos = "windows"
		switch f.Machine {
		case pe.IMAGE_FILE_MACHINE_AMD64:
			goarch = "amd64"
		case pe.IMAGE_FILE_MACHINE_ARM64:
			goarch = "arm64"
		}
		f.Close()
	} else {
		return "", "", errors.New("not a Linux, macOS or Windows executable")
	}
	if goarch == "" {
		return "", "", fmt.Errorf("unsupported CPU architecture for %s", goos)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	// The pattern requires a version right after the marker, so the copy of
	// this pattern that lives in the binary itself never matches.
	m := buildInfoRe.FindSubmatch(data)
	if m == nil {
		return "", "", errors.New("this is not an Outpost binary (or it is older than 0.2.0)")
	}
	return platformKey(goos, goarch), string(m[1]), nil
}

var buildInfoRe = regexp.MustCompile(`OUTPOST_BUILD_INFO\|([0-9]+\.[0-9]+\.[0-9]+[0-9A-Za-z.+-]{0,24})\|`)

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func agentBinaryName(version string) string { return "outpost-" + version + exeSuffix(runtime.GOOS) }

// installRelease copies a verified binary for this platform into agentDir and
// checks that it runs and reports the expected version before keeping it.
func installRelease(src, agentDir, version, wantSHA string) (string, error) {
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return "", err
	}
	tmp := filepath.Join(agentDir, ".incoming"+exeSuffix(runtime.GOOS))
	if err := copyFile(src, tmp, 0o755); err != nil {
		return "", err
	}
	if wantSHA != "" {
		got, _, err := fileSHA256(tmp)
		if err != nil || !strings.EqualFold(got, wantSHA) {
			os.Remove(tmp)
			return "", errf("checksum_mismatch", true, "downloaded binary failed sha256 check")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, tmp, "version").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Outpost "+version) {
		os.Remove(tmp)
		return "", fmt.Errorf("new binary failed its self-check (output %q, err %v)", strings.TrimSpace(string(out)), err)
	}
	dst := filepath.Join(agentDir, agentBinaryName(version))
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}
	// A fresh update replaces any earlier rollback.
	os.Remove(pinPath(agentDir))
	os.RemoveAll(heldDir(agentDir))
	pruneAgentDir(agentDir, 3)
	beginTrial(agentDir, Version, version)
	return dst, nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

type agentCopy struct {
	path, version string
}

func installedCopies(agentDir string) []agentCopy {
	var out []agentCopy
	entries, _ := os.ReadDir(agentDir)
	suffix := exeSuffix(runtime.GOOS)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "outpost-") || !strings.HasSuffix(name, suffix) {
			continue
		}
		v := strings.TrimSuffix(strings.TrimPrefix(name, "outpost-"), suffix)
		if v == "" || strings.ContainsAny(v, " /\\") {
			continue
		}
		out = append(out, agentCopy{filepath.Join(agentDir, name), v})
	}
	sort.Slice(out, func(i, j int) bool { return compareVersions(out[i].version, out[j].version) > 0 })
	return out
}

// ---- version selection, rollback ----
//
// <data>/agent holds every Outpost version this machine has run
// (outpost-<version>). At startup the process switches to the target version:
//   - the pinned version (<data>/agent/pin), set by a rollback, as long as no
//     copy newer than it has been installed since, else
//   - the newest copy, if it is newer than the running binary.
// A rollback also moves the copies newer than the target into agent/held, so
// that versions older than 0.3.1 (which do not read the pin) do not jump
// forward again. Installing an update clears the pin and held/.

func pinPath(agentDir string) string { return filepath.Join(agentDir, "pin") }
func heldDir(agentDir string) string { return filepath.Join(agentDir, "held") }
func readPin(agentDir string) string {
	b, _ := os.ReadFile(pinPath(agentDir))
	return strings.TrimSpace(string(b))
}

// selectTarget returns the copy this process should switch to, if any.
func selectTarget(agentDir string) (agentCopy, bool) {
	copies := installedCopies(agentDir)
	if pin := readPin(agentDir); pin != "" {
		var pinned *agentCopy
		newer := false
		for i := range copies {
			if copies[i].version == pin {
				pinned = &copies[i]
			} else if compareVersions(copies[i].version, pin) > 0 {
				newer = true
			}
		}
		if pinned != nil && !newer {
			if pinned.version == Version {
				return agentCopy{}, false
			}
			return *pinned, true
		}
		// Stale pin: an update was installed after the rollback.
		os.Remove(pinPath(agentDir))
		os.RemoveAll(heldDir(agentDir))
	}
	if len(copies) == 0 || compareVersions(copies[0].version, Version) <= 0 {
		return agentCopy{}, false
	}
	return copies[0], true
}

// newestCopy is kept for the Windows launcher; it follows the same rules.
func newestCopy(agentDir string) (agentCopy, bool) { return selectTarget(agentDir) }

// ensureSelfCopy keeps a copy of the running version in agentDir, so it can be
// rolled back to after a later update.
func ensureSelfCopy(agentDir string) {
	for _, c := range installedCopies(agentDir) {
		if c.version == Version {
			return
		}
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	if os.MkdirAll(agentDir, 0o755) != nil {
		return
	}
	dst := filepath.Join(agentDir, agentBinaryName(Version))
	tmp := dst + ".tmp"
	if copyFile(self, tmp, 0o755) == nil {
		_ = os.Rename(tmp, dst)
	}
}

// adoptInstalledBinary keeps a copy of the binary that "outpost install" put in
// /usr/local/bin when it is an older Outpost for this platform, so machines
// installed before rollback support can still roll back to it.
func adoptInstalledBinary(agentDir string) {
	platform, version, err := inspectBinary(installedBinary)
	if err != nil || platform != thisPlatform() || compareVersions(version, Version) >= 0 {
		return
	}
	for _, c := range installedCopies(agentDir) {
		if c.version == version {
			return
		}
	}
	dst := filepath.Join(agentDir, agentBinaryName(version))
	if copyFile(installedBinary, dst+".tmp", 0o755) == nil {
		_ = os.Rename(dst+".tmp", dst)
	}
}

// previousVersion is the newest installed version older than the running one.
func previousVersion(agentDir string) string {
	for _, c := range installedCopies(agentDir) {
		if compareVersions(c.version, Version) < 0 {
			return c.version
		}
	}
	return ""
}

// prepareRollback pins version and sets newer copies aside. It returns the
// copy to start.
func prepareRollback(agentDir, version string) (agentCopy, error) {
	var target *agentCopy
	copies := installedCopies(agentDir)
	for i := range copies {
		if copies[i].version == version {
			target = &copies[i]
		}
	}
	if target == nil {
		return agentCopy{}, fmt.Errorf("version %s is not installed on this machine", version)
	}
	if compareVersions(version, Version) >= 0 {
		return agentCopy{}, fmt.Errorf("%s is not older than the running %s", version, Version)
	}
	if err := os.MkdirAll(heldDir(agentDir), 0o755); err != nil {
		return agentCopy{}, err
	}
	for _, c := range copies {
		if compareVersions(c.version, version) > 0 {
			if err := os.Rename(c.path, filepath.Join(heldDir(agentDir), filepath.Base(c.path))); err != nil {
				return agentCopy{}, err
			}
		}
	}
	os.Remove(trialPath(agentDir)) // a deliberate rollback ends any trial
	if err := writeFileAtomic(pinPath(agentDir), []byte(version+"\n"), 0o644); err != nil {
		return agentCopy{}, err
	}
	return *target, nil
}

// pruneAgentDir keeps the newest `keep` copies plus the pinned and running ones.
func pruneAgentDir(agentDir string, keep int) {
	self, _ := os.Executable()
	pin := readPin(agentDir)
	for i, c := range installedCopies(agentDir) {
		if i >= keep && c.path != self && c.version != pin && c.version != Version {
			os.Remove(c.path)
		}
	}
}

// stripJoin removes --join TOKEN / --join=TOKEN so a relaunch does not re-join.
func stripJoin(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--join" || a == "-join" {
			i++
			continue
		}
		if strings.HasPrefix(a, "--join=") || strings.HasPrefix(a, "-join=") {
			continue
		}
		out = append(out, a)
	}
	return out
}
