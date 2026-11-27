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
// across tables takes from its column. A Go server serves search,
// Recurrence and the calendar on Postgres alone, so the build refuses
// them over a database that lists sqlite first (GoServerSQLiteErr), as
// Rust's refuses Recurrence and the calendar (D188): the loader lets a
// database's TypeScript and Go types declare @searchable on SQLite (D186),
// where only the TypeScript repositories search.
//
// Events and the notifier (D153) are served on Postgres (D190): the
// generated ORM of a database that uses the notifier carries its tables'
// events and notifier parts and hosts the HTTP runtime's notify package,
// which raises, routes, delivers, escalates and reads replies. The loader
// refuses the notifier on SQLite for every language (checkNotifications).
//
// Parties, the inbound path and conversations (D157) and the agent
// dispatcher (D158), with D183's trust and narrowed paths, are served on
// Postgres (D192): the generated ORM of a database with them hosts the
// HTTP runtime's inbound and agents packages through the notifier's host,
// so a database with them and no notifier is refused, and the loader
// refuses them on SQLite for every language (checkParties, checkAgents),
// which GoServerSQLiteErr holds too. An operation's @autonomy stays
// refused: it holds an agent run's calls of the API's MCP tools, and a Go
// server serves no MCP endpoint (D81, D116).
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
// first does not serve (D187, D192), or nil: a table that composes
// Recurrence, whose operations and deadline worker the Go adapter runs on
// Postgres, a calendar source, and a @searchable table, which the
// crosstable package reads and ranks with Postgres statements alone, and
// the parties' and agents' tables, which the inbound path and the
// dispatcher read by Postgres's statements. The generator calls it with
// the database's dialects for every Go server over a database.
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
		if td.Searchable != nil {
			features = append(features, name+": @searchable")
		}
		for _, mark := range orchestrationMarks(td) {
			features = append(features, name+": "+mark)
		}
	}
	if len(features) == 0 {
		return nil
	}
	return fmt.Errorf("the Go server of %s is over %s, whose DB service lists sqlite first, and serves %s on Postgres alone so far (D152, D155, D157, D158, D187, D192); list postgres first in %s's outputs.sql.dialects, or serve the API in TypeScript", api.Name, db.Name, strings.Join(features, ", "), db.Name)
}

// checkGoTables refuses the table features of schema, a Go server's
// database, that where, the server, does not serve.
func checkGoTables(schema *ir.Schema, where string, r *Result) {
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td == nil {
			continue
		}
		for i, source := range td.CalendarSources {
			if fd := sqlutil.FieldNamed(td, source.Title); fd != nil && fd.EncryptedAtRest {
				r.errorf(td.Owner, "%s does not serve @calendarSource %s titled by %s, an Encrypted<T> field (D152, D187): the Go calendar reads a title from its column, sealed; title it by a plain field, or serve the API in TypeScript (outputs.api.language: \"TYPESCRIPT\")", where, ir.CalendarSourceName(td, i), source.Title)
			}
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
// link, and an operation's @autonomy (D158, D183), which holds an agent
// run's calls of the API's MCP tools, and a Go server serves no MCP
// endpoint (D81, D116); the agents package's MCPGate is the gate for the
// day it does (D192). Provider slots are served (D184): the server
// imports each listed provider's module and builds its Providers into
// Deps.
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
				r.errorf(schema.Name, "%s does not serve @autonomy on %s.%s yet (D158, D183, D192): it holds an agent run's calls of the API's MCP tools, and a Go server serves no MCP endpoint (D81, D116); serve the API in TypeScript (outputs.api.language: \"TYPESCRIPT\"), or leave the @autonomy out", where, set.Name, op.Name)
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
