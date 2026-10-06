package sqlite_test

import (
	"context"
	"encoding/json"
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

// TestUpdateKeepsTheSeal: an update of a sealed ref that does not seal it,
// a move of its head say, keeps its seal and the seal's time.
func TestUpdateKeepsTheSeal(t *testing.T) {
	now := int64(1_800_000_000_000_000)
	s := newSetup(t, sqlite.Options{Clock: func() int64 { now += 1_000_000; return now }}, "")
	var ref storage.Ref
	var commit string
	must(in(s.storage, func(ctx context.Context, tx storage.Tx) (struct{}, error) {
		var err error
		ref, commit, err = refAndCommit(ctx, tx, bread, "main")
		return struct{}{}, err
	}))(t)
	sealed := must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) {
		return tx.UpdateRef(ctx, storage.RefUpdate{ID: ref.ID, Version: ref.Version, Seal: true, Actor: cook})
	}))(t)
	moved := must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) {
		return tx.UpdateRef(ctx, storage.RefUpdate{ID: ref.ID, Version: sealed.Version, Head: commit, Actor: cook})
	}))(t)
	if !sealed.Sealed || !moved.Sealed {
		t.Fatalf("the ref is sealed %t, then %t after an update that does not seal it; want sealed both times", sealed.Sealed, moved.Sealed)
	}
	images := historyOf(t, s.db, `"graph_ref_history"`, ref.ID)
	if got, want := memberText(t, images[len(images)-1].data, "sealed_at"), memberText(t, images[len(images)-2].data, "sealed_at"); got != want || got == "null" {
		t.Fatalf("the update's image holds sealed_at %s, the seal's %s", got, want)
	}
}

// TestIntegerRange: a read refuses every stored integer the adapter reads
// back outside ±(2^53 - 1), as the TypeScript adapter's does: a ref's, a
// member's and a release pointer's _version, a commit's sequence and schema
// epoch, a patch's and a snapshot entry's entity_version, and a root's
// next sequence; and takes 2^53 - 1 itself.
func TestIntegerRange(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		what, update, read string
		value              int64
		refuse             string
	}{
		{"a ref's version", `UPDATE "graph_ref" SET _version = ?1`, "readRef", maxExact + 1, "column _version is 9007199254740992"},
		{"a ref's version at 2^53 - 1", `UPDATE "graph_ref" SET _version = ?1`, "readRef", maxExact, ""},
		{"a member's version", `UPDATE "graph_member" SET _version = ?1`, "rows", -maxExact - 1, "column _version is -9007199254740992"},
		{"a commit's sequence", `UPDATE "graph_commit" SET sequence = ?1`, "readCommit", maxExact + 1, "column sequence is 9007199254740992"},
		{"a commit's schema epoch", `UPDATE "graph_commit" SET schema_epoch = ?1`, "readCommit", maxExact + 1, "column schema_epoch is 9007199254740992"},
		{"a patch's entity version", `UPDATE "graph_patch" SET entity_version = ?1`, "patches", maxExact + 1, "column entity_version is 9007199254740992"},
		{"a snapshot entry's entity version", `UPDATE "graph_snapshot_entry" SET entity_version = ?1`, "snapshot", maxExact + 1, "column entity_version is 9007199254740992"},
		{"a release pointer's version", `UPDATE "graph_release" SET _version = ?1`, "readRelease", maxExact + 1, "column _version is 9007199254740992"},
		{"a root's next sequence", `UPDATE "graph_commit" SET sequence = ?1`, "nextSequence", maxExact, "column next is 9007199254740992"},
	} {
		t.Run(c.what, func(t *testing.T) {
			s := newSetup(t, sqlite.Options{}, "")
			var ref storage.Ref
			var commit string
			must(in(s.storage, func(ctx context.Context, tx storage.Tx) (struct{}, error) {
				var err error
				if ref, commit, err = refAndCommit(ctx, tx, bread, "main"); err != nil {
					return struct{}{}, err
				}
				row, err := tx.UpsertRow(ctx, "step", storage.RowWrite{Ref: ref.ID, Root: bread, Row: stepRow(t, "Mix", "Mix"), Actor: cook})
				if err != nil {
					return struct{}{}, err
				}
				var id string
				if err := json.Unmarshal([]byte(memberText(t, row, "id")), &id); err != nil {
					return struct{}{}, err
				}
				if err := tx.InsertPatches(ctx, commit, []storage.Patch{{Kind: "step", EntityKey: "Mix", EntityID: id, EntityVersion: 1, Operation: "ADD"}}); err != nil {
					return struct{}{}, err
				}
				if err := tx.InsertSnapshot(ctx, commit, []storage.SnapshotEntry{{Kind: "step", EntityKey: "Mix", EntityID: id, EntityVersion: 1}}); err != nil {
					return struct{}{}, err
				}
				_, err = tx.WriteRelease(ctx, storage.ReleaseWrite{Root: bread, Commit: commit, Actor: cook})
				return struct{}{}, err
			}))(t)
			if _, err := s.db.Exec(c.update, c.value); err != nil {
				t.Fatal(err)
			}
			err := s.storage.Transact(ctx, func(ctx context.Context, tx storage.Tx) error {
				var err error
				switch c.read {
				case "readRef":
					_, err = tx.ReadRef(ctx, ref.ID)
				case "rows":
					_, err = tx.Rows(ctx, "step", ref.ID)
				case "readCommit":
					_, err = tx.ReadCommit(ctx, commit)
				case "patches":
					_, err = tx.Patches(ctx, []string{commit})
				case "snapshot":
					_, err = tx.Snapshot(ctx, commit)
				case "readRelease":
					_, err = tx.ReadRelease(ctx, bread)
				case "nextSequence":
					_, err = tx.NextSequence(ctx, bread)
				}
				return err
			})
			if c.refuse == "" {
				if err != nil {
					t.Fatalf("%s at %d: %v", c.what, c.value, err)
				}
				return
			}
			errorContains(t, err, c.refuse+", not an integer a number holds exactly", c.what)
		})
	}
}
