package sandbox

const seccompArch = 0xC000003E // AUDIT_ARCH_X86_64

// ptrace, mount, umount2, pivot_root, swapon, swapoff, reboot, init_module, delete_module,
// finit_module, kexec_load, add_key, request_key, keyctl, setns, perf_event_open,
// process_vm_writev, bpf
var seccompDeny = []uint32{101, 165, 166, 155, 167, 168, 169, 175, 176, 313, 246, 248, 249, 250, 308, 298, 311, 321}
