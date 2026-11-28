package verify

import (
	ir "github.com/parable-work/superschematic/ir"
)

// checkGoFeatures refuses, in a Go server, each platform feature of D152
// to D159 the Go outputs do not serve yet (D180): on the tables of the
// API's database, which its generated operations and its implementation
// read through the Go ORM, and on the API's own provider slots and
// operations. Before this check a Go server left each of them out with no
// word: no calendar or Recurrence operations, no events or notifier, no
// references, attachments or search, no parties, conversations, run
// requests or agents, no provider slots and no autonomy, which are all
// served now.
//
// A DB service's Go types are not refused. The DB kind generates its Go
// ORM always, and the build needs the Go types for it (outputs.types.go),
// whatever language its API is served in. The Go ORM of these tables is
// their plain repositories: the triggers and the core keep what each
// feature writes, whichever ORM writes the row, and a TypeScript server
// serves the feature. What would serve it wrongly is a Go server, which is
// refused. That holds on SQLite too: the SQLite DDL's triggers keep an
// audience, references and files whichever ORM writes the row (D182), and
// the Go repositories there keep their reads to an audience and grants and
// serve references, attachments and the access log as on Postgres
// (sqlite_features.go, #611). Per-record access itself (D156) is served
// (D180).
//
// Recurrence and the calendar (D152) and search across tables (D155) are
// served on Postgres (D187): a Go server runs a Recurrence's operations,
// deadlines and occurrences through the Go adapter, the generated ORM
// carries a table's calendar sources and search index, and the schema
// runtime's crosstable package reads them, a title of an Encrypted<T>
// field opened under the database's keys (#616). On SQLite they are served too
// (#615, part 2 of Go on SQLite): the behaviors' core runs a Recurrence's
// operations in the SQLite repositories' transactions, and the SQLite
// database's CrossTables is a crosstable.DialectHost, whose calendar and
// search crosstable writes in SQLite's words, search's words the FTS5
// table the index's triggers keep, as the TypeScript runtime's (D186).
// Rust's server still refuses Recurrence and the calendar there (D188).
//
// Events and the notifier (D153) are served on Postgres (D190): the
// generated ORM of a database that uses the notifier carries its tables'
// events and notifier parts and hosts the HTTP runtime's notify package,
// which raises, routes, delivers, escalates and reads replies.
//
// Parties, the inbound path and conversations (D157) and the agent
// dispatcher (D158), with D183's trust and narrowed paths, are served on
// Postgres (D192): the generated ORM of a database with them hosts the
// HTTP runtime's inbound and agents packages through the notifier's host,
// so a database with them and no notifier is refused. An operation's
// @autonomy is served too (#621): the Go server's MCP endpoint (#660)
// holds an agent run's call of its tool to it.
//
// On SQLite the three are served too (#618, part 3 of Go on SQLite, as
// TypeScript serves them since D189): the SQLite database's Notifications
// and Orchestration are notifications.DialectHosts, whose statements the
// notify, inbound and agents packages write in SQLite's words, and their
// queues are enqueued in the caller's transaction and claimed on SQLite.
// With parts 1 and 2 (#611, #615), a Go server over a database that lists
// sqlite first is refused nothing for its dialect, and the build's check
// of it (GoServerSQLiteErr) is gone.
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

// checkGoTables refuses the table features of schema, a Go server's
// database, that where, the server, does not serve.
func checkGoTables(schema *ir.Schema, where string, r *Result) {
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td == nil {
			continue
		}
	}
	// The inbound path and the dispatcher run through the notifier's host
	// (D192), which a database with no dispatch queue has none of.
	if schema.NotifierTable(ir.NotifierDispatch) == nil {
		for _, name := range sortedTypeNames(schema.Types) {
			td := schema.Types[name]
			if td == nil {
				continue
			}
			if marks := orchestrationMarks(td); len(marks) > 0 {
				r.errorf(td.Owner, "%s serves %s on %s through the notifier's host (D190, D192), and the database declares no @notifier(\"dispatch\") queue: declare the notifier's tables, or serve the API in TypeScript (outputs.api.language: \"TYPESCRIPT\")", where, marks[0], td.Name)
				return
			}
		}
	}
}

// orchestrationMarks are td's marks of D157's parties and conversations
// and D158's agents.
func orchestrationMarks(td *ir.TypeDef) []string {
	var out []string
	if td.Party != nil {
		out = append(out, "@party")
	}
	if td.Conversations != nil {
		out = append(out, "@conversations")
	}
	if td.RunRequests != nil {
		out = append(out, "@runRequests")
	}
	if td.Agents != nil {
		out = append(out, "@agents")
	}
	return out
}

// checkGoOperations refuses the features of schema, an API's, its Go
// server does not serve: a runtime provider its config lists whose
// directory holds no Go module (D159, D184), which a Go server cannot
// link. Provider slots are served (D184): the server imports each listed
// provider's module and builds its Providers into Deps. An operation's
// @autonomy (D158, D183) is served (#621): its MCP tool carries it, and
// the Go server's MCP endpoint (#660) holds an agent run's call of the
// tool to it through the dispatcher's MCPCallGate.
func checkGoOperations(schema *ir.Schema, where string, r *Result) {
	for _, p := range schema.Providers {
		if p != nil && p.Module == "" {
			r.errorf(schema.Name, "%s cannot link runtime provider %s (%s): its directory holds no Go module; a Go server imports a provider's Go module, whose go.mod names it and whose root package exports Definition() (D159, D184), so add one, or serve the API in TypeScript (outputs.api.language: \"TYPESCRIPT\")", where, p.Name, p.Path)
		}
	}
}
