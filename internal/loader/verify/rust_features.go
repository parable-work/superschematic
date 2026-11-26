package verify

import (
	"errors"
	"fmt"
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// The platform features of D152 to D159 the Rust outputs do not serve yet
// (D181), each refused at build time rather than dropped: a Rust types
// crate or a Rust server built from a schema that declares one would carry
// none of it, and a read, a route or a queue the TypeScript runtime serves
// would silently be missing. Per-record access (D156) is served: an
// @audience table's reads and writes, row rules through relations, grants,
// reveal and the access log. An audience on SQLite is refused for every
// language (checkAccess). So are references and attachments (D154, D185),
// on Postgres, and the runtime providers of an API's @providers slots
// (D159, D185), each a provider crate the server links.
//
// A DB service's Rust types are refused for a table feature; a Rust server
// for a listed provider with no Rust crate, an @autonomy operation, and
// the table features
// of the database its operations read (Input.Database), where the build
// loaded one. Recurrence (D152) is the core's in every language, and a
// Rust types crate writes it through the core; a Rust server is refused a
// table that composes it, since its operations, the calendar read and the
// feed are not served through the Rust adapter yet.

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
// does not carry (D181), sorted by type: @calendarSource (D152), @event,
// @notifier and @channelIdentities (D153), @searchable (D155), @party,
// @conversations and @runRequests
// (D157), and @agents (D158). With server, a Rust server's: a table that
// composes Recurrence (D152) too.
func RustTableFeatures(schema *ir.Schema, server bool) []RustFeature {
	if schema == nil {
		return nil
	}
	var out []RustFeature
	for _, name := range sortedTypes(schema) {
		td := schema.Types[name]
		add := func(feature string) {
			out = append(out, RustFeature{Owner: td.Owner, Type: td.Name, Feature: feature})
		}
		if len(td.CalendarSources) > 0 {
			add("@calendarSource (D152)")
		}
		if server {
			if _, ok := implementedConfig(td, recurrenceBehavior); ok {
				add("Recurrence (D152)")
			}
		}
		if len(td.Events) > 0 {
			add("@event (D153)")
		}
		if td.Notifier != nil {
			add("@notifier (D153)")
		}
		if td.ChannelIdentities != nil {
			add("@channelIdentities (D153)")
		}
		if td.Searchable != nil {
			add("@searchable (D155)")
		}
		if td.Party != nil {
			add("@party (D157)")
		}
		if td.Conversations != nil {
			add("@conversations (D157)")
		}
		if td.RunRequests != nil {
			add("@runRequests (D157)")
		}
		if td.Agents != nil {
			add("@agents (D158)")
		}
	}
	return out
}

// RustServerFeatures are the features of an API's schema a Rust server
// does not serve (D181): a provider the API's config lists whose directory
// holds no Rust crate of it (D185), which a Rust server cannot link, and
// an operation's @autonomy (D158), sorted by type.
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
	for _, name := range sortedTypes(schema) {
		td := schema.Types[name]
		for _, fd := range td.Fields {
			if fd != nil && fd.Autonomy != nil {
				out = append(out, RustFeature{Owner: td.Owner, Type: td.Name + "." + fd.Name, Feature: "@autonomy (D158)"})
			}
		}
	}
	return out
}

func sortedTypes(schema *ir.Schema) []string {
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
		features = RustTableFeatures(schema, false)
	case ir.SchemaKindAPI:
		features = RustServerFeatures(schema)
		for _, f := range RustTableFeatures(in.Database, true) {
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
// schema that did not come through the loader's config.
func RefuseRustTypes(schema *ir.Schema) error {
	if schema == nil || schema.Kind != ir.SchemaKindDB {
		return nil
	}
	return rustErrors(schema.Name, RustTableFeatures(schema, false), "its Rust types")
}

// RefuseRustServer is the Rust server generator's own check of an API and
// the databases it reads (D181), as checkRustFeatures refuses them.
func RefuseRustServer(api *ir.Schema, databases ...*ir.Schema) error {
	if api == nil {
		return nil
	}
	features := RustServerFeatures(api)
	for _, database := range databases {
		for _, f := range RustTableFeatures(database, true) {
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
