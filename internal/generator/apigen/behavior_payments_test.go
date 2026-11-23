package apigen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
)

// paymentsFixtures are the fixtures of Payments (D70 on D103's step 4):
// fixture-payments-api serves the operations of fixture-payments-db's Order
// and Refund from a @behaviorOperations set each, listPaymentActions, an
// operation of the table as a whole, among them, and runs no worker.
var paymentsFixtures = behaviorFixtures{"fixture-payments-api", "fixture-payments-db"}

// TestWriteAPIGoldenPayments generates the Go API module of
// fixture-payments-api and holds the files the tables' Payments uses
// change to their golden copies: the routes, each a handler run on the
// row, listPaymentActions' on the table as a whole; the behaviors' runner;
// the interfaces; and the OpenAPI document. Regenerate with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenPayments -update
func TestWriteAPIGoldenPayments(t *testing.T) {
	api, db := loadFixturePair(t, paymentsFixtures)
	output := generatePairAPI(t, paymentsFixtures, api, db)

	var calls []string
	for _, ep := range output.Endpoints {
		if bo := ep.BehaviorOperation(); bo == nil {
			continue
		}
		calls = append(calls, ep.Method+" "+ep.Path+" "+ep.BehaviorRepositoryCall()+" "+ep.BehaviorRunner())
	}
	slices.Sort(calls)
	want := []string{
		"POST /api/order/payments/list-payment-actions GetOrderRepository().Payments().ListPaymentActions runTableBehaviorOperation",
		"POST /api/order/{id}/payments/capture-payment GetOrderRepository().Payments().CapturePayment runBehaviorOperation",
		"POST /api/order/{id}/payments/open-payment GetOrderRepository().Payments().OpenPayment runBehaviorOperation",
		"POST /api/order/{id}/payments/record-payment-event GetOrderRepository().Payments().RecordPaymentEvent runBehaviorOperation",
		"POST /api/order/{id}/payments/refund-for GetOrderRepository().Payments().RefundFor runBehaviorOperation",
		"POST /api/order/{id}/payments/refund-payment GetOrderRepository().Payments().RefundPayment runBehaviorOperation",
		"POST /api/order/{id}/payments/settle-refund-for GetOrderRepository().Payments().SettleRefundFor runBehaviorOperation",
		"POST /api/order/{id}/payments/void-payment GetOrderRepository().Payments().VoidPayment runBehaviorOperation",
		"POST /api/order/{id}/status/transition GetOrderRepository().Status().Transition runBehaviorOperation",
		"POST /api/refund/payments/list-payment-actions GetRefundRepository().Payments().ListPaymentActions runTableBehaviorOperation",
		"POST /api/refund/{id}/payments/capture-payment GetRefundRepository().Payments().CapturePayment runBehaviorOperation",
		"POST /api/refund/{id}/payments/open-payment GetRefundRepository().Payments().OpenPayment runBehaviorOperation",
		"POST /api/refund/{id}/payments/record-payment-event GetRefundRepository().Payments().RecordPaymentEvent runBehaviorOperation",
		"POST /api/refund/{id}/payments/refund-for GetRefundRepository().Payments().RefundFor runBehaviorOperation",
		"POST /api/refund/{id}/payments/refund-payment GetRefundRepository().Payments().RefundPayment runBehaviorOperation",
		"POST /api/refund/{id}/payments/settle-refund-for GetRefundRepository().Payments().SettleRefundFor runBehaviorOperation",
		"POST /api/refund/{id}/payments/void-payment GetRefundRepository().Payments().VoidPayment runBehaviorOperation",
		"POST /api/refund/{id}/status/transition GetRefundRepository().Status().Transition runBehaviorOperation",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("the endpoints = %q, want %q", calls, want)
	}
	if len(output.Workers) != 0 {
		t.Fatalf("workers = %+v, want none: Payments hears no write and arms no deadline", output.Workers)
	}

	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
		RulesGo:         "/repo/runtime/rules/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+paymentsFixtures.api); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", paymentsFixtures.api)
	for _, name := range []string{"go.mod", "routes.go", "interfaces.go", "behaviors.go", "openapi.json"} {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}
		goldenPath := filepath.Join(goldenDir, name)
		if *update {
			if err := os.MkdirAll(goldenDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}
}

// TestPaymentsAPICompiles generates fixture-payments-db's Go types and ORM
// and fixture-payments-api's Go types and API module into a temporary tree
// shaped as the dist, and builds and vets the API module: its handlers call
// the ORM's operations under each use's member, Payments' under payments,
// listPaymentActions on the table as a whole. Linking needs the core's
// archive, which a build does not; go vet type-checks the cgo package.
func TestPaymentsAPICompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	api, db := loadFixturePair(t, paymentsFixtures)
	apiDir, _ := writeFixtureModulesWith(t, paymentsFixtures, api, db, nil)
	for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	routes, err := os.ReadFile(filepath.Join(apiDir, "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"db.GetOrderRepository().Payments().OpenPayment",
		"db.GetOrderRepository().Payments().ListPaymentActions",
		"db.GetRefundRepository().Payments().SettleRefundFor",
	} {
		if !strings.Contains(string(routes), want) {
			t.Errorf("routes.go holds no %q", want)
		}
	}
}
