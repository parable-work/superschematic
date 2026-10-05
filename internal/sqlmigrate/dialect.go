package sqlmigrate

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	ir "github.com/parable-work/superschematic/ir"
)

// dialect is what one database contributes to a plan (D27). The diff is
// shared: it matches two models, finds the changes, puts each in its phase
// and order, and computes the hazards every dialect shares (destructive,
// compat, data-dependent, api-breaking, history). A dialect supplies the
// rest:
//
//   - the model: what its create.sql creates, with types in its spelling
//     and the definitions of the derived objects;
//   - how one of its types converts to another;
//   - which changes its ALTER TABLE makes in place;
//   - the SQL of each step, and the hazards only it knows: blocking, and
//     copy-table for a table it rebuilds.
//
// The planner calls canAlter on every change to a table the previous
// version has. A change the dialect can make goes to render on its own.
// When the dialect cannot make one of a phase's changes to a table, every
// change the phase makes to that table but the renames of the table and its
// columns goes to rebuild together, so a dialect whose ALTER TABLE is
// narrow (SQLite) rebuilds a table once per phase however many of its
// columns change.
type dialect interface {
	// name is the dialect's name in models and plans.
	name() Dialect

	// model resolves a DB service's schema to its model: exactly what the
	// dialect's create.sql creates for opts.
	model(schema *ir.Schema, opts sqlgen.Options) (*Model, error)

	// convert classifies changing a column from one type to another, both
	// in the dialect's spelling, with what each holds (Column.Holds).
	convert(from, to *Column) conversion

	// canAlter reports whether the dialect changes a table that already
	// exists by c in place. It is only asked about changes to such a table,
	// including dropping the foreign key that closes a reference cycle
	// among the tables the plan drops: a dialect that cannot drops the
	// cycle's tables in one step instead.
	canAlter(c *change) bool

	// render turns one change into its steps, in order. Every step has its
	// SQL, its transactional flag and recovery, and the dialect's own
	// hazards; the planner adds the change's shared hazards to the step
	// rendered.main names and sets each step's phase and index.
	render(c *change) (rendered, error)

	// rebuild turns the changes of one table in one phase, one of which
	// canAlter refused, into the steps that rebuild it by copying it: from
	// before, the table as it is when the phase reaches it, after the
	// renames, to after, the table as the phase leaves it. The planner adds
	// every change's shared hazards to the step rendered.main names.
	rebuild(before, after *Table, changes []*change) (rendered, error)
}

// rendered is a change, or a table's rebuild, as steps.
type rendered struct {
	steps []*Step
	// main indexes the step that carries the shared hazards: the one that
	// fails when the data does not allow the change, where there is one.
	main int
}

// one is a change rendered as a single step.
func one(step *Step) rendered {
	return rendered{steps: []*Step{step}}
}

// noSQL is a change that changes nothing in the database, rendered as a
// step with no statements, in every dialect: the step carries the change's
// hazards, and the runner logs it like any other.
func noSQL(c *change) rendered {
	return one(&Step{Op: string(c.op), Subject: c.subject, Statements: []string{}, Transactional: true})
}

// conversionKind classifies a column type change (D27).
type conversionKind int

const (
	// convertSame: the types are the same.
	convertSame conversionKind = iota
	// convertCoercible: the stored values are already valid in the new
	// type, so the table and its indexes are kept as they are.
	convertCoercible
	// convertRewrite: every value converts, but the table is rewritten.
	convertRewrite
	// convertMayFail: the cast fails on values the new type cannot hold.
	convertMayFail
	// convertImpossible: there is no cast, so the change cannot be planned.
	convertImpossible
)

// conversion is how a dialect converts one column type to another.
type conversion struct {
	kind conversionKind
	// lossy is set when the cast keeps a different value than it was given:
	// a narrowing cast that truncates or rounds.
	lossy bool
	// loss says what a lossy cast loses, ending the destructive hazard's
	// sentence. Empty gives the hazard its usual reason.
	loss string
}

// dialects are the dialects the planner knows, by name.
var dialects = map[Dialect]dialect{
	Postgres: postgresDialect{},
	SQLite:   sqliteDialect{},
}

// dialectFor returns the dialect named d.
func dialectFor(d Dialect) (dialect, error) {
	if impl, ok := dialects[d]; ok {
		return impl, nil
	}
	return nil, fmt.Errorf("sqlmigrate: unknown dialect %q", d)
}

// BuildModel resolves a DB service's schema to its model in dialect. opts
// are the sqlgen options a build passes (service name, dependencies, view
// owner, naming); the model holds exactly what sqlgen.Generate and
// create.sql produce for them. A schema with no tables gives a model with
// no objects. A schema that uses a feature the dialect does not support
// fails, with the feature and the dialect named.
func BuildModel(schema *ir.Schema, opts sqlgen.Options, dialect Dialect) (*Model, error) {
	impl, err := dialectFor(dialect)
	if err != nil {
		return nil, err
	}
	return impl.model(schema, opts)
}

// dialectTitles name each dialect in the header of CreateSQL's script.
var dialectTitles = map[Dialect]string{Postgres: "PostgreSQL", SQLite: "SQLite"}

// CreateSQL renders the plan from an empty database to model as one SQL
// script: SQLite's create.sql (D27). A header comment names the service and
// the model's hash; then come the statements of each step in order, each
// ending with a semicolon, with a blank line between steps.
func CreateSQL(model *Model) (string, error) {
	plan, err := Diff(nil, model, Options{})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "-- Generated %s DDL for schema: %s\n", dialectTitles[model.Dialect], model.Service)
	b.WriteString("-- This file is auto-generated. Do not edit manually.\n")
	fmt.Fprintf(&b, "-- It is the migration plan from an empty database to model %s.\n", plan.To)
	for _, step := range plan.Steps {
		b.WriteString("\n")
		for _, statement := range step.Statements {
			b.WriteString(statement)
			b.WriteString(";\n")
		}
	}
	return b.String(), nil
}
