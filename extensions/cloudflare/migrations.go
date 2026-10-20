package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The migration runner (docs/stack-model.md, section 18.6, D64). Each phase
// a deploy runs on a D1 database runs `superschematic-migrate job` on the
// deploying machine, as the local target runs the runner, against D1's
// REST API:
//
//  1. it finds the runner: SUPERSCHEMATIC_MIGRATE, else
//     superschematic-migrate on PATH;
//  2. it looks up the uuid of each hosted DB schema's D1 database by the
//     name its node gives, with the run's parameter values in place;
//  3. it writes a job document in a temporary directory, each database at
//     `d1://<accountId>/<uuid>` with no privileges, and the plans beside it;
//  4. it runs `superschematic-migrate job --job <path>` with the run's
//     CLOUDFLARE_API_TOKEN, and the runner's output reaches the deploy's
//     log.
//
// D1 has no roles: every Worker bound to a database reads and writes all
// of it, the runner's state tables included, so a job gives no privileges,
// and a phase whose plans have no steps, which the deploy hands the runner
// only because a database's servers changed, does nothing. The D1 driver's
// lease (D27, amended) keeps two deploys apart.

// The runner, as the local target finds it.
const (
	// MigrateBinary is the runner's name on PATH.
	MigrateBinary = "superschematic-migrate"

	// MigrateEnv names the runner when it is not on PATH.
	MigrateEnv = "SUPERSCHEMATIC_MIGRATE"
)

// D1URL is the URL the runner's D1 driver reaches a database at:
// `d1://<account id>/<database uuid>`.
func D1URL(account, uuid string) string { return "d1://" + account + "/" + uuid }

// The job document `superschematic-migrate job` runs (runtime/migrate/
// README.md, "Jobs"). The target writes it; it does not import the runner,
// whose module holds the database drivers (D27).
type migrateJob struct {
	Version   int              `json:"version"`
	Phase     string           `json:"phase"`
	Databases []*migrateTarget `json:"databases"`
}

// migrateTarget is one DB schema's D1 database: no privileges, since D1
// has no roles.
type migrateTarget struct {
	Service     string `json:"service"`
	DatabaseURL string `json:"databaseUrl"`
	Plan        string `json:"plan"`
}

// migrationRunner is the cloudflare target's MigrationRunner.
type migrationRunner struct{ ext Extension }

var (
	_ registry.MigrationRunner   = migrationRunner{}
	_ registry.MigrationPreparer = migrationRunner{}
)

// PrepareMigrations refuses, before a deploy changes anything, a run whose
// phases could not migrate: without the runner, or without a token.
func (r migrationRunner) PrepareMigrations(_ context.Context, run registry.Run, log io.Writer) error {
	if err := run.Check(); err != nil {
		return err
	}
	if r.ext.getenv(TokenEnv) == "" {
		return errNoToken
	}
	path, err := r.ext.migrateBinary()
	if err != nil {
		return err
	}
	if log != nil {
		_, _ = fmt.Fprintf(log, "  the D1 migrations run %s on this machine\n", path)
	}
	return nil
}

