package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// DatabaseURLOf returns the connection string of the database a local
// environment hosts the DB service on: the url of its database node
// (TypeDatabase), on the environment's Postgres container, which trusts
// every connection.
func DatabaseURLOf(env *ir.ResolvedEnvironment, service string) (string, error) {
	if env.Resources != nil {
		for _, res := range env.Resources.Resources {
			if res == nil || res.Type != TypeDatabase || res.Properties["service"] != service {
				continue
			}
			if url, ok := res.Properties["url"].(string); ok && url != "" {
				return url, nil
			}
		}
	}
	return "", fmt.Errorf("local: environment %s hosts no database of %s", env.Environment, service)
}

// Queue runs a queue document (internal/queueops) on the database of the
// DB service that declares its queue, with the migration runner,
// `superschematic-migrate queue`, as stack dev's migrations run it: the
// runner holds the database drivers (D27). It needs only the environment's
// Postgres container, which stack dev runs, and returns what the runner
// printed. A runner's refusal comes back as its own line.
func (p *Provisioner) Queue(ctx context.Context, req registry.ProvisionRequest, service string, document []byte) ([]byte, error) {
	env := req.Environment
	url, err := DatabaseURLOf(env, service)
	if err != nil {
		return nil, err
	}
	runner, err := p.migrateBinary()
	if err != nil {
		return nil, err
	}
	file, err := os.CreateTemp("", "superschematic-queue-*.json")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.Write(document); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	out, err := p.runner().Run(ctx, Command{Path: runner, Args: []string{"queue", "--queue", file.Name(), "--database-url", url}})
	if err == nil {
		return out, nil
	}
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Stderr == "" {
		return nil, fmt.Errorf("local: %w", err)
	}
	line := runnerLine(runErr.Stderr)
	if strings.Contains(line, "postgres: connect:") {
		return nil, fmt.Errorf("local: the database of %s at %s does not answer (%s); run the environment with superschematic stack dev --environment %s, then run this from another terminal",
			service, url, line, env.Environment)
	}
	return nil, fmt.Errorf("local: %s", line)
}

// runnerLine is the error the migration runner wrote to stderr: the last
// line that begins with its name, without it, else all of stderr.
func runnerLine(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if rest, ok := strings.CutPrefix(lines[i], MigrateBinary+": "); ok {
			return rest
		}
	}
	return strings.TrimSpace(stderr)
}
