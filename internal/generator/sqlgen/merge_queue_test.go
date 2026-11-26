package sqlgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// mergeQueueFixture is a Mergeable table, Account, whose key the messages
// of three keyed queues hold (D147, D80): one whose key is two fields an
// enqueue writes, the account's key among them, and two that the
// @enqueueOnChange triggers of tables referencing Account write, keyed by
// the referencing row's account and another column.
const mergeQueueFixture = "fixture-merge-queue-db"

// mergeQueuePlan loads mergeQueueFixture and writes Account's merge.
func mergeQueuePlan(t *testing.T) (*ir.Schema, *sqlutil.MergePlan) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, mergeQueueFixture))
	if err != nil {
		t.Fatalf("load %s: %v", mergeQueueFixture, err)
	}
	tables, err := sqlutil.TablesOf(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := sqlutil.MergePlanOf(schema, tables, "Account")
	if err != nil {
		t.Fatal(err)
	}
	return schema, plan
}

// TestMergePlanLeavesQueuesOut: a queue's message holds a row's key as a
// value and never a relation to its table, which verification refuses, so
// no unique key of a queue's table holds a column that references the
// merged table. The merge of Account has a step for each table that
// references it and none for a queue, and no statement of it writes a
// queue's table: a message keyed by the account merged away stays as it
// was written.
func TestMergePlanLeavesQueuesOut(t *testing.T) {
	schema, plan := mergeQueuePlan(t)
	var steps []string
	for _, step := range plan.Steps {
		steps = append(steps, step.Source+"."+step.Field)
	}
	if want := []string{"Contact.account", "Membership.account"}; !slices.Equal(steps, want) {
		t.Fatalf("the merge's steps are %v, want %v", steps, want)
	}
	var queues []string
	for _, td := range schema.Queues() {
		queues = append(queues, QueueTableName(td))
	}
	if want := []string{"account_synced_queue", "contact_changed_queue", "membership_changed_queue"}; !slices.Equal(queues, want) {
		t.Fatalf("the fixture's queues' tables are %v, want %v", queues, want)
	}
	statements := []string{plan.Lock}
	for _, step := range plan.Steps {
		for _, collision := range step.Collisions {
			statements = append(statements, collision.SQL)
		}
		statements = append(statements, step.Repoint.SQL)
	}
	for _, sql := range statements {
		for _, queue := range queues {
			if strings.Contains(sql, queue) {
				t.Errorf("a statement of the merge names the queue table %s: %s", queue, sql)
			}
		}
	}
}

// TestMergeThroughKeyedQueuesOnPostgres runs Account's merge, as the ORMs
// run it, on a real Postgres with mergeQueueFixture's create.sql;
// testdata/pgcheck's TestMergeQueueKeysOnPostgres holds the checks. The
// merge's repoints and its collision's delete fire the referencing tables'
// triggers, which enqueue onto the account that stays, under keys whose
// ready or claimed message is already there, and no write is a unique
// violation of a queue's key. Set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL
// as for TestProjectionMigrationsOnPostgres.
func TestMergeThroughKeyedQueuesOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to run a merge through keyed queues against Postgres")
	}
	schema, plan := mergeQueuePlan(t)
	output, err := Generate(schema, Options{SchemaName: mergeQueueFixture})
	if err != nil {
		t.Fatal(err)
	}
	sqlDir := t.TempDir()
	if err := WriteDDL(output, sqlDir); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sqlDir, "merge.json"), encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	runPGCheck(t, "TestMergeQueueKeysOnPostgres",
		"PGCHECK_DATABASE_URL="+dsn,
		"PGCHECK_SQL_DIR="+sqlDir,
	)
}
