package rustgen

import (
	_ "embed"
	"os"
	"os/exec"
	"regexp"
	"testing"
)

//go:embed testdata/orm/dispatch_fences.rs
var dispatchFencesTest string

// TestFencesOnSQLiteAndPostgres generates the Rust types of
// fixture-fenced-db with both dialects' repositories, builds the database
// from the SQLite DDL and from the Postgres DDL the build writes, and runs
// one script of writes through each (D119), reading every @fenced
// counter's exact value after every write: an update, an update of many
// rows and an upsert's update advance the fence that covers a column they
// set by one, and no other; a write of uncovered columns alone advances
// none; a write that sets a fence itself is written as it stands; a fence
// with no covers advances on every update; and a soft delete advances the
// fence that covers deletedAt. Each DDL holds the fences' triggers, so
// every write is one they allow, and the script tries ones they refuse.
//
// It needs cargo and the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, which it skips without, and
// runs outside -short.
func TestFencesOnSQLiteAndPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the repositories' fences in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the repositories' fences on SQLite and on Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the repositories' fences")
	}
	out := runRepositoriesOnBoth(t, cargo, "fixture-fenced-db", "dispatch_fences", dispatchFencesTest)
	for _, dialect := range []string{"SQLite", "Postgres"} {
		held := regexp.MustCompile(dialect + ` holds \d+ fence triggers`).Find(out)
		if held == nil {
			t.Fatalf("the script did not run on %s:\n%s", dialect, out)
		}
		t.Logf("%s", held)
	}
}
