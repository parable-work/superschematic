package tsreader

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// canonicalConfig is a config as the data forms store it: written as JSON
// and canonicalized, so a use the walker reads matches the bytes a JSON or
// YAML schema stores for the same config.
func canonicalConfig(t *testing.T, config string) json.RawMessage {
	t.Helper()
	refs := []ir.BehaviorRef{{Name: "Workflow", Config: json.RawMessage(config)}}
	if err := ir.CanonicalizeBehaviors(refs); err != nil {
		t.Fatal(err)
	}
	return refs[0].Config
}

// fieldNames lists a type's fields by name, in order.
func fieldNames(td *ir.TypeDef) []string {
	var names []string
	for _, f := range td.Fields {
		names = append(names, f.Name)
	}
	return names
}

// TestABehaviorFieldLoadsAsAUseOnTheTable (D103): a field whose type is
// Workflow<C> loads as a use anchored on that field, with C read through
// the checker into the config a JSON schema stores for it, and no field of
// its own: the loader adds the behavior's fields. The table
// stays a table, and one type carries Workflow on two fields, the second's
// config read with typeof from a constant declared as const in another
// file. The uses keep field order.
func TestABehaviorFieldLoadsAsAUseOnTheTable(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-workflow-db"))
	if err != nil {
		t.Fatal(err)
	}
	order, shipment := schema.Types["Order"], schema.Types["Shipment"]
	if order == nil || shipment == nil {
		t.Fatalf("types = %v, want Order and Shipment", schema.Types)
	}
	wantOrder := []ir.BehaviorRef{{Name: "Workflow", Field: "status", Config: canonicalConfig(t, `{
		"states": ["draft", "paid", "shipped"],
		"initial": "draft",
		"transitions": [{"from": "draft", "to": "paid"}, {"from": "paid", "to": "shipped", "permission": "orders.ship"}]
	}`)}}
	if !reflect.DeepEqual(order.Behaviors, wantOrder) {
		t.Fatalf("Order behaviors = %s, want %s", mustJSON(t, order.Behaviors), mustJSON(t, wantOrder))
	}
	wantShipment := []ir.BehaviorRef{
		{Name: "Workflow", Field: "status", Config: canonicalConfig(t, `{
			"states": ["pending", "packed", "sent"],
			"transitions": [{"from": "pending", "to": "packed"}, {"from": "packed", "to": "sent"}]
		}`)},
		{Name: "Workflow", Field: "paymentStatus", Config: canonicalConfig(t, `{
			"states": ["unpaid", "paid", "refunded"],
			"transitions": [{"from": "unpaid", "to": "paid"}, {"from": "paid", "to": "refunded", "permission": "payments.refund"}],
			"outcomes": {"refunded": "failure"}
		}`)},
	}
	if !reflect.DeepEqual(shipment.Behaviors, wantShipment) {
		t.Fatalf("Shipment behaviors = %s, want %s", mustJSON(t, shipment.Behaviors), mustJSON(t, wantShipment))
	}
	if got, want := fieldNames(order), []string{"id", "title", "placedAt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Order fields = %v, want %v: the anchor field is the behavior's to add", got, want)
	}
	if got, want := fieldNames(shipment), []string{"id", "reference"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Shipment fields = %v, want %v", got, want)
	}
	for _, td := range []*ir.TypeDef{order, shipment} {
		if td.Role != ir.RoleDBTable || td.IsEngineType() {
			t.Fatalf("%s: role %s, engine type %v; a type whose behaviors are on its fields stays a table", td.Name, td.Role, td.IsEngineType())
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// loadOrderStatus loads a DB service whose Order's status is Workflow<
// config >, with extra written before the class, and returns its uses.
func loadOrderStatus(t *testing.T, config, extra string, files map[string]string) ([]ir.BehaviorRef, error) {
	t.Helper()
	all := map[string]string{"src/a.schema.ts": `import { Workflow } from "@superschematic/db";
` + extra + `
export abstract class Order {
  title: string;
  status: Workflow<` + config + `>;
}
`}
	for name, text := range files {
		all[name] = text
	}
	schema, _, err := LoadService(decoratorTestService(t, "DB", all))
	if err != nil {
		return nil, err
	}
	return schema.Types["Order"].Behaviors, nil
}

// TestABehaviorFieldConfigReadsThroughTheChecker (D103): a config is a type
// argument of literal types, read through the checker, so each spelling of
// one config loads as the same canonical bytes: in place, with readonly
// and parentheses, through an alias or an interface, with typeof from a
// constant declared as const, and with a list written as an array of a
// union of literals ((A | B)[], Array<A | B>, ReadonlyArray<A | B>, the
// union or the array through an alias), which keeps the order the union is
// written in, not the checker's.
func TestABehaviorFieldConfigReadsThroughTheChecker(t *testing.T) {
	want := canonicalConfig(t, `{
		"states": ["shipped", "draft", "paid"],
		"initial": "draft",
		"transitions": [{"from": "draft", "to": "paid"}, {"from": "paid", "to": "shipped", "permission": "orders.ship"}],
		"outcomes": {"shipped": "success"}
	}`)
	const inPlace = `{
    states: ["shipped", "draft", "paid"];
    initial: "draft";
    transitions: [{ from: "draft"; to: "paid" }, { from: "paid"; to: "shipped"; permission: "orders.ship" }];
    outcomes: { shipped: "success" };
  }`
	// withStates is the config with its states written as states.
	withStates := func(states string) string {
		return strings.Replace(inPlace, `["shipped", "draft", "paid"]`, states, 1)
	}
	// seen has the checker meet "draft" and "paid" before the config, so
	// it orders a union of the three draft, paid, shipped.
	const seen = `type Seen = "draft" | "paid";
export const seen: Seen[] = [];
`
	for _, tc := range []struct {
		name, config, extra string
		files               map[string]string
	}{
		{name: "in place", config: inPlace},
		{
			name: "readonly and parenthesized",
			config: `{
    readonly states: readonly ["shipped", "draft", "paid"];
    initial: ("draft");
    transitions: readonly [{ readonly from: "draft"; to: "paid" }, ({ from: "paid"; to: "shipped"; permission: "orders.ship" })];
    outcomes: Readonly<{ shipped: "success" }>;
  }`,
		},
		{name: "an alias", config: "OrderFlow", extra: "type OrderFlow = " + inPlace + ";"},
		{
			// A schema file declares no interface, so the config's is
			// imported.
			name:   "an interface",
			config: "OrderFlow",
			extra:  `import type { OrderFlow } from "./flows";`,
			files: map[string]string{"src/flows.ts": `export interface OrderFlow {
  states: ["shipped", "draft", "paid"];
  initial: "draft";
  transitions: [{ from: "draft"; to: "paid" }, { from: "paid"; to: "shipped"; permission: "orders.ship" }];
  outcomes: { shipped: "success" };
}
`},
		},
		{
			name:   "typeof a constant declared as const in another file",
			config: "typeof orderFlow",
			extra:  `import { orderFlow } from "./flows";`,
			files: map[string]string{"src/flows.ts": `export const orderFlow = {
  states: ["shipped", "draft", "paid"],
  initial: "draft",
  transitions: [
    { from: "draft", to: "paid" },
    { from: "paid", to: "shipped", permission: "orders.ship" },
  ],
  outcomes: { shipped: "success" },
} as const;
`},
		},
		{
			// The checker meets "paid" and "draft" first in Seen, so it
			// orders the union's members draft, paid, shipped.
			name: "an array of a union of literals",
			config: `{
    states: ("shipped" | "draft" | "paid")[];
    initial: "draft";
    transitions: [{ from: "draft"; to: "paid" }, { from: "paid"; to: "shipped"; permission: "orders.ship" }];
    outcomes: { shipped: "success" };
  }`,
			extra: `type Seen = "draft" | "paid";
export const seen: Seen[] = [];`,
		},
		{
			name:   "an array of an alias of a union",
			config: withStates("States[]"),
			extra:  seen + `type States = "shipped" | "draft" | "paid";`,
		},
		{
			name:   "an alias of an array of a union",
			config: withStates("States"),
			extra:  seen + `type States = readonly ("shipped" | "draft" | "paid")[];`,
		},
		{name: "Array of a union", config: withStates(`Array<"shipped" | "draft" | "paid">`), extra: seen},
		{name: "ReadonlyArray of an alias of a union", config: withStates("ReadonlyArray<States>"), extra: seen + `type States = "shipped" | "draft" | "paid";`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uses, err := loadOrderStatus(t, tc.config, tc.extra, tc.files)
			if err != nil {
				t.Fatal(err)
			}
			wantUses := []ir.BehaviorRef{{Name: "Workflow", Field: "status", Config: want}}
			if !reflect.DeepEqual(uses, wantUses) {
				t.Fatalf("uses = %s\nwant %s", mustJSON(t, uses), mustJSON(t, wantUses))
			}
		})
	}
}

// probeService is a DB service whose @superschematic/db is a stand-in
// exporting Probe<C> and Comments<C> as behavior field types, beside the
// real schema and schema-config packages: the walker knows a behavior's
// field type by the symbol's package and a registered behavior's name, so
// a registry with a Probe behavior that takes any config holds the config
// reader to every kind of value, and the core's Comments, which names no
// value field, is a field type the walker must refuse.
func probeService(t *testing.T, source string) string {
	t.Helper()
	dir := decoratorTestService(t, "DB", map[string]string{
		"src/a.schema.ts": source,
		"db/package.json": `{"name": "@superschematic/db", "private": true}`,
		"db/index.ts":     "export type Probe<C> = string;\nexport type Comments<C> = string;\n",
	})
	writeServiceFile(t, dir, "tsconfig.json", func(text string) string {
		start := strings.Index(text, `"@superschematic/db": [`)
		end := strings.Index(text[start:], "],") + start + len("],")
		return text[:start] + `"@superschematic/db": ["./db/index.ts"],` + text[end:]
	})
	return dir
}

// probeRegistry is the core registry with Probe, a behavior that takes any
// object as its config and names its value field.
func probeRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Default())
	if err := reg.RegisterBehavior(registry.BehaviorSpec{Declaration: json.RawMessage(`{
		"name": "Probe",
		"configSchema": {"type": "object"},
		"fields": [{"name": "reading", "type": "string"}],
		"value": "reading"
	}`)}); err != nil {
		t.Fatal(err)
	}
	return reg
}

