package verify

import (
	"errors"
	"fmt"
	"slices"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The platform features of D152 to D159 the Rust outputs do not serve yet
// (D181), each refused at build time rather than dropped: a Rust types
// crate or a Rust server built from a schema that declares one would carry
// none of it, and a read, a route or a queue the TypeScript runtime serves
// would silently be missing. Per-record access (D156) is served: an
// @audience table's reads and writes, row rules through relations, grants,
// reveal and the access log, on Postgres and on SQLite, where an audience
// is the JSON array a trigger keeps (D182, #611). References and
// attachments (D154, D185) are served on both too, and the runtime
// providers of an API's @providers slots (D159, D185), each a provider
// crate the server links. So are Recurrence and the calendar (D152) and
// search across tables (D155), on Postgres (D188) and on SQLite (#615): a
// Rust server serves a Recurrence's operations, deadlines and occurrences
// through the Rust adapter, on either dialect, the repositories of both
// carry a table's calendar sources and search index, and the runtime's
// orm::calendar and orm::search_tables read them, on SQLite through their
// sqlite modules, the search over the FTS5 table beside the index (D186).
// Events and notifications (D153) are served on Postgres (D191) and on
// SQLite (#618): each dialect's definition carries each table's events and
// what it is to the notifier, and the runtime's notify module raises,
// routes, delivers and reads replies over them, through orm::store on
// either dialect, as the TypeScript (D189) and the Go (#719) runtimes do.
// Parties, inbound messages and conversations (D157) and the agent
// orchestration (D158) are served on Postgres (D193) and on SQLite (#618):
// the definitions carry each table's party, conversations and agents part,
// the runtime's parties, inbound and agents modules read them, their
// queues' workers claim on either dialect, and a Rust server's MCP
// endpoint holds an agent run's calls to an operation's @autonomy. A Rust
// server over a database whose first dialect is sqlite is still refused
// them by rustrestgen: its wiring of the parts is Postgres's (#707).
//
// A Rust server is refused a listed provider with no Rust crate.

// RustFeature is one feature a schema declares that the Rust outputs do
// not serve yet (D181): the type it is on and what it is.
type RustFeature struct {
	// Owner is the schema file of the type that declares it.
	Owner string
	// Type is the type, and Feature the feature with its decision.
	Type    string
	Feature string
}

// RustTableFeatures are the features of schema's tables a Rust types crate
// does not carry (D181), sorted by type, on a database that lists the
// sqlite dialect when sqlite: none since #618, which serves @party,
// @conversations and @runRequests (D157) and @agents (D158) on SQLite as on
// Postgres (D193). A Rust server is refused the same table features of its
// database.
func RustTableFeatures(schema *ir.Schema, sqlite bool) []RustFeature {
	_, _ = schema, sqlite
	return nil
}

// RustServerFeatures are the features of an API's schema a Rust server
// does not serve (D181): a provider the API's config lists whose directory
// holds no Rust crate of it (D185), which a Rust server cannot link. An
// operation's @autonomy is served since D193: the MCP tool's record
// carries it, and the agent dispatcher's gate holds a run's call to it.
func RustServerFeatures(schema *ir.Schema) []RustFeature {
	if schema == nil {
		return nil
	}
	var out []RustFeature
	for _, p := range schema.Providers {
		if p != nil && p.Crate == "" {
			out = append(out, RustFeature{Type: "provider " + p.Name, Feature: fmt.Sprintf("a provider of no Rust crate (%s declares it in its package.json alone; a Rust server links a crate whose Cargo.toml declares it under [package.metadata.superschematic.provider], D185)", p.Path)})
		}
	}
	return out
}

// rustRefusal is the message of a feature the Rust outputs refuse.
func rustRefusal(f RustFeature, service, output string) string {
	return fmt.Sprintf("type %s: %s is served by the TypeScript runtime alone so far, and %s builds Rust (%s); build %s without Rust, or leave the feature out (D181)", f.Type, f.Feature, service, output, service)
}

// checkRustFeatures refuses each feature of D152 to D159 the Rust outputs
// do not serve, for a service whose config builds Rust (Input.RustOutput):
// a DB service's Rust types, and an API's Rust server, with the table
// features of its database.
func checkRustFeatures(schema *ir.Schema, in Input, r *Result) {
	if in.RustOutput == "" {
		return
	}
	var features []RustFeature
	switch schema.Kind {
	case ir.SchemaKindDB:
		features = RustTableFeatures(schema, slices.Contains(in.Dialects, registry.SQLDialectSQLite))
	case ir.SchemaKindAPI:
		features = RustServerFeatures(schema)
		// The database's dialects are its own config's, which an API's
		// build does not read here: rustrestgen refuses a database that
		// lists sqlite first (checkSQLiteDeps).
		for _, f := range RustTableFeatures(in.Database, false) {
			f.Type = in.Database.Name + "." + f.Type
			f.Owner = ""
			features = append(features, f)
		}
	}
	for _, f := range features {
		r.errorf(f.Owner, "%s", rustRefusal(f, schema.Name, in.RustOutput))
	}
}

// RefuseRustTypes is the Rust types generator's own check of a DB
// schema (D181): the table features checkRustFeatures refuses, for a
// schema that did not come through the loader's config, on SQLite when
// sqlite.
func RefuseRustTypes(schema *ir.Schema, sqlite bool) error {
	if schema == nil || schema.Kind != ir.SchemaKindDB {
		return nil
	}
	return rustErrors(schema.Name, RustTableFeatures(schema, sqlite), "its Rust types")
}

// RefuseRustServer is the Rust server generator's own check of an API and
// the databases it reads (D181), as checkRustFeatures refuses them.
func RefuseRustServer(api *ir.Schema, databases ...*ir.Schema) error {
	if api == nil {
		return nil
	}
	features := RustServerFeatures(api)
	for _, database := range databases {
		for _, f := range RustTableFeatures(database, false) {
			f.Type = database.Name + "." + f.Type
			features = append(features, f)
		}
	}
	return rustErrors(api.Name, features, "its Rust server")
}

func rustErrors(service string, features []RustFeature, output string) error {
	var errs []error
	for _, f := range features {
		errs = append(errs, errors.New(rustRefusal(f, service, output)))
	}
	return errors.Join(errs...)
}
