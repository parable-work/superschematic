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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
//  2. it reads the uuid of each hosted DB schema's D1 database from the
//     outputs of the graph the deploy applied (MigrationRequest.Outputs):
//     its node's `uuid`, which the Worker's d1 binding names too, else its
//     `id`, which the provider sets to the same uuid and which the program
//     exports for every node, for a database no Worker binds;
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
//
// A database on Hyperdrive (D92) is migrated on its origin, from the same
// machine, with the connection string the shell holds in OriginEnv(<db>):
// the runner first reads the database's Hyperdrive config through
// Cloudflare's API and refuses an origin it does not reach, a host, port,
// database or user other than the connection string's, and a config that
// caches reads while the database's settings do not say it may
// (reachOrigin, checkOrigin); then it runs `superschematic-migrate apply`
// per plan with steps, the connection string in DATABASE_URL, which no file
// and no command line holds. The Workers connect as the config's user, the
// migration's, so it gives no privileges either. `stack queue` reaches the
// origin the same way (queues.go, D96).

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
	_ registry.MigrationRunner            = migrationRunner{}
	_ registry.MigrationPreparer          = migrationRunner{}
	_ registry.DeployingMachineMigrations = migrationRunner{}
	_ registry.PrivilegelessMigrations    = migrationRunner{}
)

// MigratesOnTheDeployingMachine marks the runner as one the deploying
// machine runs, so a generated CI job that deploys a D1 database installs
// superschematic-migrate.
func (migrationRunner) MigratesOnTheDeployingMachine() {}

// GrantsNoPrivileges marks the runner as one whose databases have no
// roles, so a deploy whose D1 databases' connecting servers alone changed
// asks for neither the runner nor a token.
func (migrationRunner) GrantsNoPrivileges() {}

// PrepareMigrations refuses, before a deploy changes anything, a run whose
// phases could not migrate: without the runner, without a token, or
// without the origin connection string of a database on Hyperdrive.
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
	for _, d := range run.Environment.Deployables {
		if d.Platform != HyperdrivePlatform || len(d.Services) != 1 {
			continue
		}
		if r.ext.getenv(OriginEnv(d.Services[0].Name)) == "" {
			return errNoOrigin(*d, migrationsOnTheOrigin)
		}
	}
	if log != nil {
		_, _ = fmt.Fprintf(log, "  the migrations run %s on this machine\n", path)
	}
	return nil
}

// originUse is what the deploying machine runs on the origin of a database
// on Hyperdrive, as a refusal names it: a deploy's migrations (D92), or an
// operator's queue operation (D96).
type originUse struct {
	// runs says what runs on the origin: "its migrations run".
	runs string

	// across says what would happen were the variable to reach another
	// database than the config.
	across string

	// caching refuses a config that caches reads unless the database's
	// settings say it may, which only a deploy does: it rolls out Workers
	// that read through the config, where a queue operation does not.
	caching bool

	// ci says the generated CI's deploy job reads the variable from a
	// secret.
	ci bool
}

// The uses of a database's origin from the deploying machine.
var (
	migrationsOnTheOrigin = originUse{
		runs:    "its migrations run",
		across:  "a deploy would migrate one database while its Workers read another",
		caching: true,
		ci:      true,
	}
	queuesOnTheOrigin = originUse{
		runs:   "stack queue runs",
		across: "stack queue would read and change the queues of one database while its Workers drain another's",
	}
)

// errNoOrigin refuses use of the origin of d, a database on Hyperdrive
// that hosts one DB service, whose connection string the shell does not
// hold: it names the variable and what its value must reach.
func errNoOrigin(d ir.ResolvedDeployable, use originUse) error {
	env := OriginEnv(d.Services[0].Name)
	msg := fmt.Sprintf("cloudflare: database %s is on %s, and %s from this machine on its origin: set %s to the origin's connection string, postgres://<user>:<password>@<host>:<port>/<database>, with the host, port, database and user of its Hyperdrive config, %s",
		d.Name, HyperdrivePlatform, use.runs, env, hyperdriveConfigOf(d))
	if use.ci {
		msg += fmt.Sprintf(" (in CI, the deploy job reads it from the secret %s_<STACK>_<ENV>)", env)
	}
	return errors.New(msg)
}

