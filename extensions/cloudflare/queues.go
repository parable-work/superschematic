package cloudflare

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/registry"
)

// An operator's operation on a queue's dead messages, and a backfill
// (`superschematic stack queue`, docs/stack-model.md, section 8.8, D96),
// runs on the deploying machine against the origin of the queue's
// database, as the database's migrations do (migrations.go, D92): the
// queue is a table of a Postgres outside Cloudflare, which the Workers
// reach through the database's Hyperdrive config and this machine with
// the connection string the shell holds in OriginEnv(<db>).
//
//  1. it finds the runner as a migration does: SUPERSCHEMATIC_MIGRATE,
//     else superschematic-migrate on PATH;
//  2. it checks the origin as a migration does (reachOrigin): the
//     connection string must reach the host, port, database and user the
//     database's Hyperdrive config names, so the operation never changes
//     the queues of one database while the Workers drain another's. A
//     config that caches reads is not refused, since the operation does
//     not read through it;
//  3. it writes the queue document to a file of its own, readable by its
//     owner alone, and runs `superschematic-migrate queue --queue <file>`
//     with the connection string in DATABASE_URL alone, so no file and no
//     argument holds the password;
//  4. it returns what the runner printed, the result, and writes none of
//     it to the log, since a listing holds the dead messages' bodies: the
//     log holds where the operation ran.
//
// A queue on D1 is refused: no worker claims there, and the core refuses a
// queue on SQLite before it reaches the runner (D68).

var _ registry.QueueRunner = migrationRunner{}

// RunQueue runs a queue document on the origin of req's database, a
// database on Hyperdrive, with the migration runner on this machine, and
// returns the runner's result.
func (r migrationRunner) RunQueue(ctx context.Context, req registry.QueueRequest) ([]byte, error) {
	if err := req.Check(); err != nil {
		return nil, err
	}
	env := req.Run.Environment
	d := env.Deployable(req.Database)
	if d.Platform != HyperdrivePlatform {
		return nil, fmt.Errorf("cloudflare: database %s of environment %s is on %s, where no worker claims; stack queue reaches a queue on %s, a Postgres outside Cloudflare, alone",
			req.Database, env.Environment, d.Platform, HyperdrivePlatform)
	}
	v, err := envValues(env)
	if err != nil {
		return nil, err
	}
	binary, err := r.ext.migrateBinary()
	if err != nil {
		return nil, err
	}
	origin, where, err := r.ext.reachOrigin(ctx, v.accountID, *d, queuesOnTheOrigin)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "superschematic-hyperdrive-queue-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, req.Service+".json")
	if err := os.WriteFile(path, req.Document, 0o600); err != nil {
		return nil, err
	}
	log := req.Log
	if log == nil {
		log = io.Discard
	}
	_, _ = fmt.Fprintf(log, "  %s: the queue operation on %s, through Hyperdrive config %s, with %s on this machine\n", req.Service, where, hyperdriveConfigOf(*d), binary)
	cmd := exec.CommandContext(ctx, binary, "queue", "--queue", path)
	cmd.Env = withVariable(os.Environ(), databaseURLEnv, origin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := r.ext.exec(cmd); err != nil {
		why := runnerError(stderr.String())
		if why == "" {
			why = fmt.Sprintf("%s queue: %v", MigrateBinary, err)
		}
		return nil, fmt.Errorf("cloudflare: the queue operation on %s failed on %s: %s", req.Service, where, why)
	}
	return stdout.Bytes(), nil
}

// runnerError is the error the migration runner wrote to stderr: the last
// line that begins with its name, without it, else all of stderr.
func runnerError(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if rest, ok := strings.CutPrefix(lines[i], MigrateBinary+": "); ok {
			return rest
		}
	}
	return strings.TrimSpace(stderr)
}
