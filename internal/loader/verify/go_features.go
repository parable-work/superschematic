package verify

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// checkGoFeatures refuses, in a Go server, each platform feature of D152
// to D159 the Go outputs do not serve yet (D180): on the tables of the
// API's database, which its generated operations and its implementation
// read through the Go ORM, and on the API's own provider slots and
// operations. Before this check a Go server left each of them out with no
// word: no calendar or Recurrence operations, no events or notifier, no
// references, attachments or search, no parties, conversations, run
// requests or agents, no provider slots and no autonomy.
//
// A DB service's Go types are not refused. The DB kind generates its Go
// ORM always, and the build needs the Go types for it (outputs.types.go),
// whatever language its API is served in. The Go ORM of these tables is
// their plain repositories: the triggers and the core keep what each
// feature writes, whichever ORM writes the row, and a TypeScript server
// serves the feature. What would serve it wrongly is a Go server, which is
// refused. That holds on Postgres; on SQLite, where the Go ORM keeps no
// audience and no trigger keeps references and attachments, a database's
// Go types are refused for them (sqlite_features.go, D182). Per-record access itself (D156) is served (D180), and an
// audience on SQLite is refused for every language (checkAudience).
//
// Recurrence and the calendar (D152) and search across tables (D155) are
// served on Postgres (D187): a Go server runs a Recurrence's operations,
// deadlines and occurrences through the Go adapter, the generated ORM
// carries a table's calendar sources and search index, and the schema
// runtime's crosstable package reads them. A calendar source a Go server
// reads is refused a title of an Encrypted<T> field, which the Go read
// across tables takes from its column, and a Go server whose database
// lists sqlite first is refused Recurrence and the calendar
// (GoServerSQLiteErr), as Rust's is (D188).
//
// The Rust and SQLite ports refuse their own in files of their own; a
// later Go workstream that serves one of these removes its line here.
//
// The loader checks an API's database when it reads it, for a set or an
// operation that names its tables; the Go server's generation, which reads
// it always, checks it through GoServerDatabaseErr.
func checkGoFeatures(schema *ir.Schema, in Input, r *Result) {
	if !in.GoServer || schema.Kind != ir.SchemaKindAPI {
		return
	}
	where := "the Go server of " + schema.Name
	if in.Database != nil {
		checkGoTables(in.Database, where+", over "+in.Database.Name+",", r)
	}
	checkGoOperations(schema, where, r)
}

// GoServerDatabaseErr is the refusal of the features of db, the database
// of api, a Go server, that the Go server does not serve yet (D180), or
// nil. The generator calls it when it reads the database for the Go
// server's Deps.
func GoServerDatabaseErr(api, db *ir.Schema) error {
	if api == nil || db == nil {
		return nil
	}
	r := &Result{}
	checkGoTables(db, "the Go server of "+api.Name+", over "+db.Name+",", r)
	return r.Err()
}

// GoServerSQLiteErr is the refusal of the features of db, the database of
// api, a Go server, that a Go server on a database that lists sqlite
// first does not serve (D187), or nil: a table that composes Recurrence,
// whose operations and deadline worker the Go adapter runs on Postgres,
// and a calendar source, which the crosstable package reads on Postgres
// alone. The generator calls it with the database's dialects.
func GoServerSQLiteErr(api, db *ir.Schema, dialects []string) error {
	if api == nil || db == nil || len(dialects) == 0 || dialects[0] != registry.SQLDialectSQLite {
		return nil
	}
	var features []string
	for _, name := range sortedTypeNames(db.Types) {
		td := db.Types[name]
		if td == nil {
			continue
		}
		if usesBehavior(td, recurrenceBehavior) {
			features = append(features, name+": Recurrence")
		}
		if len(td.CalendarSources) > 0 {
			features = append(features, name+": @calendarSource")
		}
	}
	if len(features) == 0 {
		return nil
	}
	return fmt.Errorf("the Go server of %s is over %s, whose DB service lists sqlite first, and serves %s on Postgres alone so far (D152, D187); list postgres first in %s's outputs.sql.dialects, or serve the API in TypeScript", api.Name, db.Name, strings.Join(features, ", "), db.Name)
}

// goUnserved is the refusal of one feature in a Go output.
func goUnserved(r *Result, file, where, feature, decision string) {
	r.errorf(file, "%s does not serve %s yet (%s, D180): a TypeScript server does; serve the API in TypeScript (outputs.api.language: \"TYPESCRIPT\"), or leave the feature out", where, feature, decision)
}

// checkGoTables refuses the table features of schema, a Go server's
// database, that where, the server, does not serve.
func checkGoTables(schema *ir.Schema, where string, r *Result) {
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td == nil {
			continue
		}
		on := func(feature string) string { return fmt.Sprintf("%s on %s", feature, td.Name) }
		for i, source := range td.CalendarSources {
			if fd := sqlutil.FieldNamed(td, source.Title); fd != nil && fd.EncryptedAtRest {
				r.errorf(td.Owner, "%s does not serve @calendarSource %s titled by %s, an Encrypted<T> field (D152, D187): the Go calendar reads a title from its column, sealed; title it by a plain field, or serve the API in TypeScript (outputs.api.language: \"TYPESCRIPT\")", where, ir.CalendarSourceName(td, i), source.Title)
			}
		}
		if len(td.Events) > 0 {
			goUnserved(r, td.Owner, where, on("@event"), "D153")
		}
		if td.Notifier != nil {
			goUnserved(r, td.Owner, where, on("@notifier"), "D153")
		}
		if td.ChannelIdentities != nil {
			goUnserved(r, td.Owner, where, on("@channelIdentities"), "D153")
		}
		if td.Party != nil {
			goUnserved(r, td.Owner, where, on("@party"), "D157")
		}
		if td.Conversations != nil {
			goUnserved(r, td.Owner, where, on("@conversations"), "D157")
		}
		if td.RunRequests != nil {
			goUnserved(r, td.Owner, where, on("@runRequests"), "D157")
		}
		if td.Agents != nil {
			goUnserved(r, td.Owner, where, on("@agents"), "D158")
		}
	}
}

// checkGoOperations refuses the features of schema, an API's, its Go
// server does not serve: a runtime provider its config lists whose
// directory holds no Go module (D159, D184), which a Go server cannot
// link, and an operation's @autonomy (D158). Provider slots are served
// (D184): the server imports each listed provider's module and builds
// its Providers into Deps.
func checkGoOperations(schema *ir.Schema, where string, r *Result) {
	for _, p := range schema.Providers {
		if p != nil && p.Module == "" {
			r.errorf(schema.Name, "%s cannot link runtime provider %s (%s): its directory holds no Go module; a Go server imports a provider's Go module, whose go.mod names it and whose root package exports Definition() (D159, D184), so add one, or serve the API in TypeScript (outputs.api.language: \"TYPESCRIPT\")", where, p.Name, p.Path)
		}
	}
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op != nil && op.Autonomy != nil {
				goUnserved(r, schema.Name, where, fmt.Sprintf("@autonomy on %s.%s", set.Name, op.Name), "D158")
			}
		}
	}
}

// usesBehavior reports whether td composes the behavior name, by
// implements or as a behavior of the table.
func usesBehavior(td *ir.TypeDef, name string) bool {
	if _, ok := implementedConfig(td, name); ok {
		return true
	}
	for _, ref := range td.Behaviors {
		if ref.Name == name {
			return true
		}
	}
	return false
}
