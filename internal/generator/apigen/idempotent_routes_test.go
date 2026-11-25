package apigen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	ir "github.com/parable-work/superschematic/ir"
)

const idempotentDB = "fixture-idempotent-db"

const idempotentAPI = "fixture-idempotent-api"

// idempotentOrders is fixture-idempotent-api, whose placeOrder and
// cancelOrder are @idempotent, the table fixture-idempotent-db, its
// database, declares for their keys, and that table's SQLite DDL as the
// SQL generator writes it (D140).
func idempotentOrders(t *testing.T) (schema *ir.Schema, table *ir.IdempotencyTable, ddl string) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, idempotentAPI))
	if err != nil {
		t.Fatalf("load %s: %v", idempotentAPI, err)
	}
	db, err := loader.LoadService(filepath.Join(fixturesDir, idempotentDB))
	if err != nil {
		t.Fatalf("load %s: %v", idempotentDB, err)
	}
	table, err = sqlutil.IdempotencyTableOf(db)
	if err != nil {
		t.Fatal(err)
	}
	model, err := sqlmigrate.BuildModel(db, sqlgen.Options{SchemaName: idempotentDB}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	ddl, err = sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	return schema, table, ddl
}

// TestIdempotentRoutePlan: the endpoint carries its @idempotent, the
// output the table of its keys with the names the SQL generator gives
// them, and an API with such an operation and no table is refused (D140).
func TestIdempotentRoutePlan(t *testing.T) {
	schema, table, ddl := idempotentOrders(t)
	want := ir.IdempotencyTable{
		Table: "idempotency_key", Lookup: "lookup", Operation: "operation", Principal: "principal", Key: "key", RequestHash: "request_hash",
		State: "state", Status: "status", Body: "body", StartedAt: "started_at", ExpiresAt: "expires_at",
	}
	if *table != want {
		t.Errorf("table = %+v, want %+v", *table, want)
	}
	for _, column := range []string{"lookup", "request_hash", "started_at", "expires_at"} {
		if !strings.Contains(ddl, column) {
			t.Errorf("the DDL lacks %s:\n%s", column, ddl)
		}
	}
	output, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: idempotentAPI, Clock: goModuleClock, IdempotencyTable: table})
	if err != nil {
		t.Fatal(err)
	}
	if !output.HasIdempotent() || output.IdempotencyTable != table {
		t.Errorf("HasIdempotent = %t, table %+v", output.HasIdempotent(), output.IdempotencyTable)
	}
	for _, endpoint := range output.Endpoints {
		if (endpoint.Idempotent != nil) != (endpoint.Name != "listOrders") {
			t.Errorf("%s: idempotent %+v", endpoint.Name, endpoint.Idempotent)
		}
		switch endpoint.Name {
		case "placeOrder":
			if endpoint.IdempotencyOperation() != "orders.placeOrder" || endpoint.IdempotencyTTLSeconds() != 3600 || endpoint.Idempotent.Required {
				t.Errorf("placeOrder keeps its keys as %s for %d seconds", endpoint.IdempotencyOperation(), endpoint.IdempotencyTTLSeconds())
			}
		case "cancelOrder":
			if endpoint.IdempotencyTTLSeconds() != ir.DefaultIdempotencyTTLSeconds || !endpoint.Idempotent.Required {
				t.Errorf("cancelOrder's @idempotent = %+v", endpoint.Idempotent)
			}
		}
	}
	// The OpenAPI document declares the header, required where the
	// operation requires it, the refusals of a key and the replay's mark.
	var spec struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name, In    string
				Required    bool
				Description string
			}
			Responses map[string]struct {
				Description string
				Headers     map[string]any
			}
		}
	}
	if err := json.Unmarshal([]byte(output.OpenAPISpecRaw), &spec); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path, method string
		idempotent   bool
		required     bool
		ttl          string
	}{
		{"/api/orders", "post", true, false, "within 3600 seconds"},
		{"/api/orders/{number}", "delete", true, true, "within 86400 seconds"},
		{"/api/orders", "get", false, false, ""},
	} {
		operation := spec.Paths[c.path][c.method]
		declared := false
		for _, parameter := range operation.Parameters {
			if parameter.Name != ir.IdempotencyKeyHeader {
				continue
			}
			declared = true
			if parameter.In != "header" || parameter.Required != c.required || !strings.Contains(parameter.Description, c.ttl) {
				t.Errorf("%s %s: the Idempotency-Key parameter is %+v", c.method, c.path, parameter)
			}
		}
		_, inFlight := operation.Responses["409"]
		_, reused := operation.Responses["422"]
		_, replay := operation.Responses["200"].Headers[apigen.IdempotencyReplayHeader]
		if declared != c.idempotent || inFlight != c.idempotent || reused != c.idempotent || replay != c.idempotent {
			t.Errorf("%s %s: header %t, 409 %t, 422 %t, replay header %t, want each %t", c.method, c.path, declared, inFlight, reused, replay, c.idempotent)
		}
	}
	if _, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: idempotentAPI, Clock: goModuleClock}); err == nil || !strings.Contains(err.Error(), "has @idempotent operations, and the build read no table for their keys") {
		t.Errorf("without the table: %v", err)
	}
}

// TestIdempotentRoutes generates the Go types and API modules of
// fixture-idempotent-api, writes the SQLite
// DDL of fixture-idempotent-db's table beside them, copies
// testdata/idempotent_routes_test.go into the API module and runs it: the
// route keeps a key in the table through the runtime's SQL store, runs
// the implementation once per key and answers a repeat what the first
// request was, as the TypeScript router does
// (internal/generator/tsrestgen, TestGeneratedIdempotentRoutesEndToEnd).
func TestIdempotentRoutes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, table, ddl := idempotentOrders(t)
	apiDir := writeGoAPIModuleOver(t, schema, idempotentAPI, nil, nil, func(options *apigen.Options) { options.IdempotencyTable = table })
	source, err := os.ReadFile(filepath.Join("testdata", "idempotent_routes_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string][]byte{"idempotent_routes_test.go": source, "idempotency.sql": []byte(ddl)} {
		if err := os.WriteFile(filepath.Join(apiDir, name), contents, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGoAPIModule(t, apiDir)
}
