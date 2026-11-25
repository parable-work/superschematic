package ormgen

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
)

// mergeORMCases are the cases the generated module's test runs over
// fixture-merge-db, by name (D147).
var mergeORMCases = []string{
	"a merge repoints every relation that references the row and deletes it softly",
	"a row a unique index would refuse is deleted, and the rest repointed",
	"on a soft-deletable table the refused row is stamped deleted where the index holds live rows alone",
	"a unique index over an expression that reads the relation's column finds its collisions",
	"a table that deletes for good loses the row merged away, through the core",
	"a merge of a row into itself, of a row that is gone, and into a row that references it is refused and writes nothing",
	"a merge in a transaction rolls back with it",
	"a @manyToMany's join table has its pairs repointed, and one that would repeat deleted",
	"two relations of one table, a partial index and an index that holds nulls alike each find their collisions",
	"a merge that stamps a row deleted needs an acting user before it writes, and one that stamps none does not",
	"two merges of one pair at once: one folds, and the other finds its row gone",
}

// TestMergeIntoOnPostgres holds the generated Go ORM of a database whose
// tables implement Mergeable to Postgres (D147): MergeInto repoints every
// relation of the schema that references the row merged away, a relation of
// the table to itself among them, stamping updatedAt and updatedBy where
// the referencing table keeps them; deletes the referencing row a unique
// index would refuse once repointed, a key over columns, a @unique
// relation and an index over an expression each, and stamps it deleted on a
// soft-deletable table whose index holds live rows alone; deletes the row
// merged away, softly on a table with deletedAt and through the behaviors'
// core on a table whose field is a Workflow; answers how many rows of each
// table it repointed and deleted; and refuses, writing nothing, a merge of
// a row into itself, of a row that is gone and into a row that references
// the one merged away. It loads fixture-merge-db with the core's registry,
// generates its Go types, its ORM and its DDL into a temporary tree, and
// builds, vets and tests the ORM module with mergeORMTest, whose every case
// must pass on the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL. The
// module links the core's static archive, which scripts/rules-archive.sh
// stages; the test skips without it, and in -short mode.
func TestMergeIntoOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated ORM's merge end-to-end check in -short mode")
	}
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated ORM's merges against Postgres")
	}
	paths := testpaths.Local(t)
	archive := filepath.Join(paths.RulesGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperschematic_rules.a")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("the behaviors' core's archive is not staged at %s: run scripts/rules-archive.sh (make rules-archive)", archive)
	}

	ormDir := generateBehaviorORMModule(t, paths, mergeFixture)
	if err := os.WriteFile(filepath.Join(ormDir, "merge_into_test.go"), []byte(mergeORMTest), 0o644); err != nil {
		t.Fatalf("write the merge test: %v", err)
	}
	out := runORMModule(t, ormDir)
	for _, name := range mergeORMCases {
		if want := "--- PASS: TestMergeIntoOnPostgres/" + strings.ReplaceAll(name, " ", "_"); !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q:\n%s", name, out)
		}
	}
}

