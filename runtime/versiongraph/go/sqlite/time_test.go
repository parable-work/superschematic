package sqlite_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/runtime/versiongraph/go/sqlite"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/storage"
)

// maxExact is 2^53 - 1, the widest time in microseconds either side of the
// epoch that the TypeScript adapter reads exactly.
const maxExact = 1<<53 - 1

// TestTimeRange: a transaction refuses a clock outside ±(2^53 - 1)
// microseconds, which the TypeScript adapter cannot read, before it writes
// anything, and takes one at either end; a read refuses a stored time
// outside that range, and one outside the years 0000-9999.
func TestTimeRange(t *testing.T) {
	ctx := context.Background()
	now := int64(1_800_000_000_000_000)
	s := newSetup(t, sqlite.Options{Clock: func() int64 { return now }}, "")
	ref := must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) {
		ref, _, err := refAndCommit(ctx, tx, bread, "main")
		return ref, err
	}))(t)
	for _, c := range []struct {
		now    int64
		refuse string
	}{
		{maxExact, ""},
		{-maxExact, ""},
		{maxExact + 1, "the clock returned 9007199254740992, not a whole number of microseconds a number holds exactly"},
		{-maxExact - 1, "the clock returned -9007199254740992, not a whole number of microseconds a number holds exactly"},
	} {
		now = c.now
		created, err := in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Commit, error) {
			return tx.InsertCommit(ctx, storage.NewCommit{Root: bread, Ref: ref.ID, SchemaEpoch: 1, ContentHash: strings.Repeat("0", 64), Actor: cook})
		})
		if c.refuse != "" {
			errorContains(t, err, c.refuse, fmt.Sprintf("a transaction at %d", c.now))
			continue
		}
		if err != nil {
			t.Fatalf("a transaction at %d: %v", c.now, err)
		}
		read := must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Commit, error) {
			return tx.ReadCommit(ctx, created.ID)
		}))(t)
		if read.CreatedAt != created.CreatedAt {
			t.Fatalf("a commit at %d reads at %s, written at %s", c.now, read.CreatedAt, created.CreatedAt)
		}
	}
	now = 1_800_000_000_000_000
	commits := count(t, s.db, `SELECT count(*) FROM "graph_commit"`)
	if commits != 3 {
		t.Fatalf("%d commits, want the first and the two at either end: a refused clock writes nothing", commits)
	}
	adapter := must(sqlite.New(readDescriptor(t), sqlite.Options{Graph: graph, Clock: func() int64 { return maxExact + 1 }}))(t)
	errorContains(t, adapter.CreateTables(ctx, s.client), "the clock returned 9007199254740992", "the layout's transaction at 2^53")

	// Stored times a writer other than an adapter put there.
	for _, c := range []struct {
		what, column string
		micros       int64
		read         func(ctx context.Context, tx storage.Tx, id string) error
		refuse       string
	}{
		{"a commit's time past 2^53 - 1", "created_at", maxExact + 1, readCommit, "9007199254740992 is not a whole number of microseconds a number holds exactly"},
		{"a commit's time before -(2^53 - 1)", "created_at", -maxExact - 1, readCommit, "-9007199254740992 is not a whole number of microseconds a number holds exactly"},
		{"a commit's time past the year 9999", "created_at", 300_000_000_000_000_000, readCommit, "300000000000000000 microseconds falls outside the years 0000-9999"},
		{"a commit's time before the year 0000", "created_at", -63_000_000_000_000_000, readCommit, "-63000000000000000 microseconds falls outside the years 0000-9999"},
		{"a ref's seal past 2^53 - 1", "sealed_at", maxExact + 1, readRef, "column sealed_at is 9007199254740992, not an integer a number holds exactly"},
		{"a ref's discard before -(2^53 - 1)", "deleted_at", -maxExact - 1, readRef, "column deleted_at is -9007199254740992, not an integer a number holds exactly"},
		{"a ref's seal at 2^53 - 1", "sealed_at", maxExact, readRef, ""},
	} {
		var id string
		switch c.column {
		case "created_at":
			commit := must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Commit, error) {
				return tx.InsertCommit(ctx, storage.NewCommit{Root: bread, Ref: ref.ID, SchemaEpoch: 1, ContentHash: strings.Repeat("0", 64), Actor: cook})
			}))(t)
			id = commit.ID
			if _, err := s.db.Exec(`UPDATE "graph_commit" SET created_at = ?2 WHERE id = ?1`, id, c.micros); err != nil {
				t.Fatal(err)
			}
		default:
			id = ref.ID
			if _, err := s.db.Exec(`UPDATE "graph_ref" SET sealed_at = NULL, deleted_at = NULL WHERE id = ?1`, id); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`UPDATE "graph_ref" SET `+c.column+` = ?2 WHERE id = ?1`, id, c.micros); err != nil {
				t.Fatal(err)
			}
		}
		err := s.storage.Transact(ctx, func(ctx context.Context, tx storage.Tx) error { return c.read(ctx, tx, id) })
		if c.refuse == "" {
			if err != nil {
				t.Fatalf("%s: %v", c.what, err)
			}
			continue
		}
		errorContains(t, err, c.refuse, c.what)
	}
}

func readCommit(ctx context.Context, tx storage.Tx, id string) error {
	_, err := tx.ReadCommit(ctx, id)
	return err
}

func readRef(ctx context.Context, tx storage.Tx, id string) error {
	_, err := tx.ReadRef(ctx, id)
	return err
}

// TestCreateTablesReadsTheClock: the layout's transaction reads the clock
// once, as the TypeScript adapter's createTables does, though it stores no
// time, so a stepped clock gives both adapters the same times after it.
func TestCreateTablesReadsTheClock(t *testing.T) {
	calls := 0
	adapter := must(sqlite.New(readDescriptor(t), sqlite.Options{Graph: graph, Clock: func() int64 { calls++; return 1_800_000_000_000_000 }}))(t)
	db := openDB(t, "", "")
	if err := adapter.CreateTables(context.Background(), sqlite.DB(db)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("the layout's transaction read the clock %d times, want once", calls)
	}
}
