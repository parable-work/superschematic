package codegen

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

var errToolUnavailable = errors.New("required tool unavailable")

// ToolUnavailableError indicates an external tool is unavailable in PATH.
type ToolUnavailableError struct {
	Tool string
	Err  error
}

func (e *ToolUnavailableError) Error() string {
	return fmt.Sprintf("%s is not available: %v", e.Tool, e.Err)
}

func (e *ToolUnavailableError) Unwrap() error {
	return errToolUnavailable
}

// IsToolUnavailable reports whether an error indicates a missing external tool.
func IsToolUnavailable(err error) bool {
	return errors.Is(err, errToolUnavailable)
}

// RunTool executes a tool in a working directory and wraps missing-tool failures.
func RunTool(workingDir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = workingDir
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}

	var execErr *exec.Error
	if errors.As(err, &execErr) && errors.Is(execErr.Err, exec.ErrNotFound) {
		return &ToolUnavailableError{
			Tool: name,
			Err:  execErr,
		}
	}

	return fmt.Errorf("%s %s failed: %w (output: %s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
}
