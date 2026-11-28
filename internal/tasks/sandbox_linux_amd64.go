//go:build linux && amd64

package tasks

import "golang.org/x/sys/unix"

const (
	sandboxAuditArch = unix.AUDIT_ARCH_X86_64
	// sandboxMaxSyscall is the first number of the x32 table.
	sandboxMaxSyscall = 0x40000000
)

// The calls amd64 keeps beside their *at forms.
var sandboxArchSyscalls = []uint32{
	unix.SYS_OPEN, unix.SYS_CREAT, unix.SYS_FORK, unix.SYS_VFORK, unix.SYS_UNLINK, unix.SYS_RENAME,
	unix.SYS_RENAMEAT, unix.SYS_MKDIR, unix.SYS_RMDIR, unix.SYS_SYMLINK, unix.SYS_LINK, unix.SYS_CHMOD,
	unix.SYS_CHOWN, unix.SYS_LCHOWN, unix.SYS_MKNOD,
}
