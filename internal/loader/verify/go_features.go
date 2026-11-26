package verify

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// checkGoFeatures refuses, in a service the build generates Go for, each
// platform feature of D152 to D159 the Go outputs do not serve yet (D180):
// the Go ORM of a DB service with Go types, and the Go server of an API,
// whose database's tables it serves. Before this check the Go generators
// left each of them out with no word: a Go ORM with no calendar source,
// no Recurrence operations, no events, no notifier or channel identities,
// no references or attachments, no search index, no parties, no
// conversations or run requests, and no agents; a Go server with no
// provider slots and no autonomy. Per-record access itself (D156) is
// served (D180), and an audience on SQLite is refused for every language
// (checkAudience).
//
// The Rust and SQLite ports refuse their own in files of their own; a
// later Go workstream that serves one of these removes its line here.
func checkGoFeatures(schema *ir.Schema, in Input, r *Result) {
	switch {
	case in.GoORM && schema.Kind == ir.SchemaKindDB:
		checkGoTables(schema, "the Go ORM of "+schema.Name, r)
	case in.GoServer && schema.Kind == ir.SchemaKindAPI:
		where := "the Go server of " + schema.Name
		if in.Database != nil {
			checkGoTables(in.Database, where+", over "+in.Database.Name+",", r)
		}
		checkGoOperations(schema, where, r)
	}
}

// goUnserved is the refusal of one feature in a Go output.
func goUnserved(r *Result, file, where, feature, decision string) {
	r.errorf(file, "%s does not serve %s yet (%s, D180): the TypeScript server and repositories do; turn the Go output off (outputs.types.go, or outputs.api.language: \"TYPESCRIPT\"), or leave the feature out", where, feature, decision)
}

// checkGoTables refuses the table features of schema, a DB service's,
// that where, a Go output, does not serve.
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
