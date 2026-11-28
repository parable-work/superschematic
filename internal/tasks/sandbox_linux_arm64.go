//go:build linux && arm64

package tasks

import "golang.org/x/sys/unix"

const (
	sandboxAuditArch = unix.AUDIT_ARCH_AARCH64
	// arm64 has one table.
	sandboxMaxSyscall = 0
)

var sandboxArchSyscalls = []uint32{unix.SYS_RENAMEAT}
