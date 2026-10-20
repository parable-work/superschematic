package stack

import (
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// checkDatabaseDialects refuses a server, job or worker whose generated
// Deps could not reach its database where the environment places it
// (docs/stack-model.md, section 18.6, D64):
//   - a TypeScript one's Deps.db is the client of its database's first
//     dialect, a pg Pool for postgres and a SqlDatabase for sqlite, so its
//     database must resolve to that dialect;
//   - a Go or Rust one's Deps reach Postgres alone, the Go ORM and the Rust
//     pool, so its database must resolve to postgres;
//   - a TypeScript API over the user model authenticates with an identity
//     store on Postgres, and none over SQLite is built, so its database may
//     not list sqlite first, wherever it runs (D50).
//
// It runs after deriveEdges, on each sql edge.
func (r *resolver) checkDatabaseDialects() {
	for _, id := range sortedKeys(r.edges) {
		e := r.edges[id]
		if e.res.Kind != ir.EdgeSQL || e.to == nil || e.to.res.Dialect == "" {
			continue
		}
		from, db := e.from.res, e.res.Service.Name
		first, placed := firstDialect(r.services[db]), e.to.res.Dialect
		switch from.Language {
		case registry.APILanguageTypeScript:
			if placed != first {
				r.fail(CodeUnrealizable, "%s %s is a TypeScript %s on %s, whose DB service lists %s first, and environment %s places %s on %s, a %s database; a TypeScript API's Deps.db is the client of its database's first dialect (D64)",
					from.Kind, from.Name, from.Kind, db, first, r.envName(), db, e.to.res.Name, placed)
			}
		case "", registry.APILanguageGo, registry.APILanguageRust:
			if placed != registry.SQLDialectPostgres {
				lang := from.Language
				if lang == "" {
					lang = registry.APILanguageGo
				}
				r.fail(CodeUnrealizable, "%s %s is a %s %s on %s, and environment %s places %s on %s, a %s database; a Go or Rust API's Deps reach Postgres alone, through the Go ORM or the Rust pool (D64)",
					from.Kind, from.Name, lang, from.Kind, db, r.envName(), db, e.to.res.Name, placed)
			}
		}
	}
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name].res
		if !d.Kind.RunsCode() || d.Language != registry.APILanguageTypeScript {
			continue
		}
		for _, served := range d.Services {
			svc := r.services[served.Name]
			if svc == nil || !svc.Identity {
				continue
			}
			db, ok := r.databaseOf(svc)
			if !ok || firstDialect(r.services[db]) != registry.SQLDialectSQLite {
				continue
			}
			r.fail(CodeUnrealizable, "%s %s serves %s, which authenticates its users with the identity runtime over %s, whose DB service lists sqlite first; a TypeScript server's identity store runs on Postgres, and one over SQLite is not built (D50, D64)",
				d.Kind, d.Name, served.Name, db)
		}
	}
}
