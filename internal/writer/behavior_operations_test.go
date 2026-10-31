package writer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestBehaviorOperationsRoundTrip: fixture-workflow-api written in each
// format and read back over its database is the same expanded IR (D103).
// Every format writes the @behaviorOperations sets with their tables and
// clauses, and none writes the operations, types or enums the loader
// fills them with.
func TestBehaviorOperationsRoundTrip(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-workflow-api")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	db, err := loader.LoadService(tsFixtures + "/fixture-workflow-db")
	if err != nil {
		t.Fatalf("loading the database: %v", err)
	}
	want := normalizeIR(t, schema)
	contains := map[Format][]string{
		FormatTS: {
			`import { Authenticated, HttpMethod, auth, behaviorOperations, requirePermission, rest } from "@superschematic/api";`,
			`import { Order, Shipment } from "@schemas/fixture-workflow-db";`,
			"// to shipped needs orders.ship too, which the core checks.\n@behaviorOperations(Order)\n@requirePermission([\"orders.write\"])\nexport abstract class OrderActions {}\n",
			"@behaviorOperations(Shipment)\n@auth\nexport abstract class ShipmentActions {}\n",
		},
		FormatJSON: {`"behaviorOperations": {`, `"type": "Order"`, `"type": "Shipment"`, `"auth": true`, `"orders.write"`, `"operations": []`},
		FormatYAML: {"behaviorOperations:\n", "type: Order", "type: Shipment", "auth: true", "- orders.write", "operations: []"},
	}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(format), func(t *testing.T) {
			dir := t.TempDir()
			if _, err := WriteService(schema, format, dir); err != nil {
				t.Fatalf("WriteService: %v", err)
			}
			cfg := fmt.Sprintf(`{"name": %q, "kind": "API", "dependencies": [{"name": "fixture-workflow-db", "kind": "DB"}], "outputs": {}}`+"\n", schema.Name)
			if err := os.WriteFile(filepath.Join(dir, "schema.config.json"), []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			if format == FormatTS {
				writeTSProject(t, dir, schema)
			}
			source := writtenSource(t, schema, format)
			for _, want := range contains[format] {
				if !strings.Contains(source, want) {
					t.Errorf("written %s lacks %q:\n%s", format, want, source)
				}
			}
			for _, added := range []string{"statusTransition", "OrderStatusTransitionInput", "OrderStatusState", "order_status_transition", "behaviorOperation\""} {
				if strings.Contains(source, added) {
					t.Errorf("written %s carries the loader's %s:\n%s", format, added, source)
				}
			}
			reloaded, err := loader.LoadService(dir, loader.WithDependencyLoader(func(name string) (*ir.Schema, error) {
				if name != db.Name {
					return nil, fmt.Errorf("no service %s", name)
				}
				return db, nil
			}))
			if err != nil {
				dumpService(t, dir)
				t.Fatalf("reloading %s output: %v", format, err)
			}
			if got := normalizeIR(t, reloaded); got != want {
				t.Errorf("IR mismatch after writing %s\nwant:\n%s\ngot:\n%s", format, want, got)
			}
		})
	}
}

// TestWriterRefusesAFilledBehaviorOperationsSet: a set that holds an
// operation the loader did not add, or a class-level clause on another
// set, has no TypeScript form.
func TestWriterRefusesAFilledBehaviorOperationsSet(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  *ir.OperationSet
		want string
	}{
		{
			"an authored operation",
			&ir.OperationSet{Name: "OrderActions", BehaviorOperations: &ir.BehaviorOperationsConfig{Type: "Order"}, Operations: []*ir.FieldDef{
				{Name: "ping", TypeRef: ir.TypeRef{Name: "boolean"}, HTTPMethod: "GET"},
			}},
			"operation set OrderActions: a @behaviorOperations class has no members; write the set the loader has not filled",
		},
		{
			"a clause on another set",
			&ir.OperationSet{Name: "Ping", Auth: true, Operations: []*ir.FieldDef{
				{Name: "ping", TypeRef: ir.TypeRef{Name: "boolean"}, HTTPMethod: "GET"},
			}},
			"operation set Ping: a class-level @auth, @requirePermission or @publicRoute applies only to a @behaviorOperations set",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := ir.NewSchema("orders-api", ir.SchemaKindAPI)
			schema.Imports = []ir.Import{{Package: "@schemas/orders-db", Types: []string{"Order"}}}
			schema.OperationSets = []*ir.OperationSet{tc.set}
			_, err := WriteService(schema, FormatTS, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("WriteService = %v, want an error holding %q", err, tc.want)
			}
		})
	}
}
