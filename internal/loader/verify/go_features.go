package verify

import (
	"fmt"

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
		if len(td.CalendarSources) > 0 {
			goUnserved(r, td.Owner, where, on("@calendarSource"), "D152")
		}
		if usesBehavior(td, recurrenceBehavior) {
			goUnserved(r, td.Owner, where, on("Recurrence"), "D152")
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
		if td.RowReferences != nil {
			goUnserved(r, td.Owner, where, on("@references"), "D154")
		}
		if td.Attachments != nil {
			goUnserved(r, td.Owner, where, on("@attachments"), "D154")
		}
		if td.Searchable != nil {
			goUnserved(r, td.Owner, where, on("@searchable"), "D155")
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
// server does not serve: provider slots on its @envVars classes (D159)
// and an operation's @autonomy (D158).
func checkGoOperations(schema *ir.Schema, where string, r *Result) {
	for _, name := range sortedTypeNames(schema.Types) {
		if td := schema.Types[name]; td != nil && len(td.ProviderSlots) > 0 {
			goUnserved(r, td.Owner, where, fmt.Sprintf("the provider slots of %s (@providers)", td.Name), "D159")
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
