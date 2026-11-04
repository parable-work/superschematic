package behaviorlayout_test

import (
	"strings"
	"testing"
)

// agentsFixture is the DB service whose Job implements Lease and Retries,
// whose Session implements Presence, and whose Workspace and Account
// implement Budget, each beside its status Workflow but for Workspace
// (D104).
const agentsFixture = "../../loader/tsreader/testdata/services/fixture-agents-db"

// The layout document of the tables that implement Lease, Retries, Presence
// and Budget (D104): Job's uses are Lease's, with its seven owned fields
// (active, which the core computes, takes none), its seven operations but
// expireHolder, which runs on the table as a whole, and its directives
// sub-table, then Retries', with its eight owned fields, recordAttempt and
// its attempts sub-table, then its status; its deadline queue carries
// Lease's expiry and Retries' then. Session's Presence owns its four
// fields and arms its miss on Session's deadline queue. Account's and
// Workspace's Budget own no field (meters is computed) and keep three
// sub-tables each, checkReserve writing nothing; neither has a queue, since
// Budget arms no deadline and hears no change.
// testdata/fixture-agents-db.layout.json is the document the adapters of
// every language are written against.
func TestLayoutOfLeaseRetriesPresenceAndBudget(t *testing.T) {
	layout := layoutOfFixture(t, agentsFixture, "fixture-agents-db")
	uses := func(typeName string) string {
		td := layout.Types[typeName]
		if td == nil {
			t.Fatalf("the layout has no %s: %+v", typeName, layout.Types)
		}
		var out []string
		for _, use := range td.Uses {
			var fields []string
			for _, field := range use.Fields {
				fields = append(fields, field.Name+" "+field.Column.Column)
			}
			var operations []string
			for _, op := range use.Operations {
				operations = append(operations, op.Name+map[bool]string{true: " writes", false: ""}[op.Writes])
			}
			var tables []string
			for _, table := range use.Tables {
				var columns []string
				for _, field := range table.Fields {
					columns = append(columns, field.Name)
				}
				tables = append(tables, table.Name+" "+table.Table+" "+table.Row+" "+table.Key.Name+" ("+strings.Join(columns, ", ")+")")
			}
			out = append(out, use.ID+" "+use.Behavior+" "+use.Member+" ["+strings.Join(fields, ", ")+"] ["+strings.Join(operations, ", ")+"] ["+strings.Join(tables, ", ")+"]")
		}
		return strings.Join(out, "\n")
	}
	for _, test := range []struct {
		typeName  string
		want      []string
		deadlines string
	}{
		{"Job", []string{
			"Lease Lease lease [holder lease_holder, token lease_token, acquiredAt lease_acquired_at, renewedAt lease_renewed_at, expiresAt lease_expires_at, expiries lease_expiries, ended lease_ended] " +
				"[acquire writes, heartbeat writes, release writes, expire writes, direct writes, acknowledge writes, resetExpiries writes] " +
				"[directives job_lease_directives job_id number (token, name, data, dedupeKey, sentBy, sentAt, acknowledgedAt)]",
			"Retries Retries retries [total retries_total, classAttempts retries_class_attempts, bestScore retries_best_score, exhausted retries_exhausted, stuck retries_stuck, retryAt retries_retry_at, failingSince retries_failing_since, keyedBy retries_keyed_by] " +
				"[recordAttempt writes] " +
				"[attempts job_retries_attempts job_id number (at, failure, score, kept, signature, predicates, detail, subject, keyedBy)]",
			"status Workflow status [status status, enteredAt status_entered_at] [transition writes] []",
		}, "job_deadline_queue"},
		{"Session", []string{
			"Presence Presence presence [deadline presence_deadline, lastBeatAt presence_last_beat_at, missed presence_missed, released presence_released] [beat writes, miss writes] []",
			"status Workflow status [status status, enteredAt status_entered_at] [transition writes] []",
		}, "session_deadline_queue"},
		{"Account", []string{
			"Budget Budget budget [] " +
				"[reserve writes, checkReserve, recordUsage writes, settle writes, setLimit writes, reserveFor writes, settleFor writes, recordUsageFor writes, observe writes] " +
				"[meters account_budget_meters account_id meter (used, reserved, limitValue, periodStart, reservation, token, directed, scopeType, scopeKey, observed, observedLimit, observedAt, observedUntil), " +
				"holds account_budget_holds account_id hold (meter, innerType, innerKey, amount), " +
				"buckets account_budget_buckets account_id bucket (meter, startsAt, used)]",
			"status Workflow status [status status, enteredAt status_entered_at] [transition writes] []",
		}, ""},
		{"Workspace", []string{
			"Budget Budget budget [] " +
				"[reserve writes, checkReserve, recordUsage writes, settle writes, setLimit writes, reserveFor writes, settleFor writes, recordUsageFor writes, observe writes] " +
				"[meters workspace_budget_meters workspace_id meter (used, reserved, limitValue, periodStart, reservation, token, directed, scopeType, scopeKey, observed, observedLimit, observedAt, observedUntil), " +
				"holds workspace_budget_holds workspace_id hold (meter, innerType, innerKey, amount), " +
				"buckets workspace_budget_buckets workspace_id bucket (meter, startsAt, used)]",
		}, ""},
	} {
		if got := uses(test.typeName); got != strings.Join(test.want, "\n") {
			t.Errorf("%s's uses =\n%s\nwant\n%s", test.typeName, got, strings.Join(test.want, "\n"))
		}
		td := layout.Types[test.typeName]
		switch {
		case test.deadlines == "" && td.Deadlines != nil:
			t.Errorf("%s's deadlines = %+v, want none", test.typeName, td.Deadlines)
		case test.deadlines != "" && (td.Deadlines == nil || td.Deadlines.Table != test.deadlines):
			t.Errorf("%s's deadlines = %+v, want %s", test.typeName, td.Deadlines, test.deadlines)
		}
		if td.Changes != nil || len(td.Audit) != 0 {
			t.Errorf("%s's changes = %+v, audit = %+v, want neither", test.typeName, td.Changes, td.Audit)
		}
	}
}
