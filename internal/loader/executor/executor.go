// Package executor runs schema-adjacent TypeScript modules under bun and
// reads what only exists as runtime values. Today that is the deploy
// documents (deploy.values.ts, argo.config.ts, resources.config.ts), whose
// default export the harness captures together with the module graph it
// resolved. Modules must be side-effect-free at import: the harness denies
// network, clock, randomness and Bun I/O dynamically.
package executor

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/parable-work/superschematic/internal/profile"
)

// Option adjusts one execution.
type Option func(*options)

type options struct {
	profile *profile.Profiler
}

// WithProfiler enables phase timing for an execution.
func WithProfiler(prof *profile.Profiler) Option {
	return func(o *options) {
		o.profile = prof
	}
}

// Diagnostic is one schema-author error the harness collected.
type Diagnostic struct {
	File    string `json:"file"`
	Message string `json:"message"`
}

// writeHarness materializes an embedded harness to a temp file bun can run.
func writeHarness(source []byte) (string, error) {
	f, err := os.CreateTemp("", "psgen-harness-*.ts")
	if err != nil {
		return "", fmt.Errorf("writing execution harness: %w", err)
	}
	if _, err := f.Write(source); err != nil {
		closeErr := f.Close()
		removeErr := os.Remove(f.Name())
		return "", fmt.Errorf("writing execution harness: %w", errors.Join(err, closeErr, removeErr))
	}
	if err := f.Close(); err != nil {
		removeErr := os.Remove(f.Name())
		return "", fmt.Errorf("writing execution harness: %w", errors.Join(err, removeErr))
	}
	return f.Name(), nil
}

// tail returns the last n lines of s, for surfacing bun stderr in errors.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
