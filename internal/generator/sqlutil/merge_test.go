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
	ir "github.com/parable-work/superschematic/ir"
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
	if want := []string{"Board.labels.label_id", "DocumentLabel.label", "Label.parent", "LabelCode.label", "LabelNote.label", "LabelPair.first", "LabelPair.second", "LabelSlot.label", "Subscription.label"}; !slices.Equal(steps, want) {
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
		" UPDATE subscription SET deleted_at = now(), deleted_by = $3, label_id = $2 WHERE subscription.id IN (",
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
	if want := "UPDATE label_note SET label_id = $2, updated_at = now(), updated_by = COALESCE($3, updated_by), revision = label_note.revision + 1 WHERE label_note.label_id = $1"; noted.Repoint.SQL != want || noted.Repoint.Actor != AuditUpdatedBy {
		t.Errorf("LabelNote's repoint = %+v, want %s", noted.Repoint, want)
	}

	// A @manyToMany's join table: a pair that would repeat is deleted,
	// and the rest repointed, under the declaring type and its field.
	boards := byStep["Board.labels.label_id"]
	if boards.Source != "Board.labels" || len(boards.Collisions) != 1 ||
		boards.Collisions[0].SQL != "DELETE FROM board_label AS moved WHERE moved.label_id = $1 AND EXISTS (SELECT 1 FROM board_label AS held WHERE held.label_id = $2 AND held.board_id = moved.board_id)" ||
		boards.Repoint.SQL != "UPDATE board_label SET label_id = $2 WHERE label_id = $1" {
		t.Errorf("Board.labels' step = %+v", boards)
	}
	// A partial index that holds nulls alike: both sides meet its where,
	// and a null matches a null.
	slotted := byStep["LabelSlot.label"]
	if len(slotted.Collisions) != 1 || !strings.Contains(slotted.Collisions[0].SQL, "WHERE label_slot.label_id = $1 AND (open)") || !strings.Contains(slotted.Collisions[0].SQL, "moved.k1 IS NOT DISTINCT FROM held.k1") {
		t.Errorf("LabelSlot.label = %+v, want its where on both sides and nulls held alike", slotted)
	}
	// Two relations of one table: a step each, and the key that holds
	// both is a collision of each.
	if first, second := byStep["LabelPair.first"], byStep["LabelPair.second"]; len(first.Collisions) != 1 || len(second.Collisions) != 1 {
		t.Errorf("LabelPair's steps = %+v and %+v, want one collision each", first, second)
	}

	// A @unique relation: every row that references the row merged away
	// collides when one references the row that stays.
	customers := mergePlan(t, "fixture-merge-db", "Customer", false)
	if want := "SELECT (id = $1) FROM customer WHERE id IN ($1, $2) ORDER BY id FOR UPDATE"; customers.Lock != want {
		t.Errorf("Customer's lock = %s, want %s", customers.Lock, want)
	}
	if customers.RequiresUser || !plan.RequiresUser {
		t.Errorf("RequiresUser = %t for Customer and %t for Label; a merge needs a user when it stamps a row deleted, which only Label's does", customers.RequiresUser, plan.RequiresUser)
	}
	// A @hasMany whose target declares no relation back: the column the
	// build adds to the target is repointed, and no key holds it.
	if address := customers.Steps[0]; address.Source != "Address" || address.Field != "customer_id" || len(address.Collisions) != 0 || address.Repoint.SQL != "UPDATE address SET customer_id = $2 WHERE address.customer_id = $1" {
		t.Errorf("Customer.addresses' step = %+v, want the repoint of address.customer_id", address)
	}
	profile := customers.Steps[1]
	if profile.Source != "CustomerProfile" || len(profile.Collisions) != 1 || !strings.Contains(profile.Collisions[0].SQL, "WHERE EXISTS (SELECT 1 FROM held))") {
		t.Errorf("CustomerProfile.customer = %+v, want one collision of every row", profile)
	}
}

