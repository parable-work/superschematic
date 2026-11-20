package rustrestgen

import (
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// The numbering fixture (D103's step 4): fixture-numbering-api serves
// InvoiceActions and TicketActions, @behaviorOperations of Invoice and
// Ticket for any signed-in caller, over fixture-numbering-db, its one DB
// dependency, whose Invoice is numbered per customer and per year on its
// number field and whose Ticket carries a number and a reference. Each
// field's assignNumber is a route of the row under the field, run through
// the behaviors' core with run_operation; the API runs no worker.
var numberingFixture = behaviorFixture{api: "fixture-numbering-api", db: "fixture-numbering-db"}

// TestWriteRustAPIGoldenNumbering pins the crate of fixture-numbering-api:
// the router mounts each Numbering field's assignNumber under the field,
// each run through the behaviors' core with run_operation, and the layout
// it embeds holds each use's shared table of counts. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenNumbering -update
func TestWriteRustAPIGoldenNumbering(t *testing.T) {
	_, db, reg := numberingFixture.load(t)
	output := numberingFixture.generate(t, db)
	deps := behaviorDeps(behaviorLayout(t, db, reg))
	deps.Database = numberingFixture.db
	deps.DatabaseTypesCrate = naming.Default().RustTypesCrate(numberingFixture.db)
	deps.DatabaseTypesPath = "../../types/rust/" + numberingFixture.db
	if err := output.SetDeps(deps); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	var behaviors []string
	for _, endpoint := range output.Endpoints {
		if b := endpoint.Behavior; b != nil {
			behaviors = append(behaviors, endpoint.Namespace+"."+endpoint.Name+" "+strings.ToUpper(endpoint.Method)+" "+endpoint.Path+" "+b.Type+"."+b.Use+"."+b.Operation)
		}
	}
	slices.Sort(behaviors)
	if want := []string{
		"invoice.numberAssignNumber POST /api/invoice/{id}/number/assign-number Invoice.number.assignNumber",
		"ticket.numberAssignNumber POST /api/ticket/{id}/number/assign-number Ticket.number.assignNumber",
		"ticket.referenceAssignNumber POST /api/ticket/{id}/reference/assign-number Ticket.reference.assignNumber",
	}; !slices.Equal(behaviors, want) {
		t.Errorf("behavior operations\n%q, want\n%q", behaviors, want)
	}
	if len(output.Workers) != 0 || len(output.BehaviorWorkers) != 0 {
		t.Errorf("workers %+v and behavior workers %+v, want none", output.Workers, output.BehaviorWorkers)
	}
	generated := writeGoldenAPI(t, numberingFixture.api, output)
	for file, want := range map[string][]string{
		"src/router.rs": {
			"behaviors::run_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Invoice\",\n        &args.id,\n        \"number\",\n        \"assignNumber\",",
			"behaviors::run_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Ticket\",\n        &args.id,\n        \"reference\",\n        \"assignNumber\",",
		},
		"src/behaviors.rs": {
			`pub const RULES_LAYOUT: &str = r#"{`,
			`"invoice_number_counts"`,
			`"ticket_reference_counts"`,
		},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q", file, text)
			}
		}
	}
}
