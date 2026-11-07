//go:build linux

package sandbox

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// Linux sandbox: the node re-runs its own binary as "sandbox-child <policy>".
// That process locks itself in and then execs the command, because Go cannot run
// code between fork and exec. Layers (see sandboxCaps):
//
//	landlock  filesystem confinement (kernel 5.13+), no root needed
//	netns     an empty network namespace through a user namespace (strict, network off)
//	llnet     Landlock TCP rules (kernel 6.7+), used when user namespaces are not allowed
//	seccomp   deny list for ptrace, mount, bpf, kernel modules, keyrings and the like (strict)
//	caps      all capabilities dropped, no_new_privs set
//	rlimit    file size and core dump limits
const (
	sysLandlockCreate = 444
	sysLandlockAdd    = 445
	sysLandlockRest   = 446

	llCreateVersion = 1 << 0

	llFSExecute    = 1 << 0
	llFSWriteFile  = 1 << 1
	llFSReadFile   = 1 << 2
	llFSReadDir    = 1 << 3
	llFSRefer      = 1 << 13
	llFSTruncate   = 1 << 14
	llFSIoctlDev   = 1 << 15
	llNetBindTCP   = 1 << 0
	llNetConnectTP = 1 << 1

	prSetNoNewPrivs = 38
	prCapBsetDrop   = 24
	prCapAmbient    = 47
	prSetSeccomp    = 22
	rlimitNproc     = 6
)

type sandboxCaps struct {
	ABI       int  // Landlock ABI version, 0 = none
	UserNetNS bool // an unprivileged user + network namespace can be created
	Seccomp   bool
}

var (
	capsOnce sync.Once
	capsVal  sandboxCaps
)

func sandboxProbe() sandboxCaps {
	capsOnce.Do(func() {
		abi, _, e := syscall.Syscall(sysLandlockCreate, 0, 0, llCreateVersion)
		if e == 0 {
			capsVal.ABI = int(abi)
		}
		capsVal.Seccomp = seccompArch != 0
		if exe, err := os.Executable(); err == nil {
			cmd := exec.Command(exe, "sandbox-child", "--probe")
			setNetNS(cmd)
			capsVal.UserNetNS = cmd.Run() == nil
		}
	})
	return capsVal
}

func setNetNS(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET
	cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}
	cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}
	cmd.SysProcAttr.GidMappingsEnableSetgroups = false
}

// Layers lists what a job in the given mode gets on this machine.
func Layers(mode string) []string {
	if mode == Off {
		return nil
	}
	c := sandboxProbe()
	l := []string{"env", "caps", "rlimit"}
	if c.ABI > 0 {
		l = append(l, fmt.Sprintf("landlock-v%d", c.ABI))
	}
	if mode == Strict {
		if c.UserNetNS {
			l = append(l, "netns")
		} else if c.ABI >= 4 {
			l = append(l, "tcp-rules")
		}
		if c.Seccomp {
			l = append(l, "seccomp")
		}
	}
	return l
}

// Wrap rewrites cmd so it starts the sandbox child with the policy. The
// returned note says what was enforced.
func Wrap(cmd *exec.Cmd, p *Policy) error {
	c := sandboxProbe()
	p.Landlock = c.ABI > 0
	if p.Mode == Strict {
		if !p.Landlock {
			return unavailable("strict sandbox needs Landlock (Linux 5.13 or newer); this node's kernel has none. Set the node's sandbox to basic or off")
		}
		if !p.Net {
			switch {
			case c.UserNetNS:
				p.NetNS = true
			case c.ABI >= 4:
				p.LLNet = true
			default:
				return unavailable("strict sandbox cannot turn the network off here (user namespaces are blocked and the kernel is older than 6.7). Set the node's sandbox to basic or off")
			}
		}
		p.Seccomp = c.Seccomp
	}
	exe, err := os.Executable()
	if err != nil {
		return unavailable("cannot find the node binary: %v", err)
	}
	exe = strings.TrimSuffix(exe, " (deleted)")
	cmd.Path, cmd.Args = exe, []string{exe, "sandbox-child", p.Encode()}
	cmd.Env = []string{}
	if p.NetNS {
		setNetNS(cmd)
	}
	return nil
}

func Attach(cmd *exec.Cmd, mode string, ramBytes uint64) func() { return func() {} }