// TestMergePlanOfReach holds the rows each step of a merge writes as
// per-record access holds them (D156), which an ORM reads with the
// conditions of its caller's write added before a statement writes: the
// referencing rows of a relation's table, of a relation of the table to
// itself, and of the table a @hasMany adds its column to, by the column
// that references `from`; for a @manyToMany's join table, the rows of the
// declaring type whose pairs name `from`, since a pair is its declaring
// row's; and none for the pairs the merged table declares, which are
// `from`'s own, nor for an Aliased use's aliases and names. On SQLite the
// read takes ?1.
func TestMergePlanOfReach(t *testing.T) {
	reach := func(plan *MergePlan) map[string]*MergeReach {
		out := map[string]*MergeReach{}
		for _, step := range plan.Steps {
			out[step.Source+"."+step.Field] = step.Reach
		}
		return out
	}
	labels := reach(mergePlan(t, "fixture-merge-db", "Label", false))
	for step, want := range map[string]MergeReach{
		"DocumentLabel.label":   {Type: "DocumentLabel", Rows: "SELECT 1 FROM document_label WHERE document_label.label_id = $1"},
		"Label.parent":          {Type: "Label", Rows: "SELECT 1 FROM label WHERE label.parent_id = $1"},
		"LabelPair.second":      {Type: "LabelPair", Rows: "SELECT 1 FROM label_pair WHERE label_pair.second_id = $1"},
		"Board.labels.label_id": {Type: "Board", Rows: "SELECT 1 FROM board WHERE board.id IN (SELECT board_id FROM board_label WHERE label_id = $1)"},
	} {
		if got := labels[step]; got == nil || *got != want {
			t.Errorf("%s's reach = %+v, want %+v", step, got, want)
		}
	}
	customers := reach(mergePlan(t, "fixture-merge-db", "Customer", false))
	if got, want := customers["Address.customer_id"], (MergeReach{Type: "Address", Rows: "SELECT 1 FROM address WHERE address.customer_id = $1"}); got == nil || *got != want {
		t.Errorf("Customer.addresses' reach = %+v, want %+v", got, want)
	}
	boards := reach(mergePlan(t, "fixture-merge-db", "Board", true))
	if got, ok := boards["Board.labels.board_id"]; !ok || got != nil {
		t.Errorf("Board.labels' reach from the board merged away = %+v (%t), want none: the pairs are its own", got, ok)
	}
	companies := mergePlan(t, "fixture-aliased-db", "Company", false)
	for _, step := range companies.Steps {
		if step.Source == "CompanyNameAliases" || step.Source == "CompanyNameNames" {
			if step.Reach != nil {
				t.Errorf("%s.%s's reach = %+v, want none: an Aliased use's rows are the merged rows' own", step.Source, step.Field, step.Reach)
			}
		} else if step.Reach == nil {
			t.Errorf("%s.%s has no reach", step.Source, step.Field)
		}
	}

	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-merge-sqlite-db"), loader.WithRegistry(registry.New(naming.Default())))
	if err != nil {
		t.Fatal(err)
	}
	tables, err := TablesOf(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	sqlite, err := SQLiteMergePlanOf(schema, tables, "Label")
	if err != nil {
		t.Fatal(err)
	}
	if got := reach(sqlite)["DocumentLabel.label"]; got == nil || got.Rows != "SELECT 1 FROM document_label WHERE document_label.label_id = ?1" {
		t.Errorf("DocumentLabel.label's reach on SQLite = %+v, want its read taking ?1", got)
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

// TestMergePlanOfUniqueIndexes: a version graph's table that references a
// Mergeable one is refused at the build; two unique indexes over the same fields,
// apart in their where, are a collision each, since each holds rows of its
// own; and a unique index that lists no keys on a table that references a
// Mergeable one is refused at the build, since a merge could not tell
// whether it holds the relation's column and would fail on the database's
// unique violation (D147).
func TestMergePlanOfUniqueIndexes(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-merge-db"), loader.WithRegistry(registry.New(naming.Default())))
	if err != nil {
		t.Fatal(err)
	}
	tagged := schema.Types["DocumentLabel"]
	tagged.Indexes = append(tagged.Indexes, ir.IndexDef{Keys: []string{"document", "label"}, Unique: true, Name: "pinned", Where: "document_id IS NOT NULL"})
	tables, err := TablesOf(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := MergePlanOf(schema, tables, "Label")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range plan.Steps {
		if step.Source != "DocumentLabel" {
			continue
		}
		if len(step.Collisions) != 2 || !strings.Contains(step.Collisions[1].SQL, "AND (document_id IS NOT NULL)") {
			t.Errorf("DocumentLabel.label has the collisions %+v, want one per unique index over its fields", step.Collisions)
		}
	}

	// A version graph's table that references the merged table is refused:
	// a commit made before a merge would still reference the row merged
	// away.
	tagged.GraphMember = &ir.GraphMemberConfig{}
	if _, err := MergePlanOf(schema, tables, "Label"); err == nil || !strings.Contains(err.Error(), "DocumentLabel.label references Label, which implements Mergeable, and DocumentLabel is a version graph's table") {
		t.Errorf("MergePlanOf with a graph member that references the table = %v, want it refused", err)
	}
	tagged.GraphMember = nil

	coded := schema.Types["LabelCode"]
	coded.Indexes = append(coded.Indexes, ir.IndexDef{Unique: true, Name: "code_only", Expression: "lower(code)"})
	if _, err := MergePlanOf(schema, tables, "Label"); err == nil || !strings.Contains(err.Error(), "LabelCode has a unique @index that lists no keys (code_only), and LabelCode.label references Label, which implements Mergeable") {
		t.Errorf("MergePlanOf with a unique index that lists no keys = %v, want it refused", err)
	}
}
