package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/migrateplan"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// migratePlanFlags holds one migrate plan command's flag values.
type migratePlanFlags struct {
	from    string
	fromRef string
	// fromDatabase names a SQLite file built by hand to plan from, and
	// drops name the objects of it the plan drops first (D116).
	fromDatabase string
	drops        []string
	renames      []string
	readers      []string
	dialect      string
	out          string
	format       string
	failOn       []string
	allow        []string
	printModel   bool
	namingPath   string
	// databaseURL names the Postgres the new version's queries are
	// prepared on (D86).
	databaseURL string
	// documents takes the new version as the arguments, schema documents
	// of one DB service, in place of a service directory (D117).
	documents bool
	// service names the DB service development mode's General documents
	// lower into, with --documents (D117).
	service string
	// request names the file, or - for stdin, that holds a plan request
	// (migrateplan.Request), which the command answers on stdout.
	request string
}

// migratePlanInputs are the flag values migrate plan parses before it
// loads anything.
type migratePlanInputs struct {
	dialect sqlmigrate.Dialect
	renames []sqlmigrate.Rename
	drops   []sqlmigrate.Drop
	failOn  []sqlmigrate.HazardClass
}

// migrateFormats are the values of --format.
var migrateFormats = []string{"json", "sql", "markdown"}

// newMigrateCmd groups the schema migration commands (D27). The runner,
// superschematic-migrate, applies what they write.
func newMigrateCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Plan the migration of a DB service's database between two versions of its schema",
		Long: `migrate plans the change of a DB service's database from one version of
its schema to another, with no database at hand. The plan is a JSON
document of ordered steps in two phases, each with its SQL and its
hazards. superschematic-migrate (runtime/migrate) applies it.

A plan starts from another version of the schema, with no database at
hand, or from a SQLite file built by hand (--from-database). The new
version is a service directory, or one DB service's schema documents as
format --to=json writes them (--documents, --request).

export writes a runtime database's schemas and the model it recorded as
a compiled project's DB service, whose first plan from that model is
empty (D117).`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newMigratePlanCmd(a))
	cmd.AddCommand(newMigrateExportCmd(a))
	return cmd
}

