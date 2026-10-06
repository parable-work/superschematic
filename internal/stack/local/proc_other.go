//go:build !unix

package local

import (
	"errors"
	"os"
	"os/exec"
)

// setProcessGroup leaves the process in the provisioner's group, where no
// process groups exist.
func setProcessGroup(*exec.Cmd) {}

// terminate kills the process: without process groups there is no signal
// to ask it to stop.
func terminate(cmd *exec.Cmd) error { return kill(cmd) }

// kill kills the process.
func kill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
