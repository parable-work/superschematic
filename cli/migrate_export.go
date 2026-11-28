package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/migrateplan"
)

// migrateExportFlags holds one migrate export command's flag values.
type migrateExportFlags struct {
	databaseURL string
	out         string
	namingPath  string
	force       bool
}

// newMigrateExportCmd moves a runtime database to compiled mode (D117):
// it writes the DB service a build takes the database over with.
func newMigrateExportCmd(a *app) *cobra.Command {
	flags := &migrateExportFlags{}
	cmd := &cobra.Command{
		Use:   "export --database-url <url> --out <service-dir>",
		Short: "Write a runtime database's schemas as a compiled project's DB service",
		Long: `export moves a runtime database (D117) to compiled mode. It reads the
record of the database's last publish, superschematic_runtime, and the
model the publish took the database to, and writes the DB service a build
takes the database over with:

  schema.config.ts       the service's name and kind, and its dialect
  src/<type>.schema.ts   each published document in TypeScript, as the
                         tables format --to=ts writes: development mode's
                         General documents as their tables, a Workflow as
                         status: Workflow<{ ... }>
  model.json             the model the database recorded

No row moves. The build's first plan starts from the recorded model and
is empty unless the compiler changed:

  superschematic migrate plan <service-dir> --from <service-dir>/model.json

The generated servers then take the tables over as they are, and the
database keeps superschematic_runtime, which no compiled server reads.

The migration runner reads the database, since it holds the database
drivers: superschematic-migrate record, on PATH or where
$SUPERSCHEMATIC_MIGRATE names it. --database-url is a postgres:// URL, a
SQLite file or a d1:// URL, as the runner takes it, and defaults to
$DATABASE_URL. The service directory needs the package files a schemas
root gives a service (superschematic new service writes them). A file
that exists is not replaced without --force.

Examples:
  superschematic migrate export --database-url runtime.db --out schemas/services/notes-db
  superschematic migrate export --database-url postgres://migrator@db/tasks --out ./tasks-db`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigrateExport(cmd, a, flags)
		},
	}
	cmd.Flags().StringVar(&flags.databaseURL, "database-url", "", "the runtime database: a postgres:// URL, a SQLite file or a d1:// URL (default $DATABASE_URL)")
	cmd.Flags().StringVar(&flags.out, "out", "", "the service directory to write (required)")
	cmd.Flags().StringVar(&flags.namingPath, "naming", "", "naming config file (default: the superschematic.toml found walking up from --out)")
	cmd.Flags().BoolVar(&flags.force, "force", false, "replace files that exist")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func runMigrateExport(cmd *cobra.Command, a *app, flags *migrateExportFlags) error {
	url := flags.databaseURL
	if url == "" {
		url = os.Getenv("DATABASE_URL")
	}
	if url == "" {
		return errors.New("migrate export: no database: pass --database-url or set DATABASE_URL")
	}
	runner, err := inspectRunner()
	if err != nil {
		return err
	}
	recordDoc, err := runRecord(cmd, runner, url)
	if err != nil {
		return err
	}
	modelDoc, err := runRecord(cmd, runner, url, "--model")
	if err != nil {
		return err
	}
	var record struct {
		Service   string            `json:"service"`
		ModelHash string            `json:"modelHash"`
		Documents []json.RawMessage `json:"documents"`
	}
	if err := json.Unmarshal(recordDoc, &record); err != nil {
		return fmt.Errorf("migrate export: the runner printed no record: %w", err)
	}
	model, err := migrateplan.ReadModel(modelDoc)
	if err != nil {
		return fmt.Errorf("migrate export: the runner printed no model: %w", err)
	}
	if hash, err := model.Hash(); err != nil || hash != record.ModelHash {
		return fmt.Errorf("migrate export: the record is of model %s, and the model read after it is %s: a publish landed between the two reads; export again", record.ModelHash, hash)
	}

	out, err := filepath.Abs(flags.out)
	if err != nil {
		return err
	}
	env, err := documentsEnv(a, flags.namingPath, []string{filepath.Join(out, "schema.config.ts")})
	if err != nil {
		return err
	}
	files, err := migrateplan.Export(&migrateplan.Recorded{Service: record.Service, Documents: record.Documents, Model: model}, env)
	if err != nil {
		return fmt.Errorf("migrate export: %w", err)
	}
	paths := make([]string, 0, len(files))
	for rel := range files {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	if !flags.force {
		var exist []string
		for _, rel := range paths {
			if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(rel))); err == nil {
				exist = append(exist, rel)
			}
		}
		if len(exist) > 0 {
			return fmt.Errorf("migrate export: %s already %s in %s; pass --force to replace them", strings.Join(exist, ", "), pluralVerb(len(exist), "exists", "exist"), flags.out)
		}
	}
	for _, rel := range paths {
		path := filepath.Join(out, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, files[rel], 0o644); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", filepath.Join(flags.out, filepath.FromSlash(rel)))
	}
	return nil
}

// runRecord runs the migration runner's record command on the database
// and returns what it printed.
func runRecord(cmd *cobra.Command, runner, url string, args ...string) ([]byte, error) {
	record := exec.CommandContext(cmd.Context(), runner, append([]string{"record", "--database-url", url}, args...)...)
	var stdout, stderr bytes.Buffer
	record.Stdout, record.Stderr = &stdout, &stderr
	if err := record.Run(); err != nil {
		return nil, fmt.Errorf("migrate export: the migration runner could not read the database's record: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