func newMigratePlanCmd(a *app) *cobra.Command {
	flags := &migratePlanFlags{}
	cmd := &cobra.Command{
		Use:   "plan <service-dir> | plan --documents <document>... | plan --request <file>",
		Short: "Plan the migration from a previous version of a DB service to this one",
		Long: `plan compares the DB service at <service-dir> with a previous version of
it and prints the plan that migrates a database from one to the other.
Both versions resolve to models with the options a build uses; no
database is read, but the file --from-database names.

The previous version is another checkout of the service (--from
<service-dir>), the model a database recorded (--from <model.json>, as
superschematic-migrate status --model prints it), or the service as it
is at a git ref (--from-ref). With none, the plan starts from an empty
database. Each version loads with its dependencies resolved from its own
schemas root, as build --with-deps resolves them, and with its own
naming file when it has one.

A drop and an add is a drop and an add unless --rename names it a
rename: --rename purchase=order for a table, --rename
order.total=order.amount for a column.

The API and General services in each version's schemas root are that
version's readers: the columns their @source views read. --reader adds a
service that lives elsewhere; it counts on both sides.

--dialect sqlite plans the service's SQLite database; its
outputs.sql.dialects must list sqlite, and so must the previous
version's when it is a service directory or a git ref. A database whose
previous version was not built for SQLite plans from the model it
recorded (--from <model.json>).

--from-database plans from a SQLite file built by hand, with --dialect
sqlite: superschematic-migrate, on PATH or where $SUPERSCHEMATIC_MIGRATE
names it, opens the file to read it alone, and its tables, columns,
indexes, foreign keys, checks and triggers become the model the plan
starts from. What a table's definition holds that a model has no form
of, such as a UNIQUE written in it, is dropped by a rebuild of the table.
A view, a virtual table, a trigger that is not the schema's, and an index
of several keys with an expression among them stop the plan until --drop
names each (--drop view:order_totals), and the plan then drops them
first. With --print-model, the file's model is printed, for
superschematic-migrate adopt to record on the file before the plan is
applied; --rename and --drop are given to both.

--documents takes the new version as one DB service's schema documents,
the JSON files format --to=json writes, in place of the service
directory: every schema document of the service, and - reads more from
stdin, one JSON value after another. Development mode's General
documents, each an instance type and its behaviors, lower to the tables
of one DB service: --service names it, else the name they share. A
Workflow on the type is the field status: Workflow<{ ... }>, and the
type's other behaviors are ones its table implements. There is no
schemas root beside them, so the previous version is the model the
database recorded (--from <model.json>) or an empty database, and
nothing reads the database's tables. The documents name the service;
they carry no config, so the plan takes the dialect from --dialect and
the options a config with no outputs.sql options gives, and the naming
from --naming, else the superschematic.toml found walking up from the
first document, else the defaults. A Postgres plan of documents that
declare queries (@query) is refused, since it would not prepare them.

--request answers a plan request for a library that runs this binary: a
JSON object, in the file it names or on stdin (-), with version (1),
dialect, documents, from (the recorded model, or null for an empty
database), and optional service, renames, failOn and allow. It prints
one line of JSON: version, plan, hazards and refused, the ids of the
hazards of a failOn class that allow does not name; or, exiting 1,
version and error.

--out writes the plan JSON the runner applies. --format prints the plan
as json, sql or markdown. --fail-on exits non-zero, after printing, when
the plan has a hazard of a listed class that no --allow names.

A Postgres plan of a service that declares queries (@query) prepares
each of them on the new version's create.sql first, as queries verify
does, and fails, naming each query that no longer prepares or no longer
returns its row: on the Postgres --database-url names, else
$SUPERSCHEMATIC_QUERIES_DATABASE_URL's, else one it runs in Docker.

Examples:
  superschematic migrate plan ./schemas/services/shop-db
  superschematic migrate plan ./schemas/services/shop-db --from-ref origin/main --format markdown --fail-on destructive,compat
  superschematic migrate plan ./schemas/services/shop-db --from-ref origin/main --rename order.total=order.amount --out plan.json
  superschematic migrate plan ./schemas/services/shop-db --from applied-model.json --out plan.json
  superschematic migrate plan ./schemas/services/shop-db --print-model > model.json
  superschematic migrate plan ./schemas/services/shop-db --dialect sqlite --from-database legacy.db --drop view:order_totals --out plan.json
  superschematic migrate plan ./schemas/services/shop-db --dialect sqlite --from-database legacy.db --drop view:order_totals --print-model > legacy-model.json
  superschematic migrate plan --documents shop.schema.json --dialect sqlite --from applied-model.json --out plan.json
  superschematic format --to=json --stdout ./src/shop.schema.ts | superschematic migrate plan --documents - --from applied-model.json
  superschematic migrate plan --request - < request.json > response.json`,
		Args: func(cmd *cobra.Command, args []string) error {
			switch {
			case flags.request != "":
				if len(args) > 0 {
					return fmt.Errorf("--request holds the documents; give no arguments with it")
				}
				return nil
			case flags.documents:
				if len(args) == 0 {
					return fmt.Errorf("--documents takes the schema documents of one DB service as its arguments, or - for stdin")
				}
				return nil
			default:
				return cobra.ExactArgs(1)(cmd, args)
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case flags.request != "":
				return runMigratePlanRequest(cmd, a, flags)
			case flags.documents:
				return runMigratePlanDocuments(cmd, a, flags, args)
			default:
				return runMigratePlan(cmd, a, flags, args[0])
			}
		},
	}
	cmd.Flags().StringVar(&flags.from, "from", "", "the previous version: another checkout of the service directory, or a model JSON file")
	cmd.Flags().StringVar(&flags.fromRef, "from-ref", "", "the previous version: the service as it is at this git ref")
	cmd.Flags().StringVar(&flags.fromDatabase, "from-database", "", "the previous version: a SQLite file built by hand, read into a model (with --dialect sqlite)")
	cmd.Flags().StringArrayVar(&flags.drops, "drop", nil, "an object of the --from-database file that a model has no form of, for the plan to drop first: trigger:<name>, view:<name>, index:<name> or virtual-table:<name> (repeatable)")
	cmd.Flags().StringArrayVar(&flags.renames, "rename", nil, "a rename: old=new for a table, oldTable.oldColumn=newTable.newColumn for a column (repeatable)")
	cmd.Flags().StringArrayVar(&flags.readers, "reader", nil, "an API or General service directory outside the schemas root whose @source views read the database (repeatable)")
	cmd.Flags().StringVar(&flags.dialect, "dialect", string(sqlmigrate.Postgres), "the database dialect: postgres or sqlite")
	cmd.Flags().StringVar(&flags.out, "out", "", "write the plan JSON to this file")
	cmd.Flags().StringVar(&flags.format, "format", "sql", "print the plan to stdout as json, sql or markdown")
	cmd.Flags().StringSliceVar(&flags.failOn, "fail-on", nil, "exit non-zero when the plan has a hazard of these classes that --allow does not name (comma-separated; all for every class)")
	cmd.Flags().StringArrayVar(&flags.allow, "allow", nil, "a hazard id --fail-on lets pass (repeatable)")
	cmd.Flags().BoolVar(&flags.printModel, "print-model", false, "print the new version's model, or with --from-database the file's, as canonical JSON and plan nothing")
	addNamingFlag(cmd, &flags.namingPath, "(default <service-dir>/../../superschematic.toml; with --documents, the one found walking up from the first document)")
	cmd.Flags().BoolVar(&flags.documents, "documents", false, "take the new version as the arguments: one DB service's schema documents as format --to=json writes them, or - for stdin")
	cmd.Flags().StringVar(&flags.service, "service", "", "with --documents, the DB service development mode's General documents lower into (default: the name they share)")
	cmd.Flags().StringVar(&flags.request, "request", "", "answer the plan request in this file, or - for stdin, with one line of JSON on stdout")
	cmd.Flags().StringVar(&flags.databaseURL, "database-url", "", "a Postgres server whose role may create a database, which the new version's queries are prepared on (default $"+QueriesDatabaseEnv+", else Postgres in Docker)")
	return cmd
}

