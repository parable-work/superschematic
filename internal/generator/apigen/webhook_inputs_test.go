package apigen_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
)

const webhooksAPI = "fixture-webhooks-api"

// TestWebhookInputsAllowUnknownFields holds which endpoints of
// fixture-webhooks-api decode their input dropping the keys its type does
// not declare (D87): a @webhook's input, receivePlaidEvent's and
// receiveStripeEvent's, unless its type is @strictJSON, as
// receiveLedgerEvent's is. receiveGithubEvent takes no input type.
func TestWebhookInputsAllowUnknownFields(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, webhooksAPI))
	if err != nil {
		t.Fatalf("load %s: %v", webhooksAPI, err)
	}
	if !schema.Types["PlaidError"].AllowUnknownFields || schema.Types["PlaidItemEvent"].AllowUnknownFields {
		t.Fatalf("@allowUnknownFields did not reach the IR as declared: PlaidError=%v PlaidItemEvent=%v",
			schema.Types["PlaidError"].AllowUnknownFields, schema.Types["PlaidItemEvent"].AllowUnknownFields)
	}
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  webhooksAPI,
		ModulePath:  "example.com/schemas/api/" + webhooksAPI,
		TypesModule: "example.com/schemas/types/go/" + webhooksAPI,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"receivePlaidEvent":  true,
		"receiveStripeEvent": true,
		"receiveLedgerEvent": false,
		"receiveGithubEvent": false,
		"getEvent":           false,
	}
	for _, endpoint := range output.Endpoints {
		if expected, ok := want[endpoint.Name]; ok && endpoint.InputAllowsUnknownFields != expected {
			t.Errorf("%s: InputAllowsUnknownFields = %v, want %v", endpoint.Name, endpoint.InputAllowsUnknownFields, expected)
		}
	}
}

// TestWebhookRoutesDropUndeclaredKeys generates the Go types and API
// modules of fixture-webhooks-api, copies
// testdata/webhook_inputs_routes_test.go into the API module and runs it:
// a @webhook's route reads only the keys its input type declares, spelled
// as declared, at the top level and in a nested @allowUnknownFields type,
// and checks them, while a @strictJSON input refuses a key it does not
// declare (D87).
func TestWebhookRoutesDropUndeclaredKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, webhooksAPI))
	if err != nil {
		t.Fatalf("load %s: %v", webhooksAPI, err)
	}
	apiDir := writeGoAPIModule(t, schema, webhooksAPI)
	content, err := os.ReadFile(filepath.Join("testdata", "webhook_inputs_routes_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apiDir, "webhook_inputs_routes_test.go"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	runGoAPIModule(t, apiDir)
}