// mergeORMTest is the generated module's test: labels and customers merged
// through the generated repositories, in a schema of its own, with the
// rows that reference them written and read back past the repositories
// with SQL.
const mergeORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-merge-db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMergeIntoOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the merges against Postgres")
	}
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	background := context.Background()
	base, err := pgxpool.New(background, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer base.Close()
	schema := fmt.Sprintf("ormgen_merge_%d", time.Now().UnixNano())
	if _, err := base.Exec(background, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(background, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	if _, err := pool.Exec(background, string(createSQL)); err != nil {
		pool.Close()
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatalf("connect generated orm: %v", err)
	}
	defer db.Close()

	// The merges run as ada, whom the stamps name.
	ada := uuid.New()
	adaID, err := types.ParseIdentityUUID(ada.String())
	if err != nil {
		t.Fatalf("ada's id: %v", err)
	}
	ctx := WithUserID(background, adaID)

	exec := func(t *testing.T, sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	count := func(t *testing.T, sql string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	label := func(t *testing.T, name string) types.IdentityUUID {
		t.Helper()
		row, err := db.Label.CreateOne(ctx, &types.Label{Name: name})
		if err != nil {
			t.Fatalf("create the label %s: %v", name, err)
		}
		return *row.Id
	}
	document := func(t *testing.T, title string) types.IdentityUUID {
		t.Helper()
		row, err := db.Document.CreateOne(ctx, &types.Document{Title: title})
		if err != nil {
			t.Fatalf("create the document %s: %v", title, err)
		}
		return *row.Id
	}
	customer := func(t *testing.T, name string) types.IdentityUUID {
		t.Helper()
		row, err := db.Customer.CreateOne(ctx, &CustomerCreate{Name: name})
		if err != nil {
			t.Fatalf("create the customer %s: %v", name, err)
		}
		return *row.Id
	}
	tag := func(t *testing.T, doc, of types.IdentityUUID) {
		t.Helper()
		exec(t, "INSERT INTO document_label (id, document_id, label_id) VALUES (gen_random_uuid(), $1, $2)", doc.ToUUID(), of.ToUUID())
	}
	want := func(t *testing.T, got *MergeResult, repointed, deleted map[string]int) {
		t.Helper()
		if !reflect.DeepEqual(got.Repointed, repointed) || !reflect.DeepEqual(got.Deleted, deleted) {
			t.Fatalf("the merge repointed %v and deleted %v, want %v and %v", got.Repointed, got.Deleted, repointed, deleted)
		}
	}

	t.Run("a merge repoints every relation that references the row and deletes it softly", func(t *testing.T) {
		bug, defect := label(t, "bug"), label(t, "defect")
		crash := label(t, "crash")
		exec(t, "UPDATE label SET parent_id = $1 WHERE id = $2", defect.ToUUID(), crash.ToUUID())
		one, two := document(t, "one"), document(t, "two")
		tag(t, one, defect)
		tag(t, two, defect)
		old := time.Now().Add(-time.Hour)
		exec(t, "INSERT INTO label_note (id, label_id, text, updated_at) VALUES (gen_random_uuid(), $1, 'first', $2), (gen_random_uuid(), $1, 'second', $2)", defect.ToUUID(), old)

		result, err := db.Label.MergeInto(ctx, defect, bug)
		if err != nil {
			t.Fatalf("merge defect into bug: %v", err)
		}
		want(t, result, map[string]int{"DocumentLabel": 2, "Label": 1, "LabelNote": 2}, map[string]int{})
		if n := count(t, "SELECT count(*) FROM document_label WHERE label_id = $1", bug.ToUUID()); n != 2 {
			t.Errorf("%d documents carry bug, want 2", n)
		}
		if n := count(t, "SELECT count(*) FROM label WHERE id = $1 AND parent_id = $2", crash.ToUUID(), bug.ToUUID()); n != 1 {
			t.Errorf("crash does not sit under bug")
		}
		// The notes say who repointed them and when.
		if n := count(t, "SELECT count(*) FROM label_note WHERE label_id = $1 AND updated_by = $2 AND updated_at > $3", bug.ToUUID(), ada, old.Add(time.Minute)); n != 2 {
			t.Errorf("%d notes of bug are stamped by ada, want 2", n)
		}
		// The notes' fence covers the label, so the repoint advanced it.
		if n := count(t, "SELECT count(*) FROM label_note WHERE label_id = $1 AND revision = 1", bug.ToUUID()); n != 2 {
			t.Errorf("%d notes of bug advanced their revision, want 2", n)
		}
		// defect is deleted softly, by ada, and no live row.
		if n := count(t, "SELECT count(*) FROM label WHERE id = $1 AND deleted_at IS NOT NULL AND deleted_by = $2", defect.ToUUID(), ada); n != 1 {
			t.Errorf("defect is not stamped deleted by ada")
		}
		if _, err := db.Label.GetOne(ctx, defect, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetOne of defect = %v, want ErrNotFound", err)
		}
		if _, err := db.Label.GetOne(ctx, bug, nil); err != nil {
			t.Errorf("GetOne of bug = %v", err)
		}
		if n := count(t, "SELECT count(*) FROM document_label WHERE label_id = $1", defect.ToUUID()); n != 0 {
			t.Errorf("%d rows still reference defect", n)
		}
	})

	t.Run("a row a unique index would refuse is deleted, and the rest repointed", func(t *testing.T) {
		ui, frontend := label(t, "ui"), label(t, "frontend")
		both, only := document(t, "both"), document(t, "only")
		tag(t, both, ui)
		tag(t, both, frontend)
		tag(t, only, frontend)
		result, err := db.Label.MergeInto(ctx, frontend, ui)
		if err != nil {
			t.Fatalf("merge frontend into ui: %v", err)
		}
		want(t, result, map[string]int{"DocumentLabel": 1}, map[string]int{"DocumentLabel": 1})
		if n := count(t, "SELECT count(*) FROM document_label WHERE document_id = $1", both.ToUUID()); n != 1 {
			t.Errorf("the document that carried both carries %d labels, want 1", n)
		}
		if n := count(t, "SELECT count(*) FROM document_label WHERE label_id = $1", ui.ToUUID()); n != 2 {
			t.Errorf("%d documents carry ui, want 2", n)
		}
	})

	t.Run("on a soft-deletable table the refused row is stamped deleted where the index holds live rows alone", func(t *testing.T) {
		api, rest := label(t, "api"), label(t, "rest")
		follow := "INSERT INTO subscription (id, subscriber, label_id, deleted_at) VALUES (gen_random_uuid(), $1, $2, $3)"
		exec(t, follow, "grace", api.ToUUID(), nil)
		exec(t, follow, "grace", rest.ToUUID(), nil)
		exec(t, follow, "linus", rest.ToUUID(), nil)
		// alan left rest long ago and follows api: his old row is no live
		// row, so the index does not hold it.
		exec(t, follow, "alan", rest.ToUUID(), time.Now().Add(-time.Hour))
		exec(t, follow, "alan", api.ToUUID(), nil)
		result, err := db.Label.MergeInto(ctx, rest, api)
		if err != nil {
			t.Fatalf("merge rest into api: %v", err)
		}
		// grace's row of rest is stamped and repointed as one deleted
		// row; linus's and alan's old one are the rows repointed.
		want(t, result, map[string]int{"Subscription": 2}, map[string]int{"Subscription": 1})
		if n := count(t, "SELECT count(*) FROM subscription WHERE subscriber = 'grace' AND label_id = $1 AND deleted_at IS NULL", api.ToUUID()); n != 1 {
			t.Errorf("grace follows api %d times, want once", n)
		}
		if n := count(t, "SELECT count(*) FROM subscription WHERE subscriber = 'grace' AND label_id = $1 AND deleted_at IS NOT NULL AND deleted_by = $2", api.ToUUID(), ada); n != 1 {
			t.Errorf("grace's row of rest is not stamped deleted by ada")
		}
		if n := count(t, "SELECT count(*) FROM subscription WHERE subscriber = 'linus' AND label_id = $1 AND deleted_at IS NULL", api.ToUUID()); n != 1 {
			t.Errorf("linus does not follow api")
		}
		if n := count(t, "SELECT count(*) FROM subscription WHERE label_id = $1", rest.ToUUID()); n != 0 {
			t.Errorf("%d subscriptions still reference rest", n)
		}
	})

	t.Run("a unique index over an expression that reads the relation's column finds its collisions", func(t *testing.T) {
		kept, gone := label(t, "kept"), label(t, "gone")
		code := "INSERT INTO label_code (id, label_id, code) VALUES (gen_random_uuid(), $1, $2)"
		exec(t, code, kept.ToUUID(), "x1")
		exec(t, code, gone.ToUUID(), "X1")
		exec(t, code, gone.ToUUID(), "y2")
		result, err := db.Label.MergeInto(ctx, gone, kept)
		if err != nil {
			t.Fatalf("merge gone into kept: %v", err)
		}
		want(t, result, map[string]int{"LabelCode": 1}, map[string]int{"LabelCode": 1})
		if n := count(t, "SELECT count(*) FROM label_code WHERE label_id = $1 AND code IN ('x1', 'y2')", kept.ToUUID()); n != 2 {
			t.Errorf("kept holds %d of the codes x1 and y2, want both", n)
		}
	})

	t.Run("a table that deletes for good loses the row merged away, through the core", func(t *testing.T) {
		acme, acmeInc := customer(t, "Acme"), customer(t, "Acme Inc")
		exec(t, "INSERT INTO customer_profile (id, customer_id, bio) VALUES (gen_random_uuid(), $1, 'kept'), (gen_random_uuid(), $2, 'dropped')", acme.ToUUID(), acmeInc.ToUUID())
		exec(t, "INSERT INTO purchase (id, customer_id, total) VALUES (gen_random_uuid(), $1, 10), (gen_random_uuid(), $1, 20)", acmeInc.ToUUID())
		// An address declares no relation: its customer_id is the column
		// Customer's addresses adds, which no table's fields model.
		exec(t, "INSERT INTO address (id, line, customer_id) VALUES (gen_random_uuid(), '1 Main St', $1)", acmeInc.ToUUID())
		result, err := db.Customer.MergeInto(ctx, acmeInc, acme)
		if err != nil {
			t.Fatalf("merge Acme Inc into Acme: %v", err)
		}
		want(t, result, map[string]int{"Address": 1, "Purchase": 2}, map[string]int{"CustomerProfile": 1})
		if n := count(t, "SELECT count(*) FROM address WHERE customer_id = $1", acme.ToUUID()); n != 1 {
			t.Errorf("Acme holds %d addresses, want the one of Acme Inc", n)
		}
		if n := count(t, "SELECT count(*) FROM customer WHERE id = $1", acmeInc.ToUUID()); n != 0 {
			t.Errorf("Acme Inc is still a row")
		}
		if n := count(t, "SELECT count(*) FROM customer_profile WHERE customer_id = $1 AND bio = 'kept'", acme.ToUUID()); n != 1 {
			t.Errorf("Acme does not keep its own profile")
		}
		if n := count(t, "SELECT count(*) FROM purchase WHERE customer_id = $1", acme.ToUUID()); n != 2 {
			t.Errorf("Acme holds %d purchases, want 2", n)
		}
	})

	t.Run("a merge of a row into itself, of a row that is gone, and into a row that references it is refused and writes nothing", func(t *testing.T) {
		parent, child := label(t, "parent"), label(t, "child")
		exec(t, "UPDATE label SET parent_id = $1 WHERE id = $2", parent.ToUUID(), child.ToUUID())
		doc := document(t, "tagged")
		tag(t, doc, parent)
		if _, err := db.Label.MergeInto(ctx, parent, parent); !errors.Is(err, ErrMergeSelf) {
			t.Errorf("a merge into itself = %v, want ErrMergeSelf", err)
		}
		// child sits under parent: merged into child, parent's rows would
		// make child its own parent.
		if _, err := db.Label.MergeInto(ctx, parent, child); !errors.Is(err, ErrMergeSelfReference) {
			t.Errorf("a merge into a row that references it = %v, want ErrMergeSelfReference", err)
		}
		missing, err := types.ParseIdentityUUID(uuid.New().String())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Label.MergeInto(ctx, missing, parent); !errors.Is(err, ErrNotFound) {
			t.Errorf("a merge of a row that is gone = %v, want ErrNotFound", err)
		}
		if _, err := db.Label.MergeInto(ctx, parent, missing); !errors.Is(err, ErrNotFound) {
			t.Errorf("a merge into a row that is gone = %v, want ErrNotFound", err)
		}
		// A label deleted softly is no live row to merge.
		deleted := label(t, "deleted")
		if err := db.Label.DeleteOne(ctx, deleted); err != nil {
			t.Fatalf("delete a label: %v", err)
		}
		if _, err := db.Label.MergeInto(ctx, deleted, parent); !errors.Is(err, ErrNotFound) {
			t.Errorf("a merge of a deleted row = %v, want ErrNotFound", err)
		}
		if n := count(t, "SELECT count(*) FROM document_label WHERE document_id = $1 AND label_id = $2", doc.ToUUID(), parent.ToUUID()); n != 1 {
			t.Errorf("a refused merge moved the document's label")
		}
		if n := count(t, "SELECT count(*) FROM label WHERE id = $1 AND deleted_at IS NULL", parent.ToUUID()); n != 1 {
			t.Errorf("a refused merge deleted the row")
		}
	})

	t.Run("a merge in a transaction rolls back with it", func(t *testing.T) {
		stays, goes := label(t, "stays"), label(t, "goes")
		doc := document(t, "undone")
		tag(t, doc, goes)
		abort := errors.New("abort")
		err := db.Transaction(ctx, func(tx TxInterface) error {
			result, err := tx.GetLabelRepository().MergeInto(ctx, goes, stays)
			if err != nil {
				return err
			}
			want(t, result, map[string]int{"DocumentLabel": 1}, map[string]int{})
			return abort
		})
		if !errors.Is(err, abort) {
			t.Fatalf("the transaction = %v, want its abort", err)
		}
		if n := count(t, "SELECT count(*) FROM document_label WHERE document_id = $1 AND label_id = $2", doc.ToUUID(), goes.ToUUID()); n != 1 {
			t.Errorf("the merge outlived its transaction")
		}
		if _, err := db.Label.GetOne(ctx, goes, nil); err != nil {
			t.Errorf("the row merged away in a transaction that rolled back is gone: %v", err)
		}

		// A merge refused in a transaction writes nothing there, and the
		// transaction goes on: goes sits under stays, so stays cannot
		// fold into it, though the document's label is repointed before
		// the relation of the table to itself is read.
		other := document(t, "kept")
		tag(t, other, stays)
		exec(t, "UPDATE label SET parent_id = $1 WHERE id = $2", stays.ToUUID(), goes.ToUUID())
		err = db.Transaction(ctx, func(tx TxInterface) error {
			if _, err := tx.GetLabelRepository().MergeInto(ctx, stays, goes); !errors.Is(err, ErrMergeSelfReference) {
				return fmt.Errorf("a merge into a row that references it = %v, want ErrMergeSelfReference", err)
			}
			_, err := tx.Exec(ctx, "UPDATE document SET title = 'still kept' WHERE id = $1", other.ToUUID())
			return err
		})
		if err != nil {
			t.Fatalf("the transaction a merge was refused in: %v", err)
		}
		if n := count(t, "SELECT count(*) FROM document_label WHERE document_id = $1 AND label_id = $2", other.ToUUID(), stays.ToUUID()); n != 1 {
			t.Errorf("a merge refused in a transaction moved the document's label")
		}
		if n := count(t, "SELECT count(*) FROM document WHERE id = $1 AND title = 'still kept'", other.ToUUID()); n != 1 {
			t.Errorf("the transaction a merge was refused in lost its own write")
		}
	})

	t.Run("a @manyToMany's join table has its pairs repointed, and one that would repeat deleted", func(t *testing.T) {
		red, crimson := label(t, "red"), label(t, "crimson")
		board := func(title string, labels ...types.IdentityUUID) types.IdentityUUID {
			row, err := db.Board.CreateOne(ctx, &types.Board{Title: title})
			if err != nil {
				t.Fatalf("create the board %s: %v", title, err)
			}
			for _, of := range labels {
				exec(t, "INSERT INTO board_label (board_id, label_id) VALUES ($1, $2)", row.Id.ToUUID(), of.ToUUID())
			}
			return *row.Id
		}
		both, only := board("both", red, crimson), board("only", crimson)
		result, err := db.Label.MergeInto(ctx, crimson, red)
		if err != nil {
			t.Fatalf("merge crimson into red: %v", err)
		}
		want(t, result, map[string]int{"Board.labels": 1}, map[string]int{"Board.labels": 1})
		if n := count(t, "SELECT count(*) FROM board_label WHERE board_id = $1 AND label_id = $2", both.ToUUID(), red.ToUUID()); n != 1 {
			t.Errorf("the board that carried both carries red %d times, want once", n)
		}
		if n := count(t, "SELECT count(*) FROM board_label WHERE board_id = $1 AND label_id = $2", only.ToUUID(), red.ToUUID()); n != 1 {
			t.Errorf("the board that carried crimson does not carry red")
		}
		if n := count(t, "SELECT count(*) FROM board_label WHERE label_id = $1", crimson.ToUUID()); n != 0 {
			t.Errorf("%d pairs still reference crimson", n)
		}
	})

	t.Run("two relations of one table, a partial index and an index that holds nulls alike each find their collisions", func(t *testing.T) {
		from, into, other, more := label(t, "pair-from"), label(t, "pair-into"), label(t, "pair-other"), label(t, "pair-more")
		pair := "INSERT INTO label_pair (id, first_id, second_id) VALUES (gen_random_uuid(), $1, $2)"
		exec(t, pair, from.ToUUID(), other.ToUUID())
		exec(t, pair, into.ToUUID(), other.ToUUID())
		exec(t, pair, other.ToUUID(), from.ToUUID())
		exec(t, pair, more.ToUUID(), from.ToUUID())
		slot := "INSERT INTO label_slot (id, label_id, slot, open) VALUES (gen_random_uuid(), $1, $2, $3)"
		exec(t, slot, from.ToUUID(), nil, true)
		exec(t, slot, into.ToUUID(), nil, true)
		// A closed slot is outside the index, so it cannot collide.
		exec(t, slot, from.ToUUID(), "x", false)
		exec(t, slot, into.ToUUID(), "x", true)
		exec(t, slot, from.ToUUID(), "y", true)
		result, err := db.Label.MergeInto(ctx, from, into)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		want(t, result, map[string]int{"LabelPair": 2, "LabelSlot": 2}, map[string]int{"LabelPair": 1, "LabelSlot": 1})
		if n := count(t, "SELECT count(*) FROM label_pair WHERE first_id = $1 AND second_id = $2", into.ToUUID(), other.ToUUID()); n != 1 {
			t.Errorf("the pair of into and other is held %d times, want once", n)
		}
		if n := count(t, "SELECT count(*) FROM label_pair WHERE second_id = $1", into.ToUUID()); n != 2 {
			t.Errorf("%d pairs end at into, want the two that ended at from", n)
		}
		if n := count(t, "SELECT count(*) FROM label_slot WHERE label_id = $1 AND slot IS NULL", into.ToUUID()); n != 1 {
			t.Errorf("into holds %d open slots with no name, want one", n)
		}
		if n := count(t, "SELECT count(*) FROM label_slot WHERE label_id = $1 AND slot = 'x'", into.ToUUID()); n != 2 {
			t.Errorf("into holds %d slots x, want its open one and the closed one of from", n)
		}
	})

	t.Run("a merge that stamps a row deleted needs an acting user before it writes, and one that stamps none does not", func(t *testing.T) {
		keep, drop := label(t, "keep"), label(t, "drop")
		doc := document(t, "unsigned")
		tag(t, doc, drop)
		if _, err := db.Label.MergeInto(background, drop, keep); !errors.Is(err, ErrNoUserInContext) {
			t.Errorf("a merge of a soft-deletable table with no user = %v, want ErrNoUserInContext", err)
		}
		if n := count(t, "SELECT count(*) FROM document_label WHERE document_id = $1 AND label_id = $2", doc.ToUUID(), drop.ToUUID()); n != 1 {
			t.Errorf("a merge refused for want of a user moved the document's label")
		}
		// A customer is deleted for good and nothing of its merge is
		// stamped, so no user is needed.
		one, two := customer(t, "One"), customer(t, "Two")
		exec(t, "INSERT INTO purchase (id, customer_id, total) VALUES (gen_random_uuid(), $1, 5)", two.ToUUID())
		result, err := db.Customer.MergeInto(background, two, one)
		if err != nil {
			t.Fatalf("merge customers with no user: %v", err)
		}
		want(t, result, map[string]int{"Purchase": 1}, map[string]int{})
	})

	t.Run("two merges of one pair at once: one folds, and the other finds its row gone", func(t *testing.T) {
		for round := 0; round < 5; round++ {
			left, right := label(t, fmt.Sprintf("left-%d", round)), label(t, fmt.Sprintf("right-%d", round))
			doc := document(t, fmt.Sprintf("raced-%d", round))
			tag(t, doc, left)
			var wg sync.WaitGroup
			errs := make([]error, 2)
			for i, pair := range [][2]types.IdentityUUID{{left, right}, {right, left}} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, errs[i] = db.Label.MergeInto(ctx, pair[0], pair[1])
				}()
			}
			wg.Wait()
			won, lost := 0, 0
			for _, err := range errs {
				switch {
				case err == nil:
					won++
				case errors.Is(err, ErrNotFound):
					lost++
				default:
					t.Fatalf("a merge raced against its reverse = %v", err)
				}
			}
			if won != 1 || lost != 1 {
				t.Fatalf("%d merges folded and %d found their row gone, want one of each", won, lost)
			}
			if n := count(t, "SELECT count(*) FROM label WHERE id IN ($1, $2) AND deleted_at IS NULL", left.ToUUID(), right.ToUUID()); n != 1 {
				t.Fatalf("%d of the two labels are live, want one", n)
			}
			if n := count(t, "SELECT count(*) FROM document_label d JOIN label l ON l.id = d.label_id WHERE d.document_id = $1 AND l.deleted_at IS NULL", doc.ToUUID()); n != 1 {
				t.Fatalf("the document carries %d live labels, want the one that stayed", n)
			}
		}
	})
}
`