// Migrate runs one phase of req's plans on its database's D1 databases.
func (r migrationRunner) Migrate(ctx context.Context, req registry.MigrationRequest) error {
	if err := req.Run.Check(); err != nil {
		return err
	}
	log := req.Log
	if log == nil {
		log = io.Discard
	}
	logf := func(format string, args ...any) { _, _ = fmt.Fprintf(log, "  "+format+"\n", args...) }
	var plans []registry.MigrationPlan
	for _, plan := range req.Plans {
		if plan.Steps > 0 {
			plans = append(plans, plan)
		}
	}
	if len(plans) == 0 {
		// The deploy hands a plan with no steps only for the privileges
		// of the servers that connect, which D1 does not have.
		logf("%s: no %s steps; D1 has no privileges to give", req.Database, req.Phase)
		return nil
	}
	env := req.Run.Environment
	v, err := envValues(env)
	if err != nil {
		return err
	}
	token := r.ext.getenv(TokenEnv)
	if token == "" {
		return errNoToken
	}
	binary, err := r.ext.migrateBinary()
	if err != nil {
		return err
	}
	cloud, err := r.ext.cloud()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "superschematic-d1-migrate-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	job := &migrateJob{Version: 1, Phase: string(req.Phase)}
	for _, plan := range plans {
		id := d1ID(req.Database, plan.Service)
		name, err := nodeString(env, id, "name", req.Run.Parameters)
		if err != nil {
			return err
		}
		uuid, err := cloud.D1Database(ctx, v.accountID, name)
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("cloudflare: migrate %s: account %s has no D1 database %s, which the infrastructure step creates", plan.Service, v.accountID, name)
		}
		if err != nil {
			return fmt.Errorf("cloudflare: migrate %s: find D1 database %s: %w", plan.Service, name, err)
		}
		if len(plan.Plan) == 0 {
			return fmt.Errorf("cloudflare: the migration plan of %s has steps and no document", plan.Service)
		}
		sum := sha256.Sum256(plan.Plan)
		rel := "plans/" + plan.Service + "-" + hex.EncodeToString(sum[:8]) + ".json"
		if err := os.MkdirAll(filepath.Join(dir, "plans"), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), plan.Plan, 0o600); err != nil {
			return err
		}
		job.Databases = append(job.Databases, &migrateTarget{Service: plan.Service, DatabaseURL: D1URL(v.accountID, uuid), Plan: rel})
		logf("%s: %d %s steps on D1 database %s (%s)", plan.Service, plan.Steps, req.Phase, name, uuid)
	}
	doc, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "job.json")
	if err := os.WriteFile(path, append(doc, '\n'), 0o600); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, binary, "job", "--job", path)
	cmd.Env = withToken(os.Environ(), token)
	cmd.Stdout, cmd.Stderr = log, log
	if err := r.ext.exec(cmd); err != nil {
		return fmt.Errorf("cloudflare: the %s phase on %s: %s job: %w", req.Phase, req.Database, MigrateBinary, err)
	}
	return nil
}

// withToken is env with CLOUDFLARE_API_TOKEN set to token, in place of any
// value env holds: the run's, which the runner's D1 driver reads.
func withToken(env []string, token string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, TokenEnv+"=") {
			out = append(out, kv)
		}
	}
	return append(out, TokenEnv+"="+token)
}

// migrateBinary finds the runner: where SUPERSCHEMATIC_MIGRATE says, else
// on PATH.
func (e Extension) migrateBinary() (string, error) {
	if path := e.getenv(MigrateEnv); path != "" {
		return path, nil
	}
	path, err := exec.LookPath(MigrateBinary)
	if err != nil {
		return "", fmt.Errorf("cloudflare: %s is not on PATH, and %s names no runner; the cloudflare target migrates D1 from this machine with it: "+
			"install it from a release, checked against SHA256SUMS, or build it (cd runtime/migrate/go && go build -o \"$(go env GOPATH)/bin/%s\" ./cmd/%s): %w",
			MigrateBinary, MigrateEnv, MigrateBinary, MigrateBinary, err)
	}
	return path, nil
}

// exec runs a command: through Exec, which tests set, else as it is.
func (e Extension) exec(cmd *exec.Cmd) error {
	if e.Exec != nil {
		return e.Exec(cmd)
	}
	return cmd.Run()
}

// nodeString returns a string property of a node, with the run's
// parameter values in place of its references to them.
func nodeString(env *ir.ResolvedEnvironment, id, property string, params map[string]string) (string, error) {
	res := env.Resources.Resource(id)
	if res == nil {
		return "", fmt.Errorf("cloudflare: environment %s has no node %s", env.Environment, id)
	}
	value, err := literal(res.Properties[property], params)
	if err != nil {
		return "", fmt.Errorf("cloudflare: node %s, property %s: %w", id, property, err)
	}
	return value, nil
}

// literal is a value that is a string, a parameter or their concatenation,
// with each parameter's value in its place.
func literal(v any, params map[string]string) (string, error) {
	switch v := v.(type) {
	case string:
		return v, nil
	case ir.Parameter:
		value, ok := params[string(v)]
		if !ok {
			return "", fmt.Errorf("parameter %s has no value", string(v))
		}
		return value, nil
	case ir.Concat:
		var b strings.Builder
		for _, part := range v {
			s, err := literal(part, params)
			if err != nil {
				return "", err
			}
			b.WriteString(s)
		}
		return b.String(), nil
	}
	return "", fmt.Errorf("%v is not a string", v)
}