// parse checks the flags that need no schema, so a mistyped flag fails
// before anything loads.
func (f *migratePlanFlags) parse(cmd *cobra.Command) (migratePlanInputs, error) {
	var in migratePlanInputs
	if err := f.checkDocumentsFlags(cmd); err != nil {
		return in, err
	}
	if f.printModel {
		// The file's model follows the renames and the drops the plan from
		// it is given (D116), so --print-model takes them with the file.
		what, refused := "the new version's model", []string{"from", "from-ref", "rename", "drop", "reader", "out", "format", "fail-on", "allow", "database-url"}
		if f.fromDatabase != "" {
			what, refused = "the file's model", []string{"from", "from-ref", "reader", "out", "format", "fail-on", "allow", "database-url"}
		}
		var given []string
		for _, name := range refused {
			if cmd.Flags().Changed(name) {
				given = append(given, "--"+name)
			}
		}
		if len(given) > 0 {
			return in, fmt.Errorf("--print-model prints %s and plans nothing; drop %s", what, strings.Join(given, ", "))
		}
	}
	if !slices.Contains(migrateFormats, f.format) {
		return in, fmt.Errorf("--format %q: want json, sql or markdown", f.format)
	}
	switch dialect := sqlmigrate.Dialect(f.dialect); dialect {
	case sqlmigrate.Postgres, sqlmigrate.SQLite:
		in.dialect = dialect
	default:
		return in, fmt.Errorf("--dialect %q: want postgres or sqlite", f.dialect)
	}
	if err := f.checkDatabaseFlags(cmd, &in); err != nil {
		return in, err
	}
	for _, value := range f.renames {
		rename, err := sqlmigrate.ParseRename(value)
		if err != nil {
			return in, fmt.Errorf("--rename: %w", err)
		}
		in.renames = append(in.renames, rename)
	}
	classes, err := parseHazardClasses(f.failOn)
	if err != nil {
		return in, err
	}
	in.failOn = classes
	for _, id := range f.allow {
		if err := migrateplan.CheckHazardID(id); err != nil {
			return in, fmt.Errorf("--allow %w", err)
		}
	}
	return in, nil
}

