package tsreader

import ir "github.com/parable-work/superschematic/ir"

// finishQueries makes the classes a @query names as its params and row
// embedded structs (D86). A plain class in a DB schema reads as a table, and
// @query's Apply sees only its own class, so the walk marks them once every
// class is read. An object type alias is an embedded struct already.
// Verification refuses a params or row type that carries what only a table
// takes (@key, @index, a relation), which a table named by mistake would.
func (w *walker) finishQueries() {
	if w.schema == nil {
		return
	}
	for _, td := range w.schema.Types {
		if td.Role != ir.RoleQuery || td.Query == nil {
			continue
		}
		for _, name := range []string{td.Query.Params, td.Query.Row} {
			if named := w.schema.Types[name]; named != nil && named.Role == ir.RoleDBTable {
				named.Role = ir.RoleEmbeddedStruct
			}
		}
	}
}
