package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/stack/local"
)

// previousFlags are the flags that name the version a plan starts from.
// A plan takes one of them at most.
var previousFlags = []string{"from", "from-ref", "from-database"}

// checkDatabaseFlags checks --from-database and --drop against the other
// flags, before anything loads (D116): the three flags that name the
// previous version exclude one another, a file is read for --dialect
// sqlite alone, and --drop names an object of the file.
func (f *migratePlanFlags) checkDatabaseFlags(cmd *cobra.Command, in *migratePlanInputs) error {
	var given []string
	for _, name := range previousFlags {
		if cmd.Flags().Changed(name) {
			given = append(given, "--"+name)
		}
	}
	switch len(given) {
	case 0, 1:
	case 2:
		return fmt.Errorf("%s and %s both name the previous version; give one", given[0], given[1])
	default:
		return fmt.Errorf("%s and %s all name the previous version; give one", strings.Join(given[:len(given)-1], ", "), given[len(given)-1])
	}
	if f.fromDatabase == "" {
		if len(f.drops) > 0 {
			return fmt.Errorf("--drop names an object of the file --from-database reads; give --from-database with it")
		}
		return nil
	}
	if in.dialect != sqlmigrate.SQLite {
		if cmd.Flags().Changed("dialect") {
			return fmt.Errorf("--from-database reads a SQLite file, and --dialect is %s; give --dialect sqlite with it", in.dialect)
		}
		return fmt.Errorf("--from-database reads a SQLite file; give --dialect sqlite with it")
	}
	for _, value := range f.drops {
		drop, err := sqlmigrate.ParseDrop(value)
		if err != nil {
			return fmt.Errorf("--drop: %w", err)
		}
		in.drops = append(in.drops, drop)
	}
	return nil
}

// readDatabase reads the SQLite file --from-database names into a model,
// against schema, the model of the service being planned (D116,
// sqlmigrate.ReadDatabase). The migration runner reads the file, since it
// holds the SQLite driver, which stays out of the compiler's module (D27,
// Apply): `superschematic-migrate inspect` opens the file to read it alone
// and prints what its catalog holds, and the compiler turns that into the
// model. A file that does not exist is refused here, so nothing creates
// one.
func readDatabase(cmd *cobra.Command, flags *migratePlanFlags, in migratePlanInputs, schema *sqlmigrate.Model) (*sqlmigrate.Database, error) {
	label := "--from-database " + flags.fromDatabase
	path, err := filepath.Abs(flags.fromDatabase)
	if err != nil {
		return nil, fmt.Errorf("--from-database: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("--from-database: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s: a directory, not a SQLite file", label)
	}
	runner, err := inspectRunner()
	if err != nil {
		return nil, err
	}
	inspect := exec.CommandContext(cmd.Context(), runner, "inspect", "--database-url", path)
	var stdout, stderr bytes.Buffer
	inspect.Stdout, inspect.Stderr = &stdout, &stderr
	if err := inspect.Run(); err != nil {
		return nil, fmt.Errorf("%s: the migration runner could not read the file: %v: %s", label, err, strings.TrimSpace(stderr.String()))
	}
	inspection, err := sqlmigrate.ReadInspection(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	database, err := sqlmigrate.ReadDatabase(inspection, schema, in.renames, in.drops)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	return database, nil
}

// inspectRunner locates the migration runner, which reads the file:
// where $SUPERSCHEMATIC_MIGRATE names it, else on PATH, as stack dev and
// queries verify find it.
func inspectRunner() (string, error) {
	if path := os.Getenv(local.MigrateEnv); path != "" {
		return path, nil
	}
	path, err := exec.LookPath(local.MigrateBinary)
	if err != nil {
		return "", fmt.Errorf("%s is not on PATH, and %s names no runner; it reads the file --from-database names, since it holds the SQLite driver: "+
			"install it from a release, or build it (cd runtime/migrate/go && go build -o \"$(go env GOPATH)/bin/%s\" ./cmd/%s): %w",
			local.MigrateBinary, local.MigrateEnv, local.MigrateBinary, local.MigrateBinary, err)
	}
	return path, nil
}
