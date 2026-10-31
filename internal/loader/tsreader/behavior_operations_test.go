package tsreader

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestBehaviorOperationsClass: the @behaviorOperations classes of
// fixture-workflow-api are operation sets with their tables and their
// clauses and no operations, which the loader adds (D103); the table is
// an import of the API's database, and the API's other set is read as
// before.
func TestBehaviorOperationsClass(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-workflow-api"))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	sets := map[string]*ir.OperationSet{}
	for _, set := range schema.OperationSets {
		sets[set.Name] = set
	}
	orders, shipments, queries := sets["OrderActions"], sets["ShipmentActions"], sets["OrderQueries"]
	if orders == nil || shipments == nil || queries == nil {
		t.Fatalf("operation sets = %v", sets)
	}
	if want := (&ir.BehaviorOperationsConfig{Type: "Order"}); !reflect.DeepEqual(orders.BehaviorOperations, want) {
		t.Errorf("OrderActions.BehaviorOperations = %+v, want %+v", orders.BehaviorOperations, want)
	}
	if !reflect.DeepEqual(orders.Permissions, []string{"orders.write"}) || orders.Auth || orders.Public {
		t.Errorf("OrderActions' clause = auth %v, permissions %v, public %v", orders.Auth, orders.Permissions, orders.Public)
	}
	if want := (&ir.BehaviorOperationsConfig{Type: "Shipment"}); !reflect.DeepEqual(shipments.BehaviorOperations, want) {
		t.Errorf("ShipmentActions.BehaviorOperations = %+v, want %+v", shipments.BehaviorOperations, want)
	}
	if !shipments.Auth || len(shipments.Permissions) != 0 || shipments.Public {
		t.Errorf("ShipmentActions' clause = auth %v, permissions %v, public %v", shipments.Auth, shipments.Permissions, shipments.Public)
	}
	if orders.Operations == nil || len(orders.Operations) != 0 || len(shipments.Operations) != 0 {
		t.Errorf("the reader added operations: %v, %v", orders.Operations, shipments.Operations)
	}
	if !strings.HasPrefix(orders.Comment, "An order's status operations:") {
		t.Errorf("OrderActions.Comment = %q", orders.Comment)
	}
	if queries.IsBehaviorOperations() || len(queries.Operations) != 1 || queries.Auth {
		t.Errorf("OrderQueries = %+v", queries)
	}
	want := []ir.Import{{Package: "@schemas/fixture-workflow-db", Types: []string{"Order", "Shipment"}}}
	if !reflect.DeepEqual(schema.Imports, want) {
		t.Errorf("Imports = %+v, want %+v", schema.Imports, want)
	}
	for name := range schema.Types {
		t.Errorf("the reader added the type %s", name)
	}
}