func PlatformMode(mode string) string { return mode }

// RunChild is the hidden "sandbox-child" mode: lock this process in, then exec.
func RunChild(args []string) {
	if len(args) > 0 && args[0] == "--probe" {
		// Some distros (Ubuntu 24.04) let the namespace be created but strip its capabilities.
		// Bringing loopback up proves they are there.
		if err := loopbackUp(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	runtime.LockOSThread() // Landlock and seccomp apply per thread; exec from this one
	die := func(code int, format string, a ...any) {
		fmt.Fprintf(os.Stderr, "outpost sandbox: "+format+"\n", a...)
		os.Exit(code)
	}
	if len(args) != 1 {
		die(125, "missing policy")
	}
	p, err := DecodePolicy(args[0])
	if err != nil {
		die(125, "bad policy: %v", err)
	}
	path, err := lookPathIn(p.Argv[0], envValue(p.Env, "PATH"), p.Dir)
	if err != nil {
		die(127, "%s: command not found", p.Argv[0])
	}
	if p.NetNS {
		if err := loopbackUp(); err != nil {
			die(125, "network namespace: %v", err)
		}
	}
	dropCaps()
	_ = os.MkdirAll(p.Tmp, 0o700)
	setLimit(syscall.RLIMIT_CORE, 0)
	if p.FSize > 0 {
		setLimit(syscall.RLIMIT_FSIZE, uint64(p.FSize))
	}
	if os.Getuid() != 0 {
		setLimit(rlimitNproc, 8192)
	}
	if _, _, e := syscall.Syscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); e != 0 {
		die(125, "no_new_privs: %v", e)
	}
	if p.Landlock {
		if err := applyLandlock(p); err != nil {
			die(125, "landlock: %v", err)
		}
	}
	if p.Seccomp {
		if err := applySeccomp(); err != nil {
			die(125, "seccomp: %v", err)
		}
	}
	if err := syscall.Exec(path, p.Argv, p.Env); err != nil {
		die(126, "%s: %v", p.Argv[0], err)
	}
}

func envValue(env []string, k string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], k+"="); ok {
			return v
		}
	}
	return ""
}

func lookPathIn(file, pathVar, dir string) (string, error) {
	if strings.Contains(file, "/") {
		if !filepath.IsAbs(file) {
			file = filepath.Join(dir, file)
		}
		if st, err := os.Stat(file); err == nil && !st.IsDir() {
			return file, nil
		}
		return "", os.ErrNotExist
	}
	for _, d := range filepath.SplitList(pathVar) {
		f := filepath.Join(d, file)
		if st, err := os.Stat(f); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return f, nil
		}
	}
	return "", os.ErrNotExist
}

func setLimit(res int, v uint64) {
	_ = syscall.Setrlimit(res, &syscall.Rlimit{Cur: v, Max: v})
}

// loopbackUp brings "lo" up inside a fresh network namespace so localhost works.
func loopbackUp() error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var ifr struct {
		name  [16]byte
		flags uint16
		_     [22]byte
	}
	copy(ifr.name[:], "lo")
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCGIFFLAGS, uintptr(unsafe.Pointer(&ifr))); e != 0 {
		return e
	}
	ifr.flags |= syscall.IFF_UP
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCSIFFLAGS, uintptr(unsafe.Pointer(&ifr))); e != 0 {
		return e
	}
	return nil
}

// dropCaps removes every capability, including from the bounding set, so even
// uid 0 has no special rights after the exec.
func dropCaps() {
	for c := 0; c < 64; c++ {
		if _, _, e := syscall.Syscall(syscall.SYS_PRCTL, prCapBsetDrop, uintptr(c), 0); e == syscall.EINVAL {
			break
		}
	}
	syscall.Syscall6(syscall.SYS_PRCTL, prCapAmbient, 4, 0, 0, 0, 0) // clear all ambient
	hdr := struct {
		version uint32
		pid     int32
	}{0x20080522, 0}
	var data [2]struct{ eff, perm, inh uint32 }
	syscall.Syscall(syscall.SYS_CAPSET, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0)
}

// ---- Landlock ----

