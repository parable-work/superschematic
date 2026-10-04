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

	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// migratePlanFlags holds one migrate plan command's flag values.
type migratePlanFlags struct {
	from       string
	fromRef    string
	renames    []string
	readers    []string
	dialect    string
	out        string
	format     string
	failOn     []string
	allow      []string
	printModel bool
	namingPath string
}

// migratePlanInputs are the flag values migrate plan parses before it
// loads anything.
type migratePlanInputs struct {
	dialect sqlmigrate.Dialect
	renames []sqlmigrate.Rename
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
hazards. superschematic-migrate (runtime/migrate) applies it.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newMigratePlanCmd(a))
	return cmd
}

func newMigratePlanCmd(a *app) *cobra.Command {
	flags := &migratePlanFlags{}
	cmd := &cobra.Command{
		Use:   "plan <service-dir>",
		Short: "Plan the migration from a previous version of a DB service to this one",
		Long: `plan compares the DB service at <service-dir> with a previous version of
it and prints the plan that migrates a database from one to the other.
Both versions resolve to models with the options a build uses; no
database is read.

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

--out writes the plan JSON the runner applies. --format prints the plan
as json, sql or markdown. --fail-on exits non-zero, after printing, when
the plan has a hazard of a listed class that no --allow names.

Examples:
  superschematic migrate plan ./schemas/services/shop-db
  superschematic migrate plan ./schemas/services/shop-db --from-ref origin/main --format markdown --fail-on destructive,compat
  superschematic migrate plan ./schemas/services/shop-db --from-ref origin/main --rename order.total=order.amount --out plan.json
  superschematic migrate plan ./schemas/services/shop-db --from applied-model.json --out plan.json
  superschematic migrate plan ./schemas/services/shop-db --print-model > model.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigratePlan(cmd, a, flags, args[0])
		},
	}
	cmd.Flags().StringVar(&flags.from, "from", "", "the previous version: another checkout of the service directory, or a model JSON file")
	cmd.Flags().StringVar(&flags.fromRef, "from-ref", "", "the previous version: the service as it is at this git ref")
	cmd.Flags().StringArrayVar(&flags.renames, "rename", nil, "a rename: old=new for a table, oldTable.oldColumn=newTable.newColumn for a column (repeatable)")
	cmd.Flags().StringArrayVar(&flags.readers, "reader", nil, "an API or General service directory outside the schemas root whose @source views read the database (repeatable)")
	cmd.Flags().StringVar(&flags.dialect, "dialect", string(sqlmigrate.Postgres), "the database dialect: postgres or sqlite")
	cmd.Flags().StringVar(&flags.out, "out", "", "write the plan JSON to this file")
	cmd.Flags().StringVar(&flags.format, "format", "sql", "print the plan to stdout as json, sql or markdown")
	cmd.Flags().StringSliceVar(&flags.failOn, "fail-on", nil, "exit non-zero when the plan has a hazard of these classes that --allow does not name (comma-separated; all for every class)")
	cmd.Flags().StringArrayVar(&flags.allow, "allow", nil, "a hazard id --fail-on lets pass (repeatable)")
	cmd.Flags().BoolVar(&flags.printModel, "print-model", false, "print the new version's model as canonical JSON and plan nothing")
	cmd.Flags().StringVar(&flags.namingPath, "naming", "", "naming config file (default <service-dir>/../../superschematic.toml)")
	return cmd
}

// parse checks the flags that need no schema, so a mistyped flag fails
// before anything loads.
func (f *migratePlanFlags) parse(cmd *cobra.Command) (migratePlanInputs, error) {
	var in migratePlanInputs
	if f.printModel {
		var given []string
		for _, name := range []string{"from", "from-ref", "rename", "reader", "out", "format", "fail-on", "allow"} {
			if cmd.Flags().Changed(name) {
				given = append(given, "--"+name)
			}
		}
		if len(given) > 0 {
			return in, fmt.Errorf("--print-model prints the new version's model and plans nothing; drop %s", strings.Join(given, ", "))
		}
	}
	if f.from != "" && f.fromRef != "" {
		return in, fmt.Errorf("--from and --from-ref both name the previous version; give one")
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
		class, _, ok := strings.Cut(id, ":")
		if !ok || !slices.Contains(sqlmigrate.HazardClasses, sqlmigrate.HazardClass(class)) {
			return in, fmt.Errorf("--allow %q is not a hazard id: want <class>:<subject>, as the plan lists it", id)
		}
	}
	return in, nil
}

// parseHazardClasses reads --fail-on: hazard class names, or all for
// every class. The result follows sqlmigrate.HazardClasses' order.
func parseHazardClasses(values []string) ([]sqlmigrate.HazardClass, error) {
	want := map[sqlmigrate.HazardClass]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "all" {
			for _, class := range sqlmigrate.HazardClasses {
				want[class] = true
			}
			continue
		}
		class := sqlmigrate.HazardClass(value)
		if !slices.Contains(sqlmigrate.HazardClasses, class) {
			names := make([]string, 0, len(sqlmigrate.HazardClasses))
			for _, class := range sqlmigrate.HazardClasses {
				names = append(names, string(class))
			}
			return nil, fmt.Errorf("--fail-on %q is not a hazard class: want %s, or all", value, strings.Join(names, ", "))
		}
		want[class] = true
	}
	var classes []sqlmigrate.HazardClass
	for _, class := range sqlmigrate.HazardClasses {
		if want[class] {
			classes = append(classes, class)
		}
	}
	return classes, nil
}

func runMigratePlan(cmd *cobra.Command, a *app, flags *migratePlanFlags, servicePath string) error {
	in, err := flags.parse(cmd)
	if err != nil {
		return err
	}
	absService, err := filepath.Abs(servicePath)
	if err != nil {
		return fmt.Errorf("resolving service path: %w", err)
	}
	if info, err := os.Stat(absService); err != nil || !info.IsDir() {
		return fmt.Errorf("service directory not found: %s", servicePath)
	}

	// The new version resolves its naming file and registry as build does.
	schemasRoot := filepath.Dir(filepath.Dir(absService))
	names, err := resolveNaming(flags.namingPath, schemasRoot)
	if err != nil {
		return err
	}
	reg, err := a.resolveRegistry(names)
	if err != nil {
		return err
	}
	current, err := openSchemaVersion(filepath.Dir(absService), names, reg)
	if err != nil {
		return err
	}
	defer current.close()
	service, err := current.serviceAt(absService)
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

	if flags.printModel {
		model, err := current.model(name, in.dialect)
		if err != nil {
			return err
		}
		canonical, err := model.CanonicalJSON()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", canonical)
		return err
	}

	prev, err := openPrevious(cmd, a, flags, current, name, in.dialect)
	if err != nil {
		return err
	}
	defer prev.close()
	readers, err := openReaders(a, flags, current)
	if err != nil {
		return err
	}

	toModel, err := current.model(name, in.dialect)
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

	plan, err := sqlmigrate.Diff(fromModel, toModel, sqlmigrate.Options{
		Renames:       in.renames,
		ReadersBefore: sqlmigrate.MergeReads(before...),
		ReadersAfter:  sqlmigrate.MergeReads(after...),
	})
	if err != nil {
		return err
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
