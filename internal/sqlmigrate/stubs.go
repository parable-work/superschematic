package sqlmigrate

import (
	"errors"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	ir "github.com/parable-work/superschematic/ir"
)

// errNotImplemented marks an entry point whose implementation lands in a
// later change. Each stub below moves to its own file when it is built.
var errNotImplemented = errors.New("sqlmigrate: not implemented")

// BuildModel resolves a DB service's schema to its model in dialect. opts
// are the sqlgen options a build passes (service name, dependencies, view
// owner, naming); the model holds exactly what sqlgen.Generate and
// create.sql produce for them. A schema with no tables gives a model with
// no objects.
func BuildModel(schema *ir.Schema, opts sqlgen.Options, dialect Dialect) (*Model, error) {
	return nil, errNotImplemented
}

// Diff plans the migration from one model to another. A nil from is an
// empty database. The returned plan is sealed ([Plan.Seal]). Diff is a pure
// function of its arguments.
func Diff(from, to *Model, opts Options) (*Plan, error) {
	return nil, errNotImplemented
}

// SourceReads returns the columns of database's tables that consumer's
// @source views read: for every view whose @source target is a table type
// of database, the column behind each field, found by its origin in model
// (a relation field reads its foreign-key column; a @virtual field reads
// nothing). Reads are sorted by table, column, then via.
func SourceReads(consumer *ir.Schema, database string, model *Model) ([]Read, error) {
	return nil, errNotImplemented
}

// ParseRename parses a --rename value: "old=new" for a table, or
// "oldTable.oldColumn=newTable.newColumn" for a column.
func ParseRename(value string) (Rename, error) {
	return Rename{}, errNotImplemented
}

// SQL renders the plan as one SQL script for a reader: a header comment
// with the service, dialect and hashes, then each step's statements under
// a comment naming its index, phase, subject and hazards.
func (p *Plan) SQL() string {
	return ""
}

// Markdown renders the plan for a pull request: a hazard summary, then the
// steps by phase, each with its hazards and its SQL in a fenced block.
func (p *Plan) Markdown() string {
	return ""
}

// Unallowed returns the plan's hazards whose class is in classes and whose
// ID allow does not list, in step order.
func (p *Plan) Unallowed(classes []HazardClass, allow []string) []*Hazard {
	return nil
}
