//go:build unix

package local_test

import (
	"os"
	"syscall"
)

// flock takes the lock a job's run holds, as another process would.
func flock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