// loadProbe loads a Probe field with config and returns its config.
func loadProbe(t *testing.T, config, extra string) (json.RawMessage, error) {
	t.Helper()
	dir := probeService(t, `import { Probe } from "@superschematic/db";
`+extra+`
export abstract class Gauge {
  name: string;
  probe: Probe<`+config+`>;
}
`)
	schema, _, _, err := LoadServiceWithConfig(dir, WithRegistry(probeRegistry(t)))
	if err != nil {
		return nil, err
	}
	uses := schema.Types["Gauge"].Behaviors
	if len(uses) != 1 || uses[0].Name != "Probe" || uses[0].Field != "probe" {
		t.Fatalf("uses = %s, want one Probe on probe", mustJSON(t, uses))
	}
	return uses[0].Config, nil
}

// TestABehaviorConfigTakesEveryLiteral (D103): a config's string, number
// and boolean literal types are their values and null is null, nested in
// tuples and object types to any depth; a union of literals is a list; an
// optional member that is absent, or whose type is undefined, is left out;
// and a config of {} is stored as none, as @behavior's is.
func TestABehaviorConfigTakesEveryLiteral(t *testing.T) {
	got, err := loadProbe(t, `{
    text: "x";
    count: 3;
    ratio: -1.5;
    on: true;
    off: false;
    nothing: null;
    mixed: [1, "a", true, null, []];
    nested: { deep: [{ key: "v"; more: { level: 3 } }] };
    either: "b" | "a" | 2;
    absent?: "o";
    unset: undefined;
    member: Kind.Big;
  }`, `enum Kind { Big = "big" }`)
	if err != nil {
		t.Fatal(err)
	}
	want := canonicalConfig(t, `{
		"text": "x", "count": 3, "ratio": -1.5, "on": true, "off": false, "nothing": null,
		"mixed": [1, "a", true, null, []],
		"nested": {"deep": [{"key": "v", "more": {"level": 3}}]},
		"either": ["b", "a", 2],
		"absent": "o",
		"member": "big"
	}`)
	if string(got) != string(want) {
		t.Fatalf("config = %s\nwant %s", got, want)
	}

	empty, err := loadProbe(t, "{}", "")
	if err != nil {
		t.Fatal(err)
	}
	if empty != nil {
		t.Fatalf("config of {} = %s, want none", empty)
	}
}