// reachOrigin returns the connection string the shell holds for the origin
// of d, a database on Hyperdrive, and where it reaches, which a log may
// hold, once checkOrigin has found that d's Hyperdrive config reaches the
// same database as the same user. Each refusal names use.
func (e Extension) reachOrigin(ctx context.Context, account string, d ir.ResolvedDeployable, use originUse) (origin, where string, err error) {
	if len(d.Services) != 1 {
		return "", "", fmt.Errorf("cloudflare: database %s on %s hosts %d DB services; a Hyperdrive config reaches one", d.Name, HyperdrivePlatform, len(d.Services))
	}
	db := d.Services[0].Name
	origin = e.getenv(OriginEnv(db))
	if origin == "" {
		return "", "", errNoOrigin(d, use)
	}
	cloud, err := e.cloud()
	if err != nil {
		return "", "", err
	}
	if where, err = checkOrigin(ctx, cloud, account, d, db, origin, use); err != nil {
		return "", "", err
	}
	return origin, where, nil
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
	if d := env.Deployable(req.Database); d != nil && d.Platform == HyperdrivePlatform {
		return r.migrateOrigin(ctx, req, *d, plans, v, binary, log)
	}
	if req.Outputs == nil {
		return fmt.Errorf("cloudflare: migrate %s: the deploy reads the runner no outputs, where each D1 database's uuid is", req.Database)
	}
	outputs, err := req.Outputs(ctx)
	if err != nil {
		return fmt.Errorf("cloudflare: migrate %s: %w", req.Database, err)
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
		uuid := d1UUID(outputs[id])
		if uuid == "" {
			return fmt.Errorf("cloudflare: migrate %s: the outputs of %s hold no uuid of node %s, D1 database %s, which the infrastructure step creates", plan.Service, req.Run.Name(), id, name)
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

// migrateOrigin runs one phase of plans, each with steps, on the origin of
// d, a database on Hyperdrive: it checks the origin the shell names is the
// one d's Hyperdrive config reaches (reachOrigin), then runs
// `superschematic-migrate apply` per plan with the connection string in
// DATABASE_URL alone.
func (r migrationRunner) migrateOrigin(ctx context.Context, req registry.MigrationRequest, d ir.ResolvedDeployable, plans []registry.MigrationPlan, v accountValues, binary string, log io.Writer) error {
	logf := func(format string, args ...any) { _, _ = fmt.Fprintf(log, "  "+format+"\n", args...) }
	origin, where, err := r.ext.reachOrigin(ctx, v.accountID, d, migrationsOnTheOrigin)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "superschematic-hyperdrive-migrate-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for _, plan := range plans {
		if len(plan.Plan) == 0 {
			return fmt.Errorf("cloudflare: the migration plan of %s has steps and no document", plan.Service)
		}
		path := filepath.Join(dir, plan.Service+".json")
		if err := os.WriteFile(path, plan.Plan, 0o600); err != nil {
			return err
		}
		logf("%s: %d %s steps on %s, through Hyperdrive config %s", plan.Service, plan.Steps, req.Phase, where, hyperdriveConfigOf(d))
		cmd := exec.CommandContext(ctx, binary, "apply", "--plan", path, "--phase", string(req.Phase))
		cmd.Env = withVariable(os.Environ(), databaseURLEnv, origin)
		cmd.Stdout, cmd.Stderr = log, log
		if err := r.ext.exec(cmd); err != nil {
			return fmt.Errorf("cloudflare: the %s phase on %s: %s apply: %w", req.Phase, req.Database, MigrateBinary, err)
		}
	}
	return nil
}

// databaseURLEnv is the variable the runner reads a database's URL from
// when no --database-url names one.
const databaseURLEnv = "DATABASE_URL"

// checkOrigin refuses use of origin, the connection string the shell holds
// for d's DB service db, that d's Hyperdrive config does not reach: a
// config the account does not hold; an origin whose host, port, database or
// user differ from the connection string's, so the deploy would migrate one
// database, or stack queue change the queues of one, while the Workers read
// another; and, for a use that checks caching, a config that caches reads
// while d's settings do not say it may, which would answer a request with
// what its database held before the request's own write. It returns where
// the connection string reaches, its user, host and database, which a log
// may hold: never its password.
func checkOrigin(ctx context.Context, cloud Cloud, account string, d ir.ResolvedDeployable, db, origin string, use originUse) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" {
		return "", fmt.Errorf("cloudflare: %s is no postgres:// connection string with a host", OriginEnv(db))
	}
	port := 5432
	if p := u.Port(); p != "" {
		if port, err = strconv.Atoi(p); err != nil {
			return "", fmt.Errorf("cloudflare: %s names no port number", OriginEnv(db))
		}
	}
	database := strings.TrimPrefix(u.Path, "/")
	user := u.User.Username()
	where := fmt.Sprintf("database %s of %s:%d as %s", database, u.Hostname(), port, user)
	id := hyperdriveConfigOf(d)
	config, err := cloud.HyperdriveConfig(ctx, account, id)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("cloudflare: database %s's settings name Hyperdrive config %s, which account %s does not hold; create it for %s, or correct the id", d.Name, id, account, where)
	}
	if err != nil {
		return "", fmt.Errorf("cloudflare: database %s's Hyperdrive config %s: %w", d.Name, id, err)
	}
	o := config.Origin
	reached := fmt.Sprintf("database %s of %s:%d as %s", o.Database, o.Host, o.Port, o.User)
	if !strings.EqualFold(o.Host, u.Hostname()) || o.Port != port || o.Database != database || o.User != user {
		return "", fmt.Errorf("cloudflare: database %s's Hyperdrive config %s reaches %s, and %s reaches %s: %s; point the config or the variable at the same database, as the same user", d.Name, id, reached, OriginEnv(db), where, use.across)
	}
	if use.caching && !config.Caching.Disabled && !settingBool(d.Settings, "caching", false) {
		return "", fmt.Errorf("cloudflare: database %s's Hyperdrive config %s caches reads, which can answer a request with what the database held before the request's own write; turn its caching off, or set caching: true in the database's settings to keep it", d.Name, id)
	}
	return where, nil
}

