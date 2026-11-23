package tsrestgen

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// paymentsAPI is the API that serves fixture-payments-db's Order and
// Refund (D70 on D103's step 4) for any signed-in caller: Payments'
// operations, refundFor and settleRefundFor among them, routes of the row
// under the use's member, listPaymentActions a route of the table as a
// whole, and the status Workflows' transition. The router serves its MCP
// tools.
const paymentsAPI = "fixture-payments-api"

// paymentsDB is fixture-payments-api's database, its one DB dependency.
const paymentsDB = "fixture-payments-db"

// paymentsFixture is fixture-payments-api with its database and apigen's
// endpoints for it, read with one registry, which declares the behaviors.
func paymentsFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, paymentsDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", paymentsDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, paymentsAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", paymentsAPI, err)
	}
	deps := map[string]*ir.Schema{paymentsDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     paymentsAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: paymentsAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, mcp: true, depsInfo: paymentsDeps}
}

// paymentsDeps is fixture-payments-api's Deps as the dispatch layer sets
// it: a pool of its database, which lists postgres first, and the
// repositories its TypeScript types export.
var paymentsDeps = DepsInfo{
	Database:             paymentsDB,
	Dialect:              "postgres",
	ORM:                  "FixturePaymentsDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-payments-db-types",
}

// TestWriteAPIGoldenBehaviorPayments pins the package for
// fixture-payments-api: its router mounts Payments' operations and the
// Workflows' on the row under each use's member, each calling the
// repository's operation that takes the row's key, listPaymentActions' on
// the table as a whole, taking none, and no behavior worker. Regenerate
// with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenBehaviorPayments -update
func TestWriteAPIGoldenBehaviorPayments(t *testing.T) {
	fixture := paymentsFixture(t)
	output := generateBehavior(t, fixture)
	if err := output.SetDeps(paymentsDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	var paths []string
	for _, ep := range fixture.endpoints.Endpoints {
		if ep.BehaviorOperation() != nil {
			paths = append(paths, ep.Method+" "+ep.Path)
		}
	}
	slices.Sort(paths)
	if want := []string{
		"POST /api/order/payments/list-payment-actions",
		"POST /api/order/{id}/payments/capture-payment",
		"POST /api/order/{id}/payments/open-payment",
		"POST /api/order/{id}/payments/record-payment-event",
		"POST /api/order/{id}/payments/refund-for",
		"POST /api/order/{id}/payments/refund-payment",
		"POST /api/order/{id}/payments/settle-refund-for",
		"POST /api/order/{id}/payments/void-payment",
		"POST /api/order/{id}/status/transition",
		"POST /api/refund/payments/list-payment-actions",
		"POST /api/refund/{id}/payments/capture-payment",
		"POST /api/refund/{id}/payments/open-payment",
		"POST /api/refund/{id}/payments/record-payment-event",
		"POST /api/refund/{id}/payments/refund-for",
		"POST /api/refund/{id}/payments/refund-payment",
		"POST /api/refund/{id}/payments/settle-refund-for",
		"POST /api/refund/{id}/payments/void-payment",
		"POST /api/refund/{id}/status/transition",
	}; !slices.Equal(paths, want) {
		t.Errorf("the behavior routes = %q, want %q", paths, want)
	}
	checkGolden(t, output, paymentsAPI)
}