// TestABehaviorConfigRefusesWhatIsNoLiteral (D103): a member whose type
// stands for no fixed value fails the load, naming the type, the field,
// the behavior and the path to the member, and saying what it is: a
// widened string, boolean, a list of no fixed length, an index signature,
// a tuple's optional element, a function, a union with an object type, a
// constant read with typeof that is not declared as const, and a list of a
// union whose written order the reader cannot find.
func TestABehaviorConfigRefusesWhatIsNoLiteral(t *testing.T) {
	const prefix = "type Gauge: field probe: behavior Probe config "
	for _, tc := range []struct {
		name, config, extra, want string
	}{
		{"a string", `{ nested: { name: string } }`, "", prefix + "/nested/name: string is not a literal type; a config is written in literal types"},
		{"boolean", `{ on: boolean }`, "", prefix + "/on: boolean is not a literal type; write true or false"},
		{"a list of strings", `{ tags: string[] }`, "", prefix + "/tags: string[] is a list of no fixed elements; write a tuple ([A, B]) or a list of literal types ((A | B)[])"},
		{"a list of objects", `{ moves: { from: "a" }[] }`, "", prefix + `/moves: { from: "a"; }[] is a list of no fixed elements`},
		{"an index signature", `{ outcomes: Record<string, "failure"> }`, "", prefix + "/outcomes: Record<string, \"failure\"> has an index signature, which names no members; write each member"},
		{"an optional element", `{ pair: [1, 2?] }`, "", prefix + "/pair/1: an optional or rest element of a tuple is not a value; write each element"},
		{"a function", `{ check: () => void }`, "", prefix + "/check: () => void is not a literal type"},
		{"a union with an object type", `{ either: "a" | { b: 1 } }`, "", prefix + `/either: "a" | { b: 1; } is a union, which a config reads as a list of literal types, and { b: 1; } is not one`},
		{"a constant not declared as const", `typeof flow`, `const flow = { states: ["a"] };`, prefix + "/states: string[] is a list of no fixed elements"},
		{"a list of a union whose order is not written", `{ order: (keyof { b: 1; a: 2 })[] }`, "", prefix + `/order: "a" | "b" is a union, and a list read from a union keeps the order it is written in, which the reader cannot find here; write the list as a tuple ([A, B]) or as an array of a union written in place ((A | B)[])`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadProbe(t, tc.config, tc.extra)
			if err == nil {
				t.Fatal("the service loaded")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v\nwant it to contain %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "a.schema.ts:") {
				t.Fatalf("error = %v, want it located in the schema file", err)
			}
		})
	}
}

