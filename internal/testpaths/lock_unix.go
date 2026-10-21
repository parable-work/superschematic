//go:build unix

package testpaths

import (
	"fmt"
	"os"
	"syscall"
)

// lockDir takes dir's exclusive lock, waiting for any other process that
// holds it, and returns its release. The kernel drops a flock when its
// holder exits, so a test process killed mid-build leaves no stale lock.
func lockDir(dir string) (func(), error) {
	path, err := lockPath(dir)
	if err != nil {
		return nil, fmt.Errorf("the build lock for %s: %w", dir, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open the build lock for %s: %w", dir, err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s for %s: %w", path, dir, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
