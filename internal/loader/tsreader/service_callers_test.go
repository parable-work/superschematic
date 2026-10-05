package tsreader

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestServiceCallersEffectiveRule: fixture-service-auth-api reads each
// clause into the IR where it is declared, a from handle as the API
// service's name, and an operation's effective clause is its own, else its
// set's, except on an @publicRoute operation, which takes none. Importing
// the caller API's sentinel records no import: a handle in from is an
// identity, not a build-order edge.
func TestServiceCallersEffectiveRule(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-service-auth-api"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(schema.Imports) != 0 {
		t.Errorf("Imports = %+v, want none: a from handle adds no dependency", schema.Imports)
	}
	caller := []string{"fixture-service-caller-api"}
	require := func(from []string) *ir.ServiceCallers {
		return &ir.ServiceCallers{Mode: ir.ServiceCallersRequire, From: from}
	}
	allow := func(from []string) *ir.ServiceCallers {
		return &ir.ServiceCallers{Mode: ir.ServiceCallersAllow, From: from}
	}
	want := map[string]*ir.ServiceCallers{
		"reserveStock":       require(caller),
		"releaseReservation": allow(caller),
		"reindexStock":       require(nil),
		"getReservation":     nil,
		"syncStock":          require(caller),
		"syncMyStock":        allow(nil),
		"syncStatus":         nil,
		"listReservations":   allow(caller),
	}
	sets := map[string]*ir.ServiceCallers{
		"StockMutations": nil,
		"SyncOperations": require(caller),
		"LedgerQueries":  allow(caller),
	}
	seen := 0
	for _, set := range schema.OperationSets {
		if got := set.ServiceCallers; !reflect.DeepEqual(got, sets[set.Name]) {
			t.Errorf("%s ServiceCallers = %+v, want %+v", set.Name, got, sets[set.Name])
		}
		for _, op := range set.Operations {
			seen++
			if got := ir.EffectiveServiceCallers(set, op); !reflect.DeepEqual(got, want[op.Name]) {
				t.Errorf("%s effective ServiceCallers = %+v, want %+v", op.Name, got, want[op.Name])
			}
		}
	}
	if seen != len(want) {
		t.Errorf("saw %d operations, want %d", seen, len(want))
	}
}

// TestServiceCallersRefusals pins the text and location of each refusal of
// section 9.3 of docs/stack-model.md: at the method's own decorator, at the
// method when its set's clause reaches it, at the second clause on one
// operation or set, and at the config for a handle that is not an API.
func TestServiceCallersRefusals(t *testing.T) {
	dir := decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": `import { SchemaKind, service } from "@superschematic/schema-config";
import { HttpMethod, allowService, auth, hmacVerified, publicRoute, requireService, rest, webhook } from "@superschematic/api";
const Orders = service({ name: "orders-api", kind: SchemaKind.API });
const OrdersDb = service({ name: "orders-db", kind: SchemaKind.DB });
export class ThingOperations {
  @allowService()
  @rest(HttpMethod.GET, "a")
  a(): string {
    throw new Error("schema declaration only");
  }
  @publicRoute
  @requireService()
  @rest(HttpMethod.GET, "b")
  b(): string {
    throw new Error("schema declaration only");
  }
  @auth
  @webhook
  @allowService({ from: [Orders] })
  @rest(HttpMethod.POST, "c")
  c(): string {
    throw new Error("schema declaration only");
  }
  @requireService()
  @hmacVerified({ provider: "stripe" })
  @rest(HttpMethod.POST, "d")
  d(): string {
    throw new Error("schema declaration only");
  }
  @requireService()
  @allowService()
  @rest(HttpMethod.POST, "e")
  e(): string {
    throw new Error("schema declaration only");
  }
  @requireService({ from: [Orders, OrdersDb] })
  @rest(HttpMethod.POST, "f")
  f(): string {
    throw new Error("schema declaration only");
  }
}
@allowService()
export class OpenOperations {
  @rest(HttpMethod.GET, "g")
  g(): string {
    throw new Error("schema declaration only");
  }
  @publicRoute
  @rest(HttpMethod.GET, "h")
  h(): string {
    throw new Error("schema declaration only");
  }
}
@requireService({ from: [Orders] })
export class HookOperations {
  @webhook
  @rest(HttpMethod.POST, "i")
  i(): string {
    throw new Error("schema declaration only");
  }
}
@requireService()
@allowService()
export class TwiceOperations {
  @auth
  @rest(HttpMethod.GET, "j")
  j(): string {
    throw new Error("schema declaration only");
  }
}
`})
	_, _, err := LoadService(dir)
	if err == nil {
		t.Fatal("expected the service clauses to be refused")
	}
	want := []string{
		"a.schema.ts:6:3: @allowService on a needs a user clause: @auth, an Authenticated set, @requirePermission or @requireOwnership; an operation only services call is @requireService",
		"a.schema.ts:12:3: @requireService contradicts @publicRoute on b: a public route needs no caller",
		"a.schema.ts:19:3: @allowService contradicts @webhook on c: a third party calls it, and holds no service credential",
		"a.schema.ts:24:3: @requireService contradicts @hmacVerified on d: a third party calls it, and holds no service credential",
		"a.schema.ts:31:3: @allowService contradicts @requireService on the same operation: declare one of them",
		"a.schema.ts:36:19: @requireService from lists \"orders-db\", a DB service: only an API service's server calls an operation",
		"a.schema.ts:44:3: the set's @allowService on g needs a user clause: @auth, an Authenticated set, @requirePermission or @requireOwnership; an operation only services call is @requireService",
		"a.schema.ts:56:3: the set's @requireService contradicts @webhook on i: a third party calls it, and holds no service credential",
		"a.schema.ts:63:1: @allowService contradicts @requireService on the same operation set: declare one of them",
	}
	for _, line := range want {
		if !strings.Contains(err.Error(), line) {
			t.Errorf("missing %q in:\n%s", line, err)
		}
	}
	if got := strings.Count(err.Error(), "\n") + 1; got != len(want) {
		t.Errorf("got %d diagnostics, want %d:\n%s", got, len(want), err)
	}
	// h is @publicRoute in a set with @allowService: it takes no clause, so
	// nothing refuses it.
	if strings.Contains(err.Error(), " on h") {
		t.Errorf("the @publicRoute operation h was refused:\n%s", err)
	}
}