// TestBehaviorOperationsRefusals pins each refusal of a @behaviorOperations
// class's form, and of the set's clause on any other class, at the node
// that carries it.
func TestBehaviorOperationsRefusals(t *testing.T) {
	const imports = `import { Authenticated, Encrypted, HttpMethod, auth, behaviorOperations, publicRoute, requirePermission, rest, userSessions } from "@superschematic/api";
declare abstract class Order { id: string }
`
	cases := []struct {
		name   string
		kind   string
		source string
		want   []string
	}{
		{
			name: "a method",
			kind: "API",
			source: imports + `@behaviorOperations(Order)
export abstract class OrderActions {
  @rest(HttpMethod.POST, "orders/{id}/ship")
  ship(id: string): boolean {
    throw new Error("schema declaration only");
  }
}
`,
			want: []string{`a.schema.ts:5:3: OrderActions: a @behaviorOperations class has no members: the loader adds one operation for each operation of each behavior on Order's fields, and the generated server runs it`},
		},
		{
			name: "a property",
			kind: "API",
			source: imports + `@behaviorOperations(Order)
export abstract class OrderActions {
  note: string;
}
`,
			want: []string{`a.schema.ts:5:3: OrderActions: a @behaviorOperations class has no members: the loader adds one operation for each operation of each behavior on Order's fields, and the generated server runs it`},
		},
		{
			name: "an Authenticated base",
			kind: "API",
			source: imports + `@behaviorOperations(Order)
export abstract class OrderActions extends Authenticated {}
`,
			want: []string{`a.schema.ts:4:44: OrderActions: a @behaviorOperations class does not extend Authenticated: write @auth on the class`},
		},
		{
			name: "an Encrypted base",
			kind: "API",
			source: imports + `@behaviorOperations(Order)
export abstract class OrderActions extends Encrypted {}
`,
			want: []string{`a.schema.ts:4:44: OrderActions: a @behaviorOperations class is not Encrypted: its routes' bodies are its behaviors' parameters, which the generated handlers read as plain JSON`},
		},
		{
			name: "twice on one class",
			kind: "API",
			source: imports + `@behaviorOperations(Order)
@behaviorOperations(Order)
export abstract class OrderActions {}
`,
			want: []string{`a.schema.ts:4:1: @behaviorOperations appears twice on the class: a set serves one table`},
		},
		{
			name: "beside @userSessions",
			kind: "API",
			source: imports + `@userSessions()
@behaviorOperations(Order)
export abstract class OrderActions {}
`,
			want: []string{`a.schema.ts:4:1: @behaviorOperations contradicts @userSessions on the same class: a class takes one of them`},
		},
		{
			name: "before @userSessions",
			kind: "API",
			source: imports + `@behaviorOperations(Order)
@userSessions()
export abstract class OrderActions {}
`,
			want: []string{`a.schema.ts:4:1: @userSessions contradicts @behaviorOperations on the same class: a class takes one of them`},
		},
		{
			name: "no argument",
			kind: "API",
			source: imports + `// @ts-expect-error the table is required
@behaviorOperations()
export abstract class OrderActions {}
`,
			want: []string{`@behaviorOperations takes one argument, the table whose behaviors' operations the set serves, imported from the API's database: @behaviorOperations(Order)`},
		},
		{
			name: "a set-level @auth on another set",
			kind: "API",
			source: imports + `@auth
export class Orders {
  @rest(HttpMethod.GET, "orders")
  list(): string[] {
    throw new Error("schema declaration only");
  }
}
`,
			want: []string{`a.schema.ts:3:1: Orders: @auth on a class applies only to a @behaviorOperations set; extend Authenticated, or put @auth on each method of Orders`},
		},
		{
			name: "a set-level @requirePermission on another set",
			kind: "API",
			source: imports + `@requirePermission(["orders.read"])
export class Orders {
  @rest(HttpMethod.GET, "orders")
  list(): string[] {
    throw new Error("schema declaration only");
  }
}
`,
			want: []string{`a.schema.ts:3:1: Orders: @requirePermission on a class applies only to a @behaviorOperations set; put it on each method of Orders`},
		},
		{
			name: "a set-level @publicRoute on a route set",
			kind: "API",
			source: imports + `@userSessions()
@publicRoute
export class Account {}
`,
			want: []string{`a.schema.ts:4:1: Account: @publicRoute on a class applies only to a @behaviorOperations set; put it on each method of Account`},
		},
		{
			name: "a DB schema",
			kind: "DB",
			source: imports + `@behaviorOperations(Order)
export abstract class OrderActions {}
`,
			want: []string{`a.schema.ts:3:1: @behaviorOperations is only allowed in an API schema (this service is kind DB)`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := decoratorTestService(t, tc.kind, map[string]string{"src/a.schema.ts": tc.source})
			_, _, err := LoadService(dir)
			if err == nil {
				t.Fatal("expected schema errors")
			}
			msg := err.Error()
			for _, want := range tc.want {
				if !strings.Contains(msg, want) {
					t.Errorf("missing diagnostic %q in:\n%s", want, msg)
				}
			}
		})
	}
}
