package verify

import (
	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// checkMergeable validates the Mergeable trait (D147): a table whose
// repository folds one row into another is a DB table that the ORMs keep
// rows of by a key. It is refused in a schema of another kind, on a type of
// another role, a @jsonField type and a base class, which get no table, as
// the user model's traits are, and on what keeps its rows elsewhere or by
// more than their key: an engine type, whose instances the engine holds; a
// queue, whose rows are messages; a view; and a version graph's root or
// member, whose rows a commit versions.
func checkMergeable(schema *ir.Schema, r *Result) {
	bases := codegen.BaseTypeNames(schema)
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if !td.Mergeable {
			continue
		}
		if !checkIdentityPlacement(schema, td, "Mergeable", bases, r) {
			continue
		}
		switch {
		case td.IsEngineType():
			r.errorf(td.Owner, "%s: the Mergeable trait is on an engine type, whose instances the engine keeps: mergeInto repoints the relations of tables (D147)", td.Name)
		case td.Queue != nil:
			r.errorf(td.Owner, "%s: the Mergeable trait is on a queue, whose rows are messages no relation references (D147)", td.Name)
		case td.Source != nil || td.Projection != nil:
			r.errorf(td.Owner, "%s: the Mergeable trait is on a view, which has no rows of its own to merge (D147)", td.Name)
		case td.VersionGraph != nil || td.GraphMember != nil:
			r.errorf(td.Owner, "%s: the Mergeable trait is on a version graph's table, whose rows a commit versions: mergeInto writes rows in place (D147)", td.Name)
		}
	}
}
