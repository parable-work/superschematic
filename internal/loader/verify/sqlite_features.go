package verify

import (
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The features whose tables only some languages read on SQLite (D182). On
// SQLite an @audience table's rows keep to an audience the TypeScript
// repositories alone compare their callers with, only they read and write
// references and attachments, and only they keep a party's or an agent
// run's reads to its grants (D156); the Go ORM serves these and the access
// log on Postgres alone (D180, D184).
//
// A DB service that lists sqlite and declares one of them builds with Go's
// types: the DB kind writes the Go ORM always, which imports them
// (refuseORMWithoutGoTypes), so refusing them would leave such a database
// no build at all. The Go repositories on SQLite refuse each call those
// features touch at run time instead, naming why with ErrSQLiteUnsupported:
// every table's repository of a database whose reads keep to a caller, the
// access log's, and the SQLite database's References and Attachments
// (ormgen's sqliteAccessRefusal and sqliteRowFeaturesHost).
//
// Rust's types are opt-in, and its repositories would read the rows
// without them: they serve an audience (D181) and references and
// attachments (D185) on Postgres alone, and D181 leaves grant reach and
// the access log on the Rust server's SQLite. So a DB service that lists
// sqlite, declares one of the five and enables Rust's types is refused.

// sqliteReaders are the languages each feature is not refused for here on
// SQLite: TypeScript, whose repositories serve it there, and Go, whose
// repositories refuse its calls there at run time, or, for @searchable,
// whose writes the index's triggers keep (D186).
var sqliteReaders = map[string][]string{
	"@audience":     {registry.LangTypeScript, registry.LangGo},
	"@references":   {registry.LangTypeScript, registry.LangGo},
	"@attachments":  {registry.LangTypeScript, registry.LangGo},
	"@accessGrants": {registry.LangTypeScript, registry.LangGo},
	"@accessLog":    {registry.LangTypeScript, registry.LangGo},
	// The search index on SQLite (D186): its triggers keep a row's index
	// row whichever ORM writes it, and the TypeScript runtime searches it.
	// Rust's search runs on Postgres alone (D188), and a Go server is
	// refused search over a SQLite database (go_features.go), so Go's
	// types, which the DB kind builds always, are served beside it.
	"@searchable": {registry.LangTypeScript, registry.LangGo},
}

// refuseSQLiteReaders refuses decorator on td in a DB service that lists the
// sqlite dialect and enables the types of a language whose repositories do
// not serve it there. A service whose config the loader did not read
// (in.TypesLanguages nil) is left unchecked.
func refuseSQLiteReaders(schema *ir.Schema, in Input, td *ir.TypeDef, decorator string, r *Result) {
	if !slices.Contains(in.Dialects, registry.SQLDialectSQLite) {
		return
	}
	serves := sqliteReaders[decorator]
	var others []string
	for _, lang := range in.TypesLanguages {
		if !slices.Contains(serves, lang) {
			others = append(others, registry.LanguageName(lang))
		}
	}
	if len(others) == 0 {
		return
	}
	r.errorf(td.Owner, "%s on %s: %s lists the sqlite dialect and enables the %s types, whose repositories read %s's rows on SQLite without it; on SQLite only the TypeScript repositories serve %s so far (D182). Build %s for postgres alone (outputs.sql.dialects: [\"postgres\"]), or leave its %s types out",
		decorator, td.Name, schema.Name, strings.Join(others, " and "), td.Name, decorator, schema.Name, strings.Join(others, " and "))
}

// checkSQLiteVersioned refuses a @versioned table in a DB service that
// lists the sqlite dialect and enables the Go or the Rust types (D189). On
// SQLite a @versioned table's history is a table its row triggers keep
// (sqlmigrate's sqlite_history.go), whichever ORM writes the row, and the
// TypeScript repositories read the row's version; the Go and Rust
// repositories read a history with Postgres's statements alone. A service
// whose config the loader did not read is left unchecked.
func checkSQLiteVersioned(schema *ir.Schema, in Input, r *Result) {
	if !slices.Contains(in.Dialects, registry.SQLDialectSQLite) {
		return
	}
	var others []string
	for _, lang := range in.TypesLanguages {
		if lang != registry.LangTypeScript {
			others = append(others, registry.LanguageName(lang))
		}
	}
	if len(others) == 0 {
		return
	}
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td == nil || !td.Versioned || td.Role != ir.RoleDBTable {
			continue
		}
		r.errorf(td.Owner, "@versioned on %s: %s lists the sqlite dialect and enables the %s types, whose repositories read a history with Postgres's statements alone; on SQLite only the TypeScript repositories serve a @versioned table so far (D189). Build %s for postgres alone (outputs.sql.dialects: [\"postgres\"]), or leave its %s types out",
			td.Name, schema.Name, strings.Join(others, " and "), schema.Name, strings.Join(others, " and "))
	}
}

// refuseSQLiteMergeRoutes refuses a @tableOperation merge (D147) of a
// TypeScript server whose database lists sqlite first: the server then
// opens its database on SQLite (its Deps.db a SqlDatabase), where the
// TypeScript repositories refuse mergeInto (D182), so the route would
// answer only that refusal. The Go and Rust servers serve no merge route,
// and the Rust repositories' merge_into runs on SQLite, so neither is
// refused here. An API whose config, or whose database's, the loader did
// not read (in.DatabaseDialects nil) is left unchecked.
func refuseSQLiteMergeRoutes(schema *ir.Schema, in Input, db string, owner string, op *ir.FieldDef, r *Result) {
	if !in.TypeScriptServer || len(in.DatabaseDialects) == 0 || in.DatabaseDialects[0] != registry.SQLDialectSQLite {
		return
	}
	if op.TableOperation == nil || op.TableOperation.Kind != ir.TableOperationMerge {
		return
	}
	r.errorf("", "%s: a merge of %s (D147) on %s, whose first dialect is sqlite, so %s's TypeScript server opens it on SQLite, where the TypeScript repositories refuse mergeInto so far (D182). List postgres first in %s's outputs.sql.dialects, or leave the merge out of %s",
		owner, op.TableOperation.Type, db, schema.Name, db, schema.Name)
}
