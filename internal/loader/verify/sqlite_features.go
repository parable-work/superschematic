package verify

import (
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The features whose tables only some languages read on SQLite (D182). On
// SQLite an @audience table's rows keep to an audience the TypeScript
// repositories alone compare their callers with, and only they read and
// write references and attachments: the Go and Rust repositories on SQLite
// would read every row and none of the sub-tables. So a DB service that
// lists sqlite and declares one of them takes the types of TypeScript
// alone, until the Go and Rust repositories serve them there. The Rust
// repositories serve an audience (D181) and references and attachments
// (D185) on Postgres alone, so Rust is held to all three on SQLite.

// sqliteReaders are the languages each feature is not refused for here on
// SQLite: those whose repositories serve it there, and those another check
// refuses it for on every dialect.
var sqliteReaders = map[string][]string{
	"@audience":    {registry.LangTypeScript},
	"@references":  {registry.LangTypeScript},
	"@attachments": {registry.LangTypeScript},
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
