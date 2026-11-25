package sqlutil_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	. "github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

// mergePlan loads fixture and writes the merge of its table of type name,
// which mark first marks Mergeable when the fixture does not.
func mergePlan(t *testing.T, fixture, name string, mark bool) *MergePlan {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture), loader.WithRegistry(registry.New(naming.Default())))
	if err != nil {
		t.Fatal(err)
	}
	if mark {
		schema.Types[name].Mergeable = true
	}
	tables, err := TablesOf(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := MergePlanOf(schema, tables, name)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// TestMergePlanOfEnumeratesRelationsAndKeys holds what a merge is written
// from (D147): every to-one relation of the schema that references the
// table, by source and field, the table's own to itself with the read
// that refuses a row turned on itself; one collision per unique key that
// holds the relation's column, a key over columns matching its other
// columns, a @unique relation every row, and an index over an expression
// the expression with the relation's column as the row that stays; a
// stamp where the source deletes softly and the index holds live rows
// alone, and a delete anywhere else; a repoint of every row, deleted ones
// among them, with the source's updatedAt and updatedBy; and a lock of
// the two rows, live ones on a soft-deletable table, in key order.
func TestMergePlanOfEnumeratesRelationsAndKeys(t *testing.T) {
	plan := mergePlan(t, "fixture-merge-db", "Label", false)
	if want := "SELECT (id = $1) FROM label WHERE id IN ($1, $2) AND deleted_at IS NULL ORDER BY id FOR UPDATE"; plan.Lock != want {
		t.Errorf("lock = %s\nwant   %s", plan.Lock, want)
	}
	if plan.HistoryActor {
		t.Error("the plan sets a history actor, and no table of the fixture is versioned")
	}
	var steps []string
	byStep := map[string]MergeStep{}
	for _, step := range plan.Steps {
		steps = append(steps, step.Source+"."+step.Field)
		byStep[step.Source+"."+step.Field] = step
	}
	if want := []string{"DocumentLabel.label", "Label.parent", "LabelCode.label", "LabelNote.label", "Subscription.label"}; !slices.Equal(steps, want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}

	tagged := byStep["DocumentLabel.label"]
	if len(tagged.Collisions) != 1 || tagged.Collisions[0].Actor != "" || tagged.SelfReference != "" {
		t.Fatalf("DocumentLabel.label = %+v, want one collision, of its unique index", tagged)
	}
	if want := "WITH moved AS (SELECT document_label.id AS merge_key, document_label.document_id AS k1 FROM document_label WHERE document_label.label_id = $1), " +
		"held AS (SELECT document_label.document_id AS k1 FROM document_label WHERE document_label.label_id = $2) " +
		"DELETE FROM document_label WHERE document_label.id IN (SELECT moved.merge_key FROM moved WHERE EXISTS (SELECT 1 FROM held WHERE moved.k1 = held.k1))"; tagged.Collisions[0].SQL != want {
		t.Errorf("DocumentLabel's collision =\n%s\nwant\n%s", tagged.Collisions[0].SQL, want)
	}
	if want := "UPDATE document_label SET label_id = $2 WHERE document_label.label_id = $1"; tagged.Repoint.SQL != want || tagged.Repoint.Actor != "" {
		t.Errorf("DocumentLabel's repoint = %+v, want %s", tagged.Repoint, want)
	}

	// A relation of the table to itself: no key holds it, and the row that
	// stays must not reference the row merged away.
	parent := byStep["Label.parent"]
	if want := "SELECT 1 FROM label WHERE label.id = $2 AND label.parent_id = $1"; parent.SelfReference != want || len(parent.Collisions) != 0 {
		t.Errorf("Label.parent = %+v, want the read %s and no collision", parent, want)
	}

	// A soft-deletable source whose index holds live rows alone: the row
	// is stamped, by the acting user, and both sides are live rows.
	followed := byStep["Subscription.label"]
	if len(followed.Collisions) != 1 || followed.Collisions[0].Actor != AuditDeletedBy {
		t.Fatalf("Subscription.label = %+v, want one collision that stamps deletedBy", followed)
	}
	for _, want := range []string{
		"WHERE subscription.label_id = $1 AND (deleted_at IS NULL)",
		"WHERE subscription.label_id = $2 AND (deleted_at IS NULL)",
		" UPDATE subscription SET deleted_at = now(), deleted_by = $3 WHERE subscription.id IN (",
		"WHERE moved.k1 = held.k1",
	} {
		if !strings.Contains(followed.Collisions[0].SQL, want) {
			t.Errorf("Subscription's collision has no %q:\n%s", want, followed.Collisions[0].SQL)
		}
	}
	if strings.Contains(followed.Repoint.SQL, "deleted_at") {
		t.Errorf("Subscription's repoint leaves deleted rows out, which still reference the row: %s", followed.Repoint.SQL)
	}

	// An index over an expression that reads the relation's column: the
	// moved row's key is the expression over its values with the column
	// as the row that stays.
	coded := byStep["LabelCode.label"]
	if len(coded.Collisions) != 1 {
		t.Fatalf("LabelCode.label = %+v, want one collision", coded)
	}
	for _, want := range []string{
		"(SELECT ((CAST(label_id AS text) || ':' || lower(code))) FROM (VALUES (CAST($2 AS UUID), label_code.code)) AS label_code (label_id, code)) AS k1",
		"held AS (SELECT ((CAST(label_id AS text) || ':' || lower(code))) AS k1 FROM label_code WHERE label_code.label_id = $2)",
		" DELETE FROM label_code WHERE label_code.id IN (",
	} {
		if !strings.Contains(coded.Collisions[0].SQL, want) {
			t.Errorf("LabelCode's collision has no %q:\n%s", want, coded.Collisions[0].SQL)
		}
	}

	// The audit fields a source keeps are stamped: updatedBy only when a
	// user acts.
	noted := byStep["LabelNote.label"]
	if want := "UPDATE label_note SET label_id = $2, updated_at = now(), updated_by = COALESCE($3, updated_by) WHERE label_note.label_id = $1"; noted.Repoint.SQL != want || noted.Repoint.Actor != AuditUpdatedBy {
		t.Errorf("LabelNote's repoint = %+v, want %s", noted.Repoint, want)
	}

	// A @unique relation: every row that references the row merged away
	// collides when one references the row that stays.
	customers := mergePlan(t, "fixture-merge-db", "Customer", false)
	if want := "SELECT (id = $1) FROM customer WHERE id IN ($1, $2) ORDER BY id FOR UPDATE"; customers.Lock != want {
		t.Errorf("Customer's lock = %s, want %s", customers.Lock, want)
	}
	profile := customers.Steps[0]
	if profile.Source != "CustomerProfile" || len(profile.Collisions) != 1 || !strings.Contains(profile.Collisions[0].SQL, "WHERE EXISTS (SELECT 1 FROM held))") {
		t.Errorf("CustomerProfile.customer = %+v, want one collision of every row", profile)
	}
}

// TestMergePlanOfLeavesBehaviorSubTablesOut: a behavior's sub-table
// references its row, and a merge does not repoint it: its rows are the
// behavior's state of the row merged away (D147). fixture-comments-db's
// Ticket keeps Comments' sub-tables, and its merge names none of them.
func TestMergePlanOfLeavesBehaviorSubTablesOut(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-comments-db"), loader.WithRegistry(registry.New(naming.Default())))
	if err != nil {
		t.Fatal(err)
	}
	subTables := 0
	for _, td := range schema.Types {
		if td.BehaviorTable != nil && td.BehaviorTable.Row == "Ticket" {
			subTables++
		}
	}
	if subTables == 0 {
		t.Fatal("fixture-comments-db's Ticket keeps no sub-table, so the test holds nothing")
	}
	plan := mergePlan(t, "fixture-comments-db", "Ticket", true)
	for _, step := range plan.Steps {
		if td := schema.Types[step.Source]; td == nil || td.BehaviorTable != nil {
			t.Errorf("the merge repoints %s.%s, a behavior's sub-table", step.Source, step.Field)
		}
	}
	if _, err := MergePlanOf(schema, nil, "Ticket"); err == nil || !strings.Contains(err.Error(), "the Mergeable type Ticket is no table of") {
		t.Errorf("MergePlanOf without the table = %v, want it refused", err)
	}
}
