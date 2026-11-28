//go:build linux && (amd64 || arm64)

package tasks

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// deniedSyscalls are the system calls the sandbox refuses on every
// architecture it knows: the ones that start a program, open a socket,
// or open, make, rename or remove a file by its path. sandboxArchSyscalls
// adds the ones only one architecture has.
var deniedSyscalls = []uint32{
	// A program.
	unix.SYS_EXECVE, unix.SYS_EXECVEAT, unix.SYS_PTRACE,
	// The network, and io_uring, which can open a socket or a file with
	// no system call a filter sees.
	unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_BIND, unix.SYS_LISTEN,
	unix.SYS_ACCEPT, unix.SYS_ACCEPT4, unix.SYS_IO_URING_SETUP,
	// A file by its path.
	unix.SYS_OPENAT, unix.SYS_OPENAT2, unix.SYS_UNLINKAT, unix.SYS_RENAMEAT2,
	unix.SYS_MKDIRAT, unix.SYS_SYMLINKAT, unix.SYS_LINKAT, unix.SYS_TRUNCATE, unix.SYS_FCHMODAT,
	unix.SYS_FCHOWNAT, unix.SYS_MKNODAT, unix.SYS_MOUNT,
}

// The classic BPF a seccomp filter is written in: load a word of the
// system call's record, compare it, and return a verdict.
const (
	bpfLoadWord  = unix.BPF_LD | unix.BPF_W | unix.BPF_ABS
	bpfJumpEqual = unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K
	bpfJumpGE    = unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K
	bpfReturn    = unix.BPF_RET | unix.BPF_K

	// The offsets of the system call's number and architecture in
	// struct seccomp_data.
	seccompDataNr   = 0
	seccompDataArch = 4
)

func sandbox() error {
	deny := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
	denied := append(append([]uint32{}, deniedSyscalls...), sandboxArchSyscalls...)

	// A call made through another architecture's table, as a 32-bit one
	// on a 64-bit kernel, has other numbers: all of them are refused.
	filter := []unix.SockFilter{
		{Code: bpfLoadWord, K: seccompDataArch},
		{Code: bpfJumpEqual, K: sandboxAuditArch, Jt: 1, Jf: 0},
		{Code: bpfReturn, K: deny},
		{Code: bpfLoadWord, K: seccompDataNr},
	}
	if sandboxMaxSyscall != 0 {
		// The x32 table of amd64 sets a high bit on the same numbers.
		filter = append(filter,
			unix.SockFilter{Code: bpfJumpGE, K: sandboxMaxSyscall, Jt: 0, Jf: 1},
			unix.SockFilter{Code: bpfReturn, K: deny},
		)
	}
	for _, nr := range denied {
		filter = append(filter,
			unix.SockFilter{Code: bpfJumpEqual, K: nr, Jt: 0, Jf: 1},
			unix.SockFilter{Code: bpfReturn, K: deny},
		)
	}
	filter = append(filter, unix.SockFilter{Code: bpfReturn, K: unix.SECCOMP_RET_ALLOW})

	// A filter is taken only by a process that can gain no privilege,
	// and with TSYNC the kernel sets both on every thread at once.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("the plan's sandbox: no_new_privs: %w", err)
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if _, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&prog))); errno != 0 {
		return fmt.Errorf("the plan's sandbox: seccomp: %w", errno)
	}
	return nil
}
