package tsrestgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sdkgen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

const (
	idempotentAPI = "fixture-idempotent-api"
	idempotentDB  = "fixture-idempotent-db"
)

// idempotentFixture is fixture-idempotent-api, whose placeOrder and
// cancelOrder are @idempotent, over the table fixture-idempotent-db, its
// database, declares for their keys, and that table's SQLite DDL as the
// SQL generator writes it (D140).
func idempotentFixture(t *testing.T) (apiFixture, string) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, idempotentAPI))
	if err != nil {
		t.Fatalf("load %s: %v", idempotentAPI, err)
	}
	db, err := loader.LoadService(filepath.Join(fixturesDir, idempotentDB))
	if err != nil {
		t.Fatalf("load %s: %v", idempotentDB, err)
	}
	table, err := sqlutil.IdempotencyTableOf(db)
	if err != nil {
		t.Fatal(err)
	}
	model, err := sqlmigrate.BuildModel(db, sqlgen.Options{SchemaName: idempotentDB}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:         sessionauth.Provider{},
		SchemaName:       idempotentAPI,
		Clock:            fixedClock,
		IdempotencyTable: table,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: idempotentAPI, schema: schema, endpoints: endpoints}, ddl
}

// TestGenerateIdempotentShape: the operation table carries an @idempotent
// operation's ttl and whether it requires a key, and router.ts exports the
// names of the table that keeps the keys (D140).
func TestGenerateIdempotentShape(t *testing.T) {
	fixture, _ := idempotentFixture(t)
	output := generateFixture(t, fixture)
	endpoints := endpointsByName(output)
	for name, want := range map[string]string{"placeOrder": "{ ttlSeconds: 3600 }", "cancelOrder": "{ ttlSeconds: 86400, required: true }"} {
		if got := endpoints[name].IdempotentLiteral(); got != want {
			t.Errorf("%s's idempotent = %s, want %s", name, got, want)
		}
	}
	if endpoints["listOrders"].Idempotent != nil {
		t.Errorf("listOrders is idempotent: %+v", endpoints["listOrders"].Idempotent)
	}
	want := "{ table: 'idempotency_key', columns: { lookup: 'lookup', operation: 'operation', principal: 'principal', key: 'key', requestHash: 'request_hash', state: 'state', status: 'status', body: 'body', startedAt: 'started_at', expiresAt: 'expires_at' } }"
	if got := output.IdempotencyTableLiteral(); got != want {
		t.Errorf("the table = %s", got)
	}
	if !strings.Contains(want, "idempotency_key") {
		t.Fatal("unreachable")
	}
}

// TestGeneratedIdempotentRoutesEndToEnd materializes fixture-idempotent-api,
// type-checks it with its implementation, and serves the generated router
// under bun with its keys in a SQLite file made from the generated DDL of
// fixture-idempotent-db (testdata/idempotent_runtime.test.ts): each
// @idempotent route runs its implementation once per key and answers a
// repeat what the first request was, through the generated SDK too, as the
// Go router does (internal/generator/apigen, TestIdempotentRoutes).
func TestGeneratedIdempotentRoutesEndToEnd(t *testing.T) {
	fixture, ddl := idempotentFixture(t)
	tree := materializeAPI(t, fixture)
	tree.stage(t, "idempotent_implementation.ts")
	tree.typeCheck(t)

	tsOutput, err := tsgen.Generate(fixture.schema, tsgen.Options{SchemaName: idempotentAPI, Clock: fixedClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	sdkOutput, err := sdkgen.Generate(fixture.endpoints, tsgen.ParseableTypeNames(tsOutput), fixedClock)
	if err != nil {
		t.Fatalf("sdkgen.Generate: %v", err)
	}
	sdkDir := filepath.Join(tree.root, "sdk", "typescript", idempotentAPI)
	if err := sdkgen.WriteSDKWithTools(sdkOutput, fixture.endpoints, sdkDir, fixedClock); err != nil {
		t.Fatalf("write the SDK: %v", err)
	}
	typesPackage := naming.Default().NpmTypesPackage(idempotentAPI)
	link(t, tree.typesDirs[typesPackage], filepath.Join(sdkDir, "node_modules", filepath.FromSlash(typesPackage)))

	tree.env = []string{"SDK_DIR=" + sdkDir, "IDEMPOTENCY_DDL=" + ddl}
	tree.runTest(t, "idempotent_runtime.test.ts")
}
