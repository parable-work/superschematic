package verify

import (
	"slices"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// checkSearchEnvironment checks a DB service's place in its environment's
// search index (#740): outputs.sql.search.environment, whose index rows
// its worker copies into the environment's index, and
// outputs.sql.search.environmentIndex, which holds that index. Both are
// Postgres's alone: a copy holds an advisory lock of its row across the
// write, which D1 cannot hold open, and the environment index ranks by
// Postgres's words. So either is refused on a service that lists sqlite.
// environment on a service with no @searchable table copies nothing, and
// is refused as a mistake.
func checkSearchEnvironment(schema *ir.Schema, in Input, r *Result) {
	if !in.SearchEnvironment && !in.SearchEnvironmentIndex {
		return
	}
	file := ""
	for _, td := range schema.SearchableTables() {
		file = td.Owner
		break
	}
	if slices.Contains(in.Dialects, registry.SQLDialectSQLite) {
		options := []struct {
			name string
			on   bool
		}{{"environment", in.SearchEnvironment}, {"environmentIndex", in.SearchEnvironmentIndex}}
		for _, o := range options {
			if o.on {
				r.errorf(file, "outputs.sql.search.%s: %s lists sqlite, and an environment's search index is Postgres's alone (#740); a database on SQLite or D1 keeps its own index", o.name, schema.Name)
			}
		}
	}
	if in.SearchEnvironment && len(schema.SearchableTables()) == 0 {
		r.errorf(file, "outputs.sql.search.environment: %s has no @searchable table, whose index rows its worker would copy into the environment's index (#740); leave it out", schema.Name)
	}
}
