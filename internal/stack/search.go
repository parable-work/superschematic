package stack

import (
	"maps"
	"slices"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// resolveSearch reads @stack's search (#740): the search database, which
// holds the search index of the environment's databases, into the
// resolved environment's Search, the database deployable that hosts it.
//
// The search database sets outputs.sql.search.environmentIndex, and is
// placed on Postgres. Every database of the stack with a @searchable table
// sets outputs.sql.search.environment, so its worker copies its index rows
// into the environment's index: a search across the environment's
// databases would miss the rows of one that keeps them to itself. A stack
// that names no search database keeps each database's own index (D155),
// and a database that sets environment there would copy its rows nowhere,
// so it is refused.
//
// It runs after place, so each database knows its dialect.
func (r *resolver) resolveSearch() string {
	members := slices.Sorted(maps.Keys(r.members))
	ref := r.stack.Search
	if ref == nil {
		for _, name := range members {
			if svc := r.services[name]; svc != nil && svc.Kind == ir.SchemaKindDB && svc.SearchEnvironment {
				r.fail(CodeInvalidStack, "DB service %s sets outputs.sql.search.environment, and the stack names no search database to copy its index rows into; name it with @stack({ search }) (#740), or leave environment out to keep its own index", name)
			}
		}
		return ""
	}
	search := r.services[ref.Name]
	if search == nil || search.Kind != ir.SchemaKindDB {
		// checkRef reported it.
		return ""
	}
	if !search.EnvironmentIndex {
		r.fail(CodeInvalidStack, "@stack search names %s, whose config does not set outputs.sql.search.environmentIndex; set it, so %s holds the environment's search index (#740)", ref.Name, ref.Name)
	}
	for _, name := range members {
		svc := r.services[name]
		if svc == nil || svc.Kind != ir.SchemaKindDB || !svc.Searchable || svc.SearchEnvironment {
			continue
		}
		r.fail(CodeInvalidStack, "DB service %s has a @searchable table, and its config does not set outputs.sql.search.environment; set it, so its worker copies its index rows into %s, the stack's search database (#740)", name, ref.Name)
	}
	host := r.deployables[r.byService[ref.Name]]
	if host == nil {
		return ""
	}
	if dialect := host.res.Dialect; dialect != "" && dialect != registry.SQLDialectPostgres {
		r.fail(CodeUnrealizable, "environment %s places %s, the stack's search database, on %s, a %s database; an environment's search index is Postgres's alone (#740)", r.envName(), ref.Name, host.res.Name, dialect)
	}
	return host.res.Name
}
