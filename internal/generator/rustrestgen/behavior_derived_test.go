package rustrestgen

import (
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// The fixture of Derived and Provenance (D73 on D103's step 4):
// fixture-derived-provenance-api serves TicketTriage and ArticleSummaries,
// @behaviorOperations of Ticket and Article for any signed-in caller, over
// fixture-derived-provenance-db, its one DB dependency. Derived's pulls and
// settles are routes of the table as a whole, run through the behaviors'
// core with run_table_operation, and the rest routes of the row under each
// use's member, run with run_operation; the API runs no worker.
var derivedProvenanceFixture = behaviorFixture{api: "fixture-derived-provenance-api", db: "fixture-derived-provenance-db"}

// TestWriteRustAPIGoldenDerivedProvenance pins the crate of
// fixture-derived-provenance-api: the router mounts each of Derived's and
// Provenance's operations, and the layout it embeds holds each use's
// sub-table and each derived field's schema. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenDerivedProvenance -update
func TestWriteRustAPIGoldenDerivedProvenance(t *testing.T) {
	_, db, reg := derivedProvenanceFixture.load(t)
	output := derivedProvenanceFixture.generate(t, db)
	deps := behaviorDeps(behaviorLayout(t, db, reg))
	deps.Database = derivedProvenanceFixture.db
	deps.DatabaseTypesCrate = naming.Default().RustTypesCrate(derivedProvenanceFixture.db)
	deps.DatabaseTypesPath = "../../types/rust/" + derivedProvenanceFixture.db
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
		"article-summaries.derivedApplyDerived POST /api/article/{id}/derived/apply-derived Article.Derived.applyDerived",
		"article-summaries.derivedFailedDerivations POST /api/article/derived/failed-derivations Article.Derived.failedDerivations",
		"article-summaries.derivedRederive POST /api/article/{id}/derived/rederive Article.Derived.rederive",
		"article-summaries.derivedSettleDerivations POST /api/article/derived/settle-derivations Article.Derived.settleDerivations",
		"article-summaries.derivedStaleDerivations POST /api/article/derived/stale-derivations Article.Derived.staleDerivations",
		"ticket-triage.derivedApplyDerived POST /api/ticket/{id}/derived/apply-derived Ticket.Derived.applyDerived",
		"ticket-triage.derivedFailedDerivations POST /api/ticket/derived/failed-derivations Ticket.Derived.failedDerivations",
		"ticket-triage.derivedRederive POST /api/ticket/{id}/derived/rederive Ticket.Derived.rederive",
		"ticket-triage.derivedSettleDerivations POST /api/ticket/derived/settle-derivations Ticket.Derived.settleDerivations",
		"ticket-triage.derivedStaleDerivations POST /api/ticket/derived/stale-derivations Ticket.Derived.staleDerivations",
		"ticket-triage.provenanceAccept POST /api/ticket/{id}/provenance/accept Ticket.Provenance.accept",
		"ticket-triage.provenanceReject POST /api/ticket/{id}/provenance/reject Ticket.Provenance.reject",
		"ticket-triage.statusTransition POST /api/ticket/{id}/status/transition Ticket.status.transition",
	}; !slices.Equal(behaviors, want) {
		t.Errorf("behavior operations\n%q, want\n%q", behaviors, want)
	}
	if len(output.Workers) != 0 || len(output.BehaviorWorkers) != 0 {
		t.Errorf("workers %+v and behavior workers %+v, want none", output.Workers, output.BehaviorWorkers)
	}
	generated := writeGoldenAPI(t, derivedProvenanceFixture.api, output)
	for file, want := range map[string][]string{
		"src/behaviors.rs": {
			`pub const RULES_LAYOUT: &str = r#"{`,
			`"ticket_derived_derivations"`,
			`"ticket_provenance_records"`,
			`"enum": [`,
		},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q", file, text)
			}
		}
	}
}
