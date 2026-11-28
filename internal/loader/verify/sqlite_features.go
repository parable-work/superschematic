package verify

import (
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The features whose tables only some languages read on SQLite (D182). On
// SQLite an @audience table's rows keep to an audience, a JSON array a
// trigger keeps, references and attachments are rows of sub-tables whose
// triggers clear and release them, and a party's or an agent run's reads
// keep to its grants (D156). The TypeScript, Go and Rust repositories
// serve each of them there, with the access log (#611).
//
// A DB service that lists sqlite and declares one of them is refused for
// the types of a language whose repositories would read the rows without
// it there. No ORM is now, for these five, nor for @searchable, whose
// search the TypeScript, Go and Rust runtimes run there (D186, #615).

// sqliteReaders are the languages each feature is not refused for here on
// SQLite: those whose repositories serve it there, or, for @searchable,
// whose writes the index's triggers keep (D186).
var sqliteReaders = map[string][]string{
	"@audience":     {registry.LangTypeScript, registry.LangGo, registry.LangRust},
	"@references":   {registry.LangTypeScript, registry.LangGo, registry.LangRust},
	"@attachments":  {registry.LangTypeScript, registry.LangGo, registry.LangRust},
	"@accessGrants": {registry.LangTypeScript, registry.LangGo, registry.LangRust},
	"@accessLog":    {registry.LangTypeScript, registry.LangGo, registry.LangRust},
	// The search index on SQLite (D186): its triggers keep a row's index
	// row whichever ORM writes it, and the TypeScript runtime, the Go
	// schema runtime's crosstable and the Rust runtime's
	// orm::search_tables::sqlite search it (#615).
	"@searchable": {registry.LangTypeScript, registry.LangGo, registry.LangRust},
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
	var serving []string
	for _, lang := range serves {
		serving = append(serving, registry.LanguageName(lang))
	}
	r.errorf(td.Owner, "%s on %s: %s lists the sqlite dialect and enables the %s types, whose repositories read %s's rows on SQLite without it; on SQLite only the %s repositories serve %s so far (D182). Build %s for postgres alone (outputs.sql.dialects: [\"postgres\"]), or leave its %s types out",
		decorator, td.Name, schema.Name, strings.Join(others, " and "), td.Name, strings.Join(serving, ", "), decorator, schema.Name, strings.Join(others, " and "))
}

// checkSQLiteVersioned refuses a @versioned table in a DB service that
// lists the sqlite dialect and enables the types of a language whose
// repositories do not serve one there (D189). On SQLite a @versioned
// table's history is a table its row triggers keep (sqlmigrate's
// sqlite_history.go), whichever ORM writes the row, and the TypeScript, Go
// and Rust repositories read the row's version and no history (#618: the
// Go and Rust repositories on SQLite refuse their history reads). A
// service whose config the loader did not read is left unchecked.
func checkSQLiteVersioned(schema *ir.Schema, in Input, r *Result) {
	if !slices.Contains(in.Dialects, registry.SQLDialectSQLite) {
		return
	}
	var others []string
	for _, lang := range in.TypesLanguages {
		if lang != registry.LangTypeScript && lang != registry.LangGo && lang != registry.LangRust {
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
		r.errorf(td.Owner, "@versioned on %s: %s lists the sqlite dialect and enables the %s types, whose repositories read a history with Postgres's statements alone; on SQLite only the TypeScript, Go and Rust repositories serve a @versioned table so far (D189, #618). Build %s for postgres alone (outputs.sql.dialects: [\"postgres\"]), or leave its %s types out",
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
