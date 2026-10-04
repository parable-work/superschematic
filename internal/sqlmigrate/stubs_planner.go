package sqlmigrate

import (
	"errors"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	ir "github.com/parable-work/superschematic/ir"
)

// errNotImplemented marks an entry point whose implementation lands in a
// later change.
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
