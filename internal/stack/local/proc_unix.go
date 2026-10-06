//go:build unix

package local

import (
	"errors"
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
