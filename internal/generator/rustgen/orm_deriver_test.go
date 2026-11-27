package rustgen

import (
	_ "embed"
	"testing"
)

//go:embed testdata/orm/derived_rules.rs
var derivedRulesTest string

// TestRepositoriesRunTheDeriverLoop holds the HTTP runtime's Rust deriver
// loop (superschematic_http_runtime::deriver, D146) to the repositories of
// fixture-derived-provenance-db, whose tables Ticket and Article implement
// Derived, on the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, as
// TestDerivedTablesEndToEnd holds the TypeScript runtime's runDeriver to the
// TypeScript repositories: the loop pulls each table's stale fields
// through its typed repository's staleDerivations, opened as a caller
// holding the Derived configs' permissions in a transaction per call, as a
// Rust deriver's generated main.rs runs them, hands each to its answer,
// and settles the answers through settleDerivations, so each derived field
// holds its value and nothing is stale after; a value the field's enum
// refuses counts an attempt and writes nothing; and a caller without the
// permission ends the loop with the table named.
//
// It needs cargo, which it skips without, and runs outside -short. Without
// the database the cases are skipped, and the test says so.
func TestRepositoriesRunTheDeriverLoop(t *testing.T) {
	runBehaviorRepositoriesWith(t, "fixture-derived-provenance-db", "derived_rules", derivedRulesTest, true)
}
