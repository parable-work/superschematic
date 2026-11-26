package verify

import (
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// checkNotifications checks a schema's events, notifier classes and
// channel identities (D153), once the notifications expansion filled the
// classes' fields.
//
// An @event is a DB table's: its payload and title name fields of the
// table. A @notifier part is declared once, the dispatch part on a @queue
// class and every other on a table, and a @channelIdentities class once,
// a table. Each declares no fields, which the expansion fills. A schema
// that declares any of them declares the parts routing and sending write
// (ir.NotifierRequiredParts). The notifier is built for Postgres alone:
// its worker enqueues and claims through the Postgres queue client.
func checkNotifications(schema *ir.Schema, in Input, r *Result) {
	if !schema.UsesNotifications() {
		return
	}
	var first *ir.TypeDef
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td == nil {
			continue
		}
		if len(td.Events) > 0 || td.Notifier != nil || td.ChannelIdentities != nil {
			if first == nil {
				first = td
			}
		}
		checkEvents(schema, td, r)
	}
	if schema.Kind != ir.SchemaKindDB {
		r.errorf(first.Owner, "%s: events, the notifier's tables and channel identities are a DB service's, and %s is a %s service (D153)", first.Name, schema.Name, schema.Kind)
		return
	}
	if slices.Contains(in.Dialects, registry.SQLDialectSQLite) {
		r.errorf(first.Owner, "%s: %s lists the sqlite dialect, and the notifier's worker enqueues and claims through the Postgres queue client alone so far; build %s for postgres alone (outputs.sql.dialects: [\"postgres\"]) (D153)", first.Name, schema.Name, schema.Name)
	}
	parts := map[string]*ir.TypeDef{}
	var identities *ir.TypeDef
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td == nil || (td.Notifier == nil && td.ChannelIdentities == nil) {
			continue
		}
		decorator := "@channelIdentities"
		if td.Notifier != nil {
			decorator = `@notifier("` + td.Notifier.Part + `")`
			if other := parts[td.Notifier.Part]; other != nil {
				r.errorf(td.Owner, "%s marks %s and %s; a schema has one", decorator, other.Name, td.Name)
				continue
			}
			parts[td.Notifier.Part] = td
		} else {
			if identities != nil {
				r.errorf(td.Owner, "%s marks %s and %s; a schema has one", decorator, identities.Name, td.Name)
				continue
			}
			identities = td
		}
		queue := td.Notifier != nil && td.Notifier.Part == ir.NotifierDispatch
		switch {
		case queue && td.Queue == nil:
			r.errorf(td.Owner, "%s on %s: the dispatch part is the notifier's queue; mark the class @queue() too", decorator, td.Name)
		case !queue && (td.Role != ir.RoleDBTable || td.Queue != nil):
			r.errorf(td.Owner, "%s on %s: the class is a table of the database, and no queue", decorator, td.Name)
		case td.Audience != nil || td.RowReferences != nil || td.Attachments != nil || len(td.TableBehaviors()) > 0:
			r.errorf(td.Owner, "%s on %s: the class is the notifier's own, and takes no @audience, @references, @attachments or behavior", decorator, td.Name)
		}
		for _, fd := range td.Fields {
			if fd != nil && fd.Origin != ir.OriginNotifications {
				r.errorf(td.Owner, "%s on %s: the class declares %s, and the build fills its fields; leave the class empty", decorator, td.Name, fd.Name)
				break
			}
		}
	}
	var missing []string
	for _, part := range ir.NotifierRequiredParts {
		if parts[part] == nil {
			missing = append(missing, `@notifier("`+part+`")`)
		}
	}
	if len(missing) > 0 {
		r.errorf(first.Owner, "%s uses the notifier (D153), and declares no %s: routing and sending an event write each; declare an empty class for each", schema.Name, strings.Join(missing, ", "))
	}
}

// checkEvents holds td's @event declarations to its fields.
func checkEvents(schema *ir.Schema, td *ir.TypeDef, r *Result) {
	for _, event := range td.Events {
		if event == nil {
			continue
		}
		fail := func(format string, args ...any) {
			r.errorf(td.Owner, "@event %s on %s: "+format, append([]any{event.Kind, td.Name}, args...)...)
		}
		if td.Role != ir.RoleDBTable || td.Queue != nil || td.Projection != nil {
			fail("an event is raised by a row of a table of the database, and %s is none", td.Name)
			continue
		}
		if keyScalar(td) == "" || keyCount(td) != 1 {
			fail("%s has no single key field, which a notice names its record by", td.Name)
		}
		for _, field := range event.Payload {
			if fieldNamed(td, field) == nil {
				fail("payload names %s, which is no field of %s", field, td.Name)
			}
		}
		if event.Title != "" && fieldNamed(td, event.Title) == nil {
			fail("title %s is no field of %s", event.Title, td.Name)
		}
	}
}

// keyCount is how many key fields td has.
func keyCount(td *ir.TypeDef) int {
	n := 0
	for _, fd := range td.Fields {
		if fd != nil && fd.Key {
			n++
		}
	}
	return n
}
