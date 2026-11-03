package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/queryverify"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
)

// QueriesDatabaseEnv names the Postgres server `queries verify` and
// `migrate plan` prepare a DB service's queries on when --database-url
// names none: CI's, whose role may create a database. With neither, they
// start Postgres in Docker (D86).
const QueriesDatabaseEnv = "SUPERSCHEMATIC_QUERIES_DATABASE_URL"

// newQueriesCmd groups the commands of a DB service's declared queries
// (D86).
func newQueriesCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queries",
		Short: "Verify a DB service's declared queries against Postgres",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newQueriesVerifyCmd(a))
	return cmd
}

// queriesVerifyFlags holds one queries verify command's flag values.
type queriesVerifyFlags struct {
	databaseURL string
	namingPath  string
}

func newQueriesVerifyCmd(a *app) *cobra.Command {
	flags := &queriesVerifyFlags{}
	cmd := &cobra.Command{
		Use:   "verify <service-dir>",
		Short: "Prepare each @query of a DB service on Postgres and write its queries.lock",
		Long: `verify applies the DB service's create.sql to a database of its own on
Postgres, prepares each statement of each @query there, and compares what
Postgres describes with the types the schema declares: each parameter's
type, each result column's name and type, and that a column a row field
holds as never null is never null. A problem names its query. With none,
it writes queries.lock beside the service's schema config, which check and
build hold the queries to, with no database.

--database-url names a Postgres server whose role may create a database,
as CI's does; verify creates one there and drops it. Without it,
$SUPERSCHEMATIC_QUERIES_DATABASE_URL names one, or verify runs the
Postgres image stack dev runs in Docker, pgvector's when the database has a
vector column. The statements are prepared by superschematic-migrate,
which holds the driver: on PATH, or where $SUPERSCHEMATIC_MIGRATE names it.

Examples:
  superschematic queries verify ./schemas/services/finances-db
  superschematic queries verify ./schemas/services/finances-db --database-url postgres://postgres:secret@localhost:5432/postgres`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runQueriesVerify(cmd, a, flags, args[0])
		},
	}
	cmd.Flags().StringVar(&flags.databaseURL, "database-url", "", "a Postgres server whose role may create a database (default $"+QueriesDatabaseEnv+", else Postgres in Docker)")
	cmd.Flags().StringVar(&flags.namingPath, "naming", "", "naming config file (default <service-dir>/../../superschematic.toml)")
	return cmd
}

func runQueriesVerify(cmd *cobra.Command, a *app, flags *queriesVerifyFlags, servicePath string) error {
	absService, err := filepath.Abs(servicePath)
	if err != nil {
		return fmt.Errorf("resolving service path: %w", err)
	}
	if info, err := os.Stat(absService); err != nil || !info.IsDir() {
		return fmt.Errorf("service directory not found: %s", servicePath)
	}
	schemasRoot := filepath.Dir(filepath.Dir(absService))
	names, err := resolveNaming(flags.namingPath, schemasRoot)
	if err != nil {
		return err
	}
	reg, err := a.resolveRegistry(names)
	if err != nil {
		return err
	}
	version, err := openSchemaVersion(filepath.Dir(absService), names, reg)
	if err != nil {
		return err
	}
	defer version.close()
	service, err := version.serviceAt(absService)
	if err != nil {
		return err
	}
	if service.Config.Kind != ir.SchemaKindDB {
		return fmt.Errorf("queries verify: %s is of kind %s; a DB service declares queries", service.Name, service.Config.Kind)
	}
	lock, err := version.verifyQueries(cmd, service.Name, flags.databaseURL, "queries verify")
	if err != nil {
		return err
	}
	path := filepath.Join(service.Dir, queryverify.LockFile)
	if len(lock.Queries) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s declares no queries\n", service.Name)
		return err
	}
	if err := queryverify.WriteLock(path, lock); err != nil {
		return err
	}
	noun := "queries"
	if len(lock.Queries) == 1 {
		noun = "query"
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "verified %d %s of %s; wrote %s\n", len(lock.Queries), noun, service.Name, path)
	return err
}

// verifyQueries prepares the queries of the DB service name of this
// version on Postgres (queryverify.Verify) and returns their lock, or an
// error that names each query that does not hold, prefixed with command.
// The Postgres is databaseURL, else $SUPERSCHEMATIC_QUERIES_DATABASE_URL,
// else one in Docker.
func (v *schemaVersion) verifyQueries(cmd *cobra.Command, name, databaseURL, command string) (*queryverify.Lock, error) {
	schema, _, err := v.load(name)
	if err != nil {
		return nil, err
	}
	if len(schema.Queries()) == 0 {
		return &queryverify.Lock{Version: queryverify.LockVersion}, nil
	}
	createSQL, err := v.createSQL(name)
	if err != nil {
		return nil, err
	}
	runner, err := migrateRunner()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", command, err)
	}
	if databaseURL == "" {
		databaseURL = os.Getenv(QueriesDatabaseEnv)
	}
	image := local.DefaultPostgresImage
	if queryverify.NeedsVectors(schema) {
		image = local.DefaultVectorPostgresImage
	}
	stderr := cmd.ErrOrStderr()
	lock, problems, err := queryverify.Verify(cmd.Context(), queryverify.Options{
		Schema:      schema,
		CreateSQL:   createSQL,
		DatabaseURL: databaseURL,
		Image:       image,
		Runner:      runner,
		Log:         func(format string, args ...any) { logLine(stderr, format, args...) },
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", command, err)
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s: the queries of %s do not hold against its model:\n  %s", command, name, strings.Join(problems, "\n  "))
	}
	return lock, nil
}

func logLine(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format+"\n", args...)
}

// createSQL is the create.sql a build of this version writes for the DB
// service name on Postgres.
func (v *schemaVersion) createSQL(name string) (string, error) {
	schema, cfg, err := v.load(name)
	if err != nil {
		return "", err
	}
	outputs, err := registry.ParseOutputs(cfg.Outputs, v.reg)
	if err != nil {
		return "", fmt.Errorf("schema config for %s: %w", name, err)
	}
	deps := map[string]*ir.Schema{}
	for _, dep := range cfg.Dependencies {
		depSchema, _, err := v.load(dep.Name)
		if err != nil {
			return "", fmt.Errorf("load dependency %s of %s: %w", dep.Name, name, err)
		}
		deps[dep.Name] = depSchema
	}
	output, err := sqlgen.Generate(schema, generator.SQLOptions(cfg, outputs, deps, v.names))
	if err != nil {
		return "", fmt.Errorf("sql for %s: %w", name, err)
	}
	if output == nil {
		return "", nil
	}
	dir, err := os.MkdirTemp("", "superschematic-create-sql-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := sqlgen.WriteDDL(output, dir); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, "create.sql"))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// migrateRunner locates the migration runner, which prepares the queries
// since it holds the Postgres driver: where $SUPERSCHEMATIC_MIGRATE names
// it, else on PATH, as stack dev finds it.
func migrateRunner() (string, error) {
	if path := os.Getenv(local.MigrateEnv); path != "" {
		return path, nil
	}
	path, err := exec.LookPath(local.MigrateBinary)
	if err != nil {
		return "", fmt.Errorf("%s is not on PATH, and %s names no runner; it prepares the queries on Postgres: "+
			"install it from a release, or build it (cd runtime/migrate/go && go build -o \"$(go env GOPATH)/bin/%s\" ./cmd/%s): %w",
			local.MigrateBinary, local.MigrateEnv, local.MigrateBinary, local.MigrateBinary, err)
	}
	return path, nil
}
