package tsreader

import (
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestImplementsTimersLoadsAsAUseOnTheTable (D103): `implements
// Timers<Ticket, C>` loads as a use the table implements, anchored on the
// type, with C read through the checker as a field's config is, before
// the uses on its fields; Timers is no trait and no heritage of the
// type's, and the table stays a table.
func TestImplementsTimersLoadsAsAUseOnTheTable(t *testing.T) {
	schema, _, err := LoadService(decoratorTestService(t, "DB", map[string]string{"src/a.schema.ts": `import { Temporal } from "superscalar";
import { Timers, Workflow } from "@superschematic/db";

const reminders = { deadlines: { remind: { start: { field: "visitAt" }, afterMs: -60000 } } } as const;

export abstract class Ticket implements Timers<Ticket, { deadlines: { respond: { start: { create: true }; afterMs: 1500; then: { transition: "closed" } } } }> {
  title: string;
  status: Workflow<{ states: ["open", "closed"]; transitions: [{ from: "open"; to: "closed" }] }>;
}

export abstract class Visit implements Timers<Visit, typeof reminders> {
  visitAt?: Temporal.DateTime;
}
`}))
	if err != nil {
		t.Fatal(err)
	}
	ticket := schema.Types["Ticket"]
	want := []ir.BehaviorRef{
		{Name: "Timers", Implements: true, Config: canonicalConfig(t, `{"deadlines": {"respond": {"start": {"create": true}, "afterMs": 1500, "then": {"transition": "closed"}}}}`)},
		{Name: "Workflow", Field: "status", Config: canonicalConfig(t, `{"states": ["open", "closed"], "transitions": [{"from": "open", "to": "closed"}]}`)},
	}
	if !reflect.DeepEqual(ticket.Behaviors, want) {
		t.Fatalf("Ticket's behaviors = %s, want %s", mustJSON(t, ticket.Behaviors), mustJSON(t, want))
	}
	if ticket.RawHeritage != nil || len(ticket.Implements) != 0 || ticket.IsEngineType() || ticket.Role != ir.RoleDBTable {
		t.Errorf("Ticket = heritage %+v, traits %+v, engine %v, role %s", ticket.RawHeritage, ticket.Implements, ticket.IsEngineType(), ticket.Role)
	}
	visit := schema.Types["Visit"]
	if want := canonicalConfig(t, `{"deadlines": {"remind": {"start": {"field": "visitAt"}, "afterMs": -60000}}}`); len(visit.Behaviors) != 1 || string(visit.Behaviors[0].Config) != string(want) {
		t.Errorf("Visit's behaviors = %s", mustJSON(t, visit.Behaviors))
	}
}

// TestImplementsTimersRefusals (D103): the reader refuses an implements
// entry of a behavior that is not written Timers<Table, Config>, a config
// the declaration's schema refuses or that is no literal type, a type that
// implements a behavior and is no DB table, and a class that inherits a
// behavior another class implements, each at its node, in these words.
func TestImplementsTimersRefusals(t *testing.T) {
	const config = `{ deadlines: { respond: { start: { create: true }; afterMs: 1500 } } }`
	for _, tc := range []struct {
		name, kind, source, want string
	}{
		{
			"the table is not the class",
			"DB",
			`export abstract class Other { id: string; }
export abstract class Ticket implements Timers<Other, ` + config + `> { title: string; }`,
			"type Ticket: implements Timers<T, C>'s first type argument is the table itself, Ticket: implements Timers<Ticket, { ... }> (D103)",
		},
		{
			"a config the schema refuses",
			"DB",
			`export abstract class Ticket implements Timers<Ticket, { deadlines: { respond: { start: { create: true }; afterMs: 1500.5 } } }> { title: string; }`,
			"type Ticket: behavior Timers config: ",
		},
		{
			"a config that is no literal",
			"DB",
			`export abstract class Ticket implements Timers<Ticket, { deadlines: { respond: { start: { create: true }; then: { transition: string } } } }> { title: string; }`,
			"type Ticket: behavior Timers config /deadlines/respond/then/transition: string is not a literal type",
		},
		{
			"a type of another kind",
			"General",
			`export abstract class Ticket implements Timers<Ticket, ` + config + `> { title: string; }`,
			"type Ticket implements Timers, and a type implements a behavior on a DB table, which Ticket is not (D103)",
		},
		{
			"a @jsonField type",
			"DB",
			`import { jsonField } from "@superschematic/schema";
@jsonField
export abstract class Ticket implements Timers<Ticket, ` + config + `> { title: string; }`,
			"type Ticket implements Timers, and a type implements a behavior on a DB table, which Ticket is not (D103)",
		},
		{
			"an inherited use",
			"DB",
			`export abstract class Base implements Timers<Base, ` + config + `> { title: string; }
export abstract class Ticket extends Base { body: string; }`,
			"type Ticket: Base implements Timers, which Ticket does not inherit: a behavior a table implements is on the table that declares it (D103)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := LoadService(decoratorTestService(t, tc.kind, map[string]string{"src/a.schema.ts": `import { Timers } from "@superschematic/db";
` + tc.source + "\n"}))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want an error holding %q", err, tc.want)
			}
		})
	}
}
