package sqlgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// A relative recurrence's target trigger (#626): fixture-recurrence-db's
// Filing reads Contract.endsOn through its contract, so contracts keeps one
// trigger for filings: after an update that moves ends_on, it writes a
// message of FilingChange for each filing whose contract_id names the row,
// its image before and after the filing as it is. Postgres's is a function
// and an AFTER UPDATE trigger, SQLite's one update trigger whose WHEN
// compares the date.
func TestTargetChangeTriggers(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-recurrence-db"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-recurrence-db"})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.TargetChangeTriggers) != 1 {
		t.Fatalf("target triggers = %+v, want one", output.TargetChangeTriggers)
	}
	c := output.TargetChangeTriggers[0]
	got := strings.Join([]string{c.Origin, c.Target, c.Queue, c.Table, c.Hearer, c.QueueTable, c.FunctionName, c.TriggerName, c.TargetKey, c.Via, c.HearerKey, strings.Join(c.Dates, ",")}, " ")
	if want := "Filing Contract FilingChange contract filing filing_change_queue contract_filing_dates trg_contract_filing_dates id contract_id id ends_on"; got != want {
		t.Fatalf("target trigger =\n%s\nwant\n%s", got, want)
	}
	if trigger := c.Trigger(); trigger.Timing != "AFTER UPDATE" || trigger.Function != c.FunctionName || trigger.Table != "contract" {
		t.Errorf("Postgres trigger = %+v", trigger)
	}
	pg := c.FunctionSQL()
	for _, want := range []string{
		`IF NOT (OLD."ends_on" IS DISTINCT FROM NEW."ends_on") THEN`,
		`INSERT INTO "filing_change_queue" ("row_key", "write", "at", "before", "after", "due_at", "created_at")`,
		`SELECT h."id", 'update', floor(extract(epoch FROM written) * 1000)::bigint, jsonb_build_object(`,
		`FROM "filing" h`,
		`WHERE h."contract_id" = NEW."id";`,
	} {
		if !strings.Contains(pg, want) {
			t.Errorf("Postgres's function lacks %q:\n%s", want, pg)
		}
	}
	// Its message's images are the filing's change trigger's, before and
	// after alike, so the write moves nothing of the filing.
	var change ChangeTrigger
	for _, each := range output.ChangeTriggers {
		if each.Origin == "Filing" {
			change = each
		}
	}
	if image := change.pgImage("h"); !strings.Contains(pg, image+", "+image+", written, written") {
		t.Errorf("Postgres's message is not the filing's image twice:\n%s", pg)
	}
	sqlite := c.SQLiteTrigger(sqliteQuote)
	for _, want := range []string{
		`CREATE TRIGGER "trg_contract_filing_dates" AFTER UPDATE ON "contract" FOR EACH ROW`,
		`WHEN OLD."ends_on" IS NOT NEW."ends_on"`,
		`INSERT INTO "filing_change_queue" ("row_key", "write", "at", "before", "after")`,
		`WHERE h."contract_id" = NEW."id";`,
	} {
		if !strings.Contains(sqlite.Definition, want) {
			t.Errorf("SQLite's trigger lacks %q:\n%s", want, sqlite.Definition)
		}
	}
	// create.sql creates it after the change triggers, and drop.sql drops
	// it before them.
	sqlDir := t.TempDir()
	if err := WriteDDL(output, sqlDir); err != nil {
		t.Fatal(err)
	}
	for file, before := range map[string][2]string{
		"create.sql": {"CREATE OR REPLACE FUNCTION \"filing_changes\"()", "CREATE OR REPLACE FUNCTION \"contract_filing_dates\"()"},
		"drop.sql":   {"DROP TRIGGER IF EXISTS trg_contract_filing_dates", "DROP TRIGGER IF EXISTS trg_filing_changes"},
	} {
		data, err := os.ReadFile(filepath.Join(sqlDir, file))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		first, second := strings.Index(text, before[0]), strings.Index(text, before[1])
		if first < 0 || second < 0 || first > second {
			t.Errorf("%s does not hold %q before %q", file, before[0], before[1])
		}
	}
}