// TestABehaviorFieldIsRefusedWhereItCannotBe (D103): a behavior's field
// type is refused, located and naming the type and the field, on a type
// that is not a DB table (an API schema's, a trait); for a behavior that
// names no value field; when optional or decorated; inside a wrapper or a
// list; when a base class carries it; and with a config its config schema
// refuses.
func TestABehaviorFieldIsRefusedWhereItCannotBe(t *testing.T) {
	const flow = `{ states: ["draft", "paid"]; transitions: [{ from: "draft"; to: "paid" }] }`
	for _, tc := range []struct {
		name, kind, source, want string
	}{
		{
			name: "an API schema's type",
			kind: "API",
			source: `import { Workflow } from "@superschematic/db";
export abstract class OrderView {
  status: Workflow<` + flow + `>;
}
`,
			want: "a.schema.ts:3:3: type OrderView: field status is a Workflow, and a behavior is anchored on a field of a DB table, which OrderView is not (D103)",
		},
		{
			name: "a trait",
			kind: "DB",
			source: `import { trait } from "@superschematic/schema";
import { Workflow } from "@superschematic/db";
@trait()
export abstract class Stateful {
  status: Workflow<` + flow + `>;
}
`,
			want: "a.schema.ts:5:3: type Stateful: field status is a Workflow, and a behavior is anchored on a field of a DB table, which Stateful is not (D103)",
		},
		{
			name: "an optional field",
			kind: "DB",
			source: `import { Workflow } from "@superschematic/db";
export abstract class Order {
  status?: Workflow<` + flow + `>;
}
`,
			want: "a.schema.ts:3:3: type Order: field status is a Workflow, which is never optional: the behavior sets it when a row is created; write status: Workflow<...> (D103)",
		},
		{
			name: "a decorated field",
			kind: "DB",
			source: `import { unique, Workflow } from "@superschematic/db";
export abstract class Order {
  @unique
  status: Workflow<` + flow + `>;
}
`,
			want: "a.schema.ts:3:3: type Order: field status is a Workflow, which takes no decorator: the behavior declares the field (D103)",
		},
		{
			name: "inside a wrapper",
			kind: "DB",
			source: `import { Nullable } from "@superschematic/schema";
import { Workflow } from "@superschematic/db";
export abstract class Order {
  status: Nullable<Workflow<` + flow + `>>;
}
`,
			want: "a.schema.ts:4:20: Workflow<C> is behavior Workflow as a field's type, which is the field's whole type (status: Workflow<{ ... }>) on a DB table, never inside a wrapper, a list or a union, nor an argument's (D103)",
		},
		{
			name: "a list",
			kind: "DB",
			source: `import { Workflow } from "@superschematic/db";
export abstract class Order {
  statuses: Workflow<` + flow + `>[];
}
`,
			want: "a.schema.ts:3:13: Workflow<C> is behavior Workflow as a field's type",
		},
		{
			name: "a base class's field",
			kind: "DB",
			source: `import { Workflow } from "@superschematic/db";
export abstract class Tracked {
  status: Workflow<` + flow + `>;
}
export abstract class Order extends Tracked {
  title: string;
}
`,
			want: "a.schema.ts:5:37: type Order: Tracked's field status is a Workflow, which Order does not inherit: a behavior is anchored on a field of the table that declares it (D103)",
		},
		{
			name: "a config its schema refuses",
			kind: "DB",
			source: `import { Workflow } from "@superschematic/db";
export abstract class Order {
  status: Workflow<{ states: ["draft", "draft"]; transitions: []; colour: "red" }>;
}
`,
			want: "a.schema.ts:3:20: type Order: field status: behavior Workflow config: jsonschema validation failed with 'superschematic://behaviors/Workflow/config.json#'\n" +
				"- at '/states': items at 0 and 1 are equal\n" +
				"- at '': additional properties 'colour' not allowed",
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

	t.Run("a behavior that names no value field", func(t *testing.T) {
		dir := probeService(t, `import { Comments } from "@superschematic/db";
export abstract class Order {
  notes: Comments<{}>;
}
`)
		_, _, err := LoadService(dir)
		want := "a.schema.ts:3:10: type Order: field notes: behavior Comments names no value field in its declaration, so it is not anchored on a field; compose it on the type (D103)"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v\nwant it to contain %q", err, want)
		}
	})
}

// TestBothFormsOnOneTypeReachVerify (D103): a type that composes a
// behavior with @behavior and another on a field loads with both uses,
// the decorator's first, so the verification pass refuses it for every
// authoring form alike (verify.checkTypeBehaviors); the decorator's own
// check that a type lists a behavior once does not mistake the field's use
// for a second listing.
func TestBothFormsOnOneTypeReachVerify(t *testing.T) {
	dir := decoratorTestService(t, "DB", map[string]string{"src/a.schema.ts": `import { behavior } from "@superschematic/schema";
import { Workflow } from "@superschematic/db";
@behavior("Workflow", { states: ["open"], transitions: [] })
export abstract class Order {
  status: Workflow<{ states: ["draft", "paid"]; transitions: [{ from: "draft"; to: "paid" }] }>;
}
`})
	schema, _, err := LoadService(dir)
	if err != nil {
		t.Fatal(err)
	}
	order := schema.Types["Order"]
	if got := order.TypeBehaviors(); len(got) != 1 || got[0].Name != "Workflow" || order.Behaviors[0].FieldAnchored() {
		t.Fatalf("behaviors = %s, want the decorator's use first", mustJSON(t, order.Behaviors))
	}
	if got := order.FieldBehaviors(); len(got) != 1 || got[0].Field != "status" {
		t.Fatalf("behaviors = %s, want the field's use second", mustJSON(t, order.Behaviors))
	}
}