func llFSMask(abi int) uint64 {
	m := uint64(1<<13 - 1) // ABI 1: bits 0 to 12
	if abi >= 2 {
		m |= llFSRefer
	}
	if abi >= 3 {
		m |= llFSTruncate
	}
	if abi >= 5 {
		m |= llFSIoctlDev
	}
	return m
}

func applyLandlock(p *Policy) error {
	abi := sandboxProbe().ABI
	if abi < 1 {
		return fmt.Errorf("not supported")
	}
	handled := llFSMask(abi)
	var attr [16]byte
	size := uintptr(8)
	binary.NativeEndian.PutUint64(attr[0:], handled)
	if p.LLNet && abi >= 4 {
		binary.NativeEndian.PutUint64(attr[8:], llNetBindTCP|llNetConnectTP)
		size = 16
	}
	fd, _, e := syscall.Syscall(sysLandlockCreate, uintptr(unsafe.Pointer(&attr[0])), size, 0)
	if e != 0 {
		return e
	}
	rs := int(fd)
	defer syscall.Close(rs)

	ro := uint64(llFSExecute|llFSReadFile|llFSReadDir) & handled
	rw := handled
	fileBits := uint64(llFSExecute|llFSWriteFile|llFSReadFile|llFSTruncate|llFSIoctlDev) & handled
	add := func(path string, access uint64) error {
		st, err := os.Stat(path)
		if err != nil {
			return nil // folder not on this machine
		}
		if !st.IsDir() {
			access &= fileBits
		}
		pfd, err := syscall.Open(path, 0x200000|syscall.O_CLOEXEC, 0) // O_PATH
		if err != nil {
			return nil
		}
		defer syscall.Close(pfd)
		var r [12]byte
		binary.NativeEndian.PutUint64(r[0:], access)
		binary.NativeEndian.PutUint32(r[8:], uint32(pfd))
		if _, _, e := syscall.Syscall6(sysLandlockAdd, uintptr(rs), 1, uintptr(unsafe.Pointer(&r[0])), 0, 0, 0); e != 0 {
			return fmt.Errorf("allow %s: %v", path, e)
		}
		return nil
	}
	for _, d := range p.RW {
		if d != "" {
			_ = os.MkdirAll(d, 0o700)
		}
	}
	for _, d := range p.RO {
		if d == "" {
			continue
		}
		if err := add(d, ro); err != nil {
			return err
		}
	}
	for _, d := range p.RW {
		if d == "" {
			continue
		}
		if err := add(d, rw); err != nil {
			return err
		}
	}
	if _, _, e := syscall.Syscall(sysLandlockRest, uintptr(rs), 0, 0); e != 0 {
		return e
	}
	return nil
}

// ---- seccomp ----

type sockFilter struct {
	Code uint16
	Jt   uint8
	Jf   uint8
	K    uint32
}

// seccompProgram: wrong architecture is killed, a listed syscall fails with
// EPERM, everything else is allowed.
func seccompProgram(arch uint32, deny []uint32) []sockFilter {
	const (
		ldAbs   = 0x20 // BPF_LD|BPF_W|BPF_ABS
		jeq     = 0x15 // BPF_JMP|BPF_JEQ|BPF_K
		ret     = 0x06
		allow   = 0x7fff0000
		killPrc = 0x80000000
		eperm   = 0x00050000 | 1
	)
	prog := []sockFilter{
		{ldAbs, 0, 0, 4}, // arch
		{jeq, 1, 0, arch},
		{ret, 0, 0, killPrc},
		{ldAbs, 0, 0, 0}, // syscall number
	}
	n := len(deny)
	for i, nr := range deny {
		prog = append(prog, sockFilter{jeq, uint8(n - i), 0, nr})
	}
	return append(prog, sockFilter{ret, 0, 0, allow}, sockFilter{ret, 0, 0, eperm})
}

func applySeccomp() error {
	if seccompArch == 0 {
		return nil
	}
	prog := seccompProgram(seccompArch, seccompDeny)
	fprog := struct {
		n   uint16
		_   [6]byte
		ptr *sockFilter
	}{uint16(len(prog)), [6]byte{}, &prog[0]}
	if _, _, e := syscall.Syscall(syscall.SYS_PRCTL, prSetSeccomp, 2, uintptr(unsafe.Pointer(&fprog))); e != 0 {
		return e
	}
	runtime.KeepAlive(prog)
	return nil
}