// parseHazardClasses reads --fail-on: hazard class names, or all for
// every class. The result follows sqlmigrate.HazardClasses' order.
func parseHazardClasses(values []string) ([]sqlmigrate.HazardClass, error) {
	classes, err := migrateplan.ParseHazardClasses(values)
	if err != nil {
		return nil, fmt.Errorf("--fail-on %w", err)
	}
	return classes, nil
}

func runMigratePlan(cmd *cobra.Command, a *app, flags *migratePlanFlags, servicePath string) error {
	in, err := flags.parse(cmd)
	if err != nil {
		return err
	}

	// The new version resolves its naming file and registry as build does.
	svc, err := a.openService(servicePath, flags.namingPath)
	if err != nil {
		return err
	}
	current, err := openSchemaVersion(filepath.Dir(svc.dir), svc.names, svc.reg)
	if err != nil {
		return err
	}
	defer current.close()
	service, err := current.serviceAt(svc.dir)
	if err != nil {
		return err
	}
	if err := current.requireDatabase(service); err != nil {
		return err
	}
	name := service.Name
	if err := current.requireDialect(name, in.dialect); err != nil {
		return err
	}

	toModel, err := current.model(name, in.dialect)
	if err != nil {
		return err
	}
	// A file built by hand is read against the model the plan ends at
	// (D116). --print-model then prints the file's model, which adopt
	// records on the file before the plan from it is applied.
	var database *sqlmigrate.Database
	if flags.fromDatabase != "" {
		if database, err = readDatabase(cmd, flags, in, toModel); err != nil {
			return err
		}
	}
	if flags.printModel {
		model := toModel
		if database != nil {
			model = database.Model
		}
		canonical, err := model.CanonicalJSON()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", canonical)
		return err
	}

	prev := &previousVersion{label: "--from-database " + flags.fromDatabase}
	if database != nil {
		prev.model = database.Model
	} else if prev, err = openPrevious(cmd, a, flags, current, name, in.dialect); err != nil {
		return err
	}
	defer prev.close()
	readers, err := openReaders(a, flags, current)
	if err != nil {
		return err
	}

	fromModel := prev.model
	if prev.found {
		if fromModel, err = prev.version.model(name, in.dialect); err != nil {
			return fmt.Errorf("%s: %w", prev.label, err)
		}
	}

	// Expand steps are checked against the readers before the rollout,
	// contract steps against those after it.
	var before, after [][]sqlmigrate.Read
	if prev.version != nil {
		reads, err := prev.version.reads(name, fromModel)
		if err != nil {
			return fmt.Errorf("%s: %w", prev.label, err)
		}
		before = append(before, reads)
	}
	reads, err := current.reads(name, toModel)
	if err != nil {
		return err
	}
	after = append(after, reads)
	// A --reader is deployed at a version of its own, so its fields may
	// name columns of either model: a column the new version drops is only
	// in the previous one. Its reads against both count on both sides.
	for _, reader := range readers {
		readsFrom, err := sqlmigrate.SourceReads(reader, name, fromModel)
		if err != nil {
			return fmt.Errorf("--reader %s: %w", reader.Name, err)
		}
		readsTo, err := sqlmigrate.SourceReads(reader, name, toModel)
		if err != nil {
			return fmt.Errorf("--reader %s: %w", reader.Name, err)
		}
		reads := sqlmigrate.MergeReads(readsFrom, readsTo)
		before = append(before, reads)
		after = append(after, reads)
	}

	opts := sqlmigrate.Options{
		Renames:       in.renames,
		ReadersBefore: sqlmigrate.MergeReads(before...),
		ReadersAfter:  sqlmigrate.MergeReads(after...),
	}
	var plan *sqlmigrate.Plan
	if database != nil {
		plan, err = sqlmigrate.DiffDatabase(database, toModel, opts)
	} else {
		plan, err = sqlmigrate.Diff(fromModel, toModel, opts)
	}
	if err != nil {
		return err
	}
	// The new version's queries must hold against the model the plan
	// migrates to (D86).
	if in.dialect == sqlmigrate.Postgres {
		if _, err := current.verifyQueries(cmd, name, flags.databaseURL, "migrate plan"); err != nil {
			return err
		}
	}
	return writeMigratePlan(cmd, flags, in, plan)
}

