package tsreader

import (
	"encoding/json"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestABundleReadsItsPartsThroughTheChecker (D103, step 5): a table that
// implements a core bundle records the bundle and its settings, and the
// bundle's parts as its uses after its own, each config read through the
// checker from @superschematic/db's alias with the table's settings
// substituted: a setting written in place (S["resource"]), an object
// setting (S["interval"]), a number setting, and an optional setting the
// table leaves out, whose member is left out with it. Each part names the
// bundle.
func TestABundleReadsItsPartsThroughTheChecker(t *testing.T) {
	dir := decoratorTestService(t, "DB", map[string]string{"src/a.schema.ts": `import { Identity, Temporal } from "superscalar";
import { Assignment, AutoGenerate, Relation, Reservation, Workflow, key } from "@superschematic/db";
export abstract class Room {
  @key
  id: AutoGenerate<Identity.UUID>;
}
export abstract class Booking implements Reservation<Booking, { resource: "room"; interval: { start: "startsAt"; end: "endsAt" }; amount: "priceCents"; currency: "EUR" }> {
  @key
  id: AutoGenerate<Identity.UUID>;
  startsAt: Temporal.DateTime;
  endsAt: Temporal.DateTime;
  priceCents: number;
  room: Relation<Room>;
}
export abstract class Problem implements Assignment<Problem, { assign: "problems.assign" }> {
  @key
  id: AutoGenerate<Identity.UUID>;
  review: Workflow<{ states: ["open", "closed"]; transitions: [{ from: "open"; to: "closed"; by: ["seat:assignee"] }] }>;
}
`})
	schema, _, err := LoadService(dir)
	if err != nil {
		t.Fatal(err)
	}
	booking, problem := schema.Types["Booking"], schema.Types["Problem"]
	if got := mustJSON(t, booking.Bundles); got != `[{"name":"Reservation","settings":{"amount":"priceCents","currency":"EUR","interval":{"end":"endsAt","start":"startsAt"},"resource":"room"}}]` {
		t.Errorf("Booking's bundles = %s", got)
	}
	var labels []string
	for _, use := range booking.Behaviors {
		labels = append(labels, use.Name+"@"+use.Field+"#"+use.Bundle)
	}
	if got := strings.Join(labels, " "); got != "Holds@#Reservation Payments@#Reservation Timers@#Reservation Workflow@status#Reservation" {
		t.Errorf("Booking's uses = %s", got)
	}
	holds := string(booking.Behaviors[0].Config)
	payments := string(booking.Behaviors[1].Config)
	if !strings.Contains(holds, `"slot":{"interval":{"end":"endsAt","start":"startsAt"},"link":"room"}`) || !strings.Contains(payments, `"amount":{"field":"priceCents"}`) || !strings.Contains(payments, `"currency":"EUR"`) {
		t.Errorf("the configs = %s and %s", holds, payments)
	}
	if !booking.Behaviors[0].Implements || booking.Behaviors[3].Implements {
		t.Errorf("Booking's uses = %s, want the implemented parts marked and the field's not", mustJSON(t, booking.Behaviors))
	}
	if len(problem.Behaviors) != 2 || problem.Behaviors[0].Field != "review" || problem.Behaviors[0].Bundle != "" {
		t.Fatalf("Problem's uses = %s, want its own Workflow first and the bundle's Seats after", mustJSON(t, problem.Behaviors))
	}
	if got, want := string(problem.Behaviors[1].Config), `{"seats":{"assignee":{"assign":"problems.assign","max":1,"onFull":"refuse"}}}`; got != want {
		t.Errorf("Problem's Seats = %s, want %s", got, want)
	}
}

// TestAProjectsOwnBundle (D103, step 5): a bundle a schema file declares
// is no type, and a table that implements it records its parts alone,
// since the registry declares no such bundle, a setting read in a tuple's
// element among them.
func TestAProjectsOwnBundle(t *testing.T) {
	dir := decoratorTestService(t, "DB", map[string]string{"src/a.schema.ts": `import { Identity } from "superscalar";
import { AutoGenerate, Bundle, StateRules, Workflow, key, type FieldOf } from "@superschematic/db";
type Frozen<T, S extends { by: FieldOf<T> }> = Bundle<
  [StateRules<T, { states: { done: { frozen: "all"; by: [S["by"]] } } }>],
  { state: Workflow<{ states: ["open", "done"]; transitions: [{ from: "open"; to: "done" }] }> }
>;
export abstract class Task implements Frozen<Task, { by: "owner" }> {
  @key
  id: AutoGenerate<Identity.UUID>;
  owner: Identity.UUID;
}
`})
	schema, _, err := LoadService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, isType := schema.Types["Frozen"]; isType {
		t.Error("the bundle's alias loaded as a type")
	}
	task := schema.Types["Task"]
	if len(task.Bundles) != 0 {
		t.Errorf("Task's bundles = %s, want none: the registry declares no Frozen", mustJSON(t, task.Bundles))
	}
	want := []ir.BehaviorRef{
		{Name: "StateRules", Implements: true, Config: json.RawMessage(`{"states":{"done":{"by":["owner"],"frozen":"all"}}}`), Bundle: "Frozen"},
		{Name: "Workflow", Field: "state", Config: json.RawMessage(`{"states":["open","done"],"transitions":[{"from":"open","to":"done"}]}`), Bundle: "Frozen"},
	}
	if got := mustJSON(t, task.Behaviors); got != mustJSON(t, want) {
		t.Errorf("Task's uses = %s, want %s", got, mustJSON(t, want))
	}
}

// TestABundleIsRefusedWhereItCannotBe (D103, step 5): a bundle is
// implemented by a DB table, named with the table itself and its settings,
// declared as a Bundle<Parts, Fields> of carriers, and read without its
// table type parameter; a part is refused beside a use of the same
// behavior or field, and a base class's bundle is not inherited.
func TestABundleIsRefusedWhereItCannotBe(t *testing.T) {
	const head = `import { Identity } from "superscalar";
import { Assignment, AutoGenerate, Bundle, Mailbox, Seats, StateRules, Workflow, key } from "@superschematic/db";
`
	for _, tc := range []struct{ name, kind, source, want string }{
		{
			name: "an API's type",
			kind: "API",
			source: head + `export abstract class Chore implements Assignment<Chore, {}> {
  title: string;
}
`,
			want: "type Chore implements the bundle Assignment, and a bundle is implemented by a DB table, which Chore is not (D103)",
		},
		{
			name: "another table as the first type argument",
			kind: "DB",
			source: head + `export abstract class Other { id: string; }
export abstract class Chore implements Assignment<Other, {}> {
  title: string;
}
`,
			want: "type Chore: bundle Assignment's first type argument is the table itself, Chore: implements Assignment<Chore, { ... }> (D103)",
		},
		{
			name: "no settings",
			kind: "DB",
			source: head + `// @ts-ignore
export abstract class Chore implements Assignment<Chore> {
  title: string;
}
`,
			want: "type Chore: bundle Assignment takes 2 type arguments, the table and its settings: implements Assignment<Chore, { ... }> (D103)",
		},
		{
			name: "a part beside the table's own use of its behavior",
			kind: "DB",
			source: head + `export abstract class Chore implements Seats<Chore, { seats: { owner: {} } }>, Assignment<Chore, {}> {
  title: string;
}
`,
			want: "type Chore: bundle Assignment implements Seats, which the type implements already, and a table implements a behavior at most once (D103)",
		},
		{
			name: "a field part beside the table's own use on the field",
			kind: "DB",
			source: head + `export abstract class Message implements Mailbox<Message, {}> {
  createdBy: Identity.UUID;
  status: Workflow<{ states: ["a", "b"]; transitions: [{ from: "a"; to: "b" }] }>;
}
`,
			want: "type Message: bundle Mailbox anchors Workflow on field status, which the type anchors Workflow on already (D103)",
		},
		{
			name: "a part that is no carrier",
			kind: "DB",
			source: head + `type Odd<T, S extends {}> = Bundle<[string]>;
export abstract class Chore implements Odd<Chore, {}> {
  title: string;
}
`,
			want: "bundle Odd: a part is a behavior's carrier from @superschematic/db with the table and its config, as Holds<T, { ... }>",
		},
		{
			name: "a field behavior among the parts",
			kind: "DB",
			source: head + `type Odd<T, S extends {}> = Bundle<[Workflow<{ states: ["a"]; transitions: [] }>]>;
export abstract class Chore implements Odd<Chore, {}> {
  title: string;
}
`,
			want: "bundle Odd: Workflow names a value field, so it is anchored on a field: write it in Bundle's Fields, as { status: Workflow<{ ... }> }",
		},
		{
			name: "a part's config that reads the table",
			kind: "DB",
			source: head + `// @ts-ignore: the checker refuses it too
type Odd<T, S extends {}> = Bundle<[StateRules<T, { states: { a: { frozen: T } } }>]>;
export abstract class Chore implements Odd<Chore, {}> {
  title: string;
}
`,
			want: "bundle Odd's config reads its table only as a carrier's first type argument; read a setting, S[\"name\"]",
		},
		{
			name: "a tuple's element of a setting left out",
			kind: "DB",
			source: head + `// @ts-ignore: the checker refuses it too
type Odd<T, S extends { by?: Extract<keyof T, string> }> = Bundle<[StateRules<T, { states: { a: { frozen: "all"; by: [S["by"]] } } }>]>;
export abstract class Chore implements Odd<Chore, {}> {
  title: string;
}
`,
			want: "a tuple's element reads a setting the table leaves out, and an element takes a value",
		},
		{
			name: "a part's config its schema refuses",
			kind: "DB",
			source: head + `type Odd<T, S extends { max: number }> = Bundle<[Seats<T, { seats: { a: { max: S["max"] } } }>]>;
export abstract class Chore implements Odd<Chore, { max: 0 }> {
  title: string;
}
`,
			want: "type Chore: bundle Odd part Seats: behavior Seats config",
		},
		{
			name: "a base class's bundle",
			kind: "DB",
			source: head + `export abstract class Base implements Assignment<Base, {}> {
  title: string;
}
export abstract class Chore extends Base {
  done: boolean;
}
`,
			want: "type Chore: Base implements the bundle Assignment, which Chore does not inherit: a bundle a table implements is on the table that declares it (D103)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := LoadService(decoratorTestService(t, tc.kind, map[string]string{"src/a.schema.ts": tc.source}))
			if err == nil {
				t.Fatal("the service loaded")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v\nwant it to contain %q", err, tc.want)
			}
		})
	}
}
