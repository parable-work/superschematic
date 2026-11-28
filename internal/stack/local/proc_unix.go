//go:build unix

package local

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup starts the process in a process group of its own, so
// Stop reaches its children too, and a Ctrl-C at the terminal reaches the
// provisioner first, which stops the servers callers first.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminate sends SIGTERM to the process's group.
func terminate(cmd *exec.Cmd) error { return signalGroup(cmd, syscall.SIGTERM) }

// kill sends SIGKILL to the process's group.
func kill(cmd *exec.Cmd) error { return signalGroup(cmd, syscall.SIGKILL) }

// tryLock takes an exclusive lock on f without waiting, and reports
// whether it took it: another process holds it otherwise.
func tryLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

func signalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