// writeMigratePlan writes --out, prints --format, then applies --fail-on.
func writeMigratePlan(cmd *cobra.Command, flags *migratePlanFlags, in migratePlanInputs, plan *sqlmigrate.Plan) error {
	canonical, err := plan.CanonicalJSON()
	if err != nil {
		return err
	}
	if flags.out != "" {
		if err := os.WriteFile(flags.out, append(canonical, '\n'), 0o644); err != nil {
			return fmt.Errorf("writing --out: %w", err)
		}
	}
	var text string
	switch flags.format {
	case "json":
		text = string(canonical) + "\n"
	case "markdown":
		text = plan.Markdown()
	default:
		text = plan.SQL()
	}
	if _, err := io.WriteString(cmd.OutOrStdout(), text); err != nil {
		return err
	}
	return failOnHazards(cmd.ErrOrStderr(), plan, in.failOn, flags.allow)
}

// failOnHazards lists on w the hazards of classes that allow does not
// name, with the flags that allow them, and returns an error when there
// is one.
func failOnHazards(w io.Writer, plan *sqlmigrate.Plan, classes []sqlmigrate.HazardClass, allow []string) error {
	unallowed := plan.Unallowed(classes, allow)
	if len(unallowed) == 0 {
		return nil
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "migrate plan: %s of a --fail-on class that no --allow names:\n", countNoun(len(unallowed), "hazard"))
	for _, hazard := range unallowed {
		fmt.Fprintf(&b, "  %s\n    %s\n", hazard.ID, hazard.Reason)
	}
	b.WriteString("Review each, then allow it by its id:\n")
	for _, hazard := range unallowed {
		fmt.Fprintf(&b, "  --allow '%s'\n", hazard.ID)
	}
	if _, err := w.Write(b.Bytes()); err != nil {
		return err
	}
	return fmt.Errorf("migrate plan: %s not allowed", countNoun(len(unallowed), "hazard"))
}

// countNoun writes n with noun, plural unless n is 1.
func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// readModelFile reads a model JSON file, as superschematic-migrate status
// --model prints it, and checks it is a model of service in dialect.
func readModelFile(path, service string, dialect sqlmigrate.Dialect) (*sqlmigrate.Model, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--from: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var model sqlmigrate.Model
	if err := decoder.Decode(&model); err != nil {
		return nil, fmt.Errorf("--from %s: not a model (%w)", path, err)
	}
	switch {
	case model.Version != sqlmigrate.ModelVersion:
		return nil, fmt.Errorf("--from %s: model version %d; this compiler reads version %d", path, model.Version, sqlmigrate.ModelVersion)
	case model.Service != service:
		return nil, fmt.Errorf("--from %s: the model is %s's, not %s's", path, model.Service, service)
	case model.Dialect != dialect:
		return nil, fmt.Errorf("--from %s: the model is for %s, not --dialect %s", path, model.Dialect, dialect)
	}
	return &model, nil
}