// d1UUID is a D1 database's uuid among its node's outputs: `uuid`, which a
// Worker's d1 binding names and the program exports when one does, else
// `id`, the same uuid, which the program exports for every node.
func d1UUID(outputs map[string]any) string {
	for _, name := range []string{"uuid", "id"} {
		if s, ok := outputs[name].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// withToken is env with CLOUDFLARE_API_TOKEN set to token, in place of any
// value env holds: the run's, which the runner's D1 driver reads.
func withToken(env []string, token string) []string {
	return withVariable(env, TokenEnv, token)
}

// withVariable is env with name set to value, in place of any value env
// holds.
func withVariable(env []string, name, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, name+"=") {
			out = append(out, kv)
		}
	}
	return append(out, name+"="+value)
}

// migrateBinary finds the runner: where SUPERSCHEMATIC_MIGRATE says, else
// on PATH.
func (e Extension) migrateBinary() (string, error) {
	if path := e.getenv(MigrateEnv); path != "" {
		return path, nil
	}
	path, err := exec.LookPath(MigrateBinary)
	if err != nil {
		return "", fmt.Errorf("cloudflare: %s is not on PATH, and %s names no runner; the cloudflare target migrates D1 and a Postgres on Hyperdrive from this machine with it, and runs stack queue with it there: "+
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
