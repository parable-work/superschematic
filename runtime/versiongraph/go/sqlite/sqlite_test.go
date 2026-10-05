package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	versiongraph "github.com/parable-work/superschematic/runtime/versiongraph/go"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/canonical"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/engine"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/sqlite"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/storage"
)

// TestRefVersionFences: a ref's version fences its update and its discard,
// and a refused discard leaves the transaction usable.
func TestRefVersionFences(t *testing.T) {
	s := newSetup(t, sqlite.Options{}, "")
	ref := must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) {
		return tx.CreateRef(ctx, storage.NewRef{Root: bread, Name: "main", Actor: cook})
	}))(t)
	if ref.Version != 1 {
		t.Fatalf("a new ref is at version %d, want 1", ref.Version)
	}
	moved := must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) {
		return tx.UpdateRef(ctx, storage.RefUpdate{ID: ref.ID, Version: 1, Actor: cook})
	}))(t)
	if moved.Version != 2 {
		t.Fatalf("UpdateRef moved the ref to version %d, want 2", moved.Version)
	}
	if _, err := in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) {
		return tx.UpdateRef(ctx, storage.RefUpdate{ID: ref.ID, Version: 1, Seal: true, Actor: cook})
	}); !errors.Is(err, storage.ErrVersionConflict) {
		t.Fatalf("UpdateRef at the stale version 1 = %v, want ErrVersionConflict", err)
	}
	if now := must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) { return tx.ReadRef(ctx, ref.ID) }))(t); now.Sealed {
		t.Fatal("the stale UpdateRef sealed the ref")
	}
	draft := must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) {
		if err := tx.DiscardRef(ctx, ref.ID, 1, cook); !errors.Is(err, storage.ErrVersionConflict) {
			t.Errorf("DiscardRef at the stale version 1 = %v, want ErrVersionConflict", err)
		}
		// The transaction goes on after the refused discard, and commits.
		return tx.CreateRef(ctx, storage.NewRef{Root: bread, Parent: ref.ID, Name: "draft", Actor: cook})
	}))(t)
	if read := must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) { return tx.ReadRef(ctx, draft.ID) }))(t); read.Name != "draft" {
		t.Fatalf("the draft written after a refused discard reads as %+v", read)
	}
	must(in(s.storage, func(ctx context.Context, tx storage.Tx) (struct{}, error) {
		return struct{}{}, tx.DiscardRef(ctx, ref.ID, 2, cook)
	}))(t)
	discarded := must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) { return tx.ReadRef(ctx, ref.ID) }))(t)
	if !discarded.Discarded || discarded.Version != 3 {
		t.Fatalf("the discarded ref reads as %+v, want discarded at version 3", discarded)
	}
	if _, err := in(s.storage, func(ctx context.Context, tx storage.Tx) (struct{}, error) {
		return struct{}{}, tx.DiscardRef(ctx, ref.ID, 3, cook)
	}); !errors.Is(err, storage.ErrVersionConflict) {
		t.Fatalf("DiscardRef of a discarded ref at its version = %v, want ErrVersionConflict", err)
	}
	if _, err := in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) { return tx.ReadRef(ctx, "Missing") }); !errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrVersionConflict) {
		t.Fatalf("ReadRef of a ref that does not exist = %v, want ErrNotFound", err)
	}
}

// TestReleaseVersionFences: a release pointer's version fences its first
// write and every move, and its history is the release log.
func TestReleaseVersionFences(t *testing.T) {
	s := newSetup(t, sqlite.Options{}, "")
	_, commit := func() (storage.Ref, string) {
		var ref storage.Ref
		var commit string
		must(in(s.storage, func(ctx context.Context, tx storage.Tx) (struct{}, error) {
			var err error
			ref, commit, err = refAndCommit(ctx, tx, bread, "main")
			return struct{}{}, err
		}))(t)
		return ref, commit
	}()
	write := func(version int64, actor string) (storage.Release, error) {
		return in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Release, error) {
			return tx.WriteRelease(ctx, storage.ReleaseWrite{Root: bread, Commit: commit, Version: version, Actor: actor})
		})
	}
	first := must(write(0, cook))(t)
	if first.Version != 1 {
		t.Fatalf("a first pointer is at version %d, want 1", first.Version)
	}
	for _, version := range []int64{0, 2} {
		if _, err := write(version, cook); !errors.Is(err, storage.ErrVersionConflict) {
			t.Fatalf("WriteRelease at version %d of a pointer at 1 = %v, want ErrVersionConflict", version, err)
		}
	}
	moved := must(write(1, "Baker"))(t)
	if moved.ID != first.ID || moved.Version != 2 {
		t.Fatalf("the move wrote %+v, want pointer %s at version 2", moved, first.ID)
	}
	if read := must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Release, error) { return tx.ReadRelease(ctx, bread) }))(t); read.Version != 2 {
		t.Fatalf("ReadRelease reads version %d, want 2", read.Version)
	}
	if _, err := in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Release, error) { return tx.ReadRelease(ctx, "Soup") }); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("ReadRelease of a root without one = %v, want ErrNotFound", err)
	}
	// The pointer's history is the release log: each write's image at its
	// version.
	var log []string
	for _, i := range historyOf(t, s.db, `"graph_release_history"`, first.ID) {
		log = append(log, fmt.Sprintf("%d %s %s", i.version, i.operation, memberText(t, i.data, "updated_by")))
	}
	if want := []string{`1 INSERT "Cook"`, `2 UPDATE "Baker"`}; !slices.Equal(log, want) {
		t.Fatalf("the release log is %q, want %q", log, want)
	}
}

// TestNameTaken: a root's live ref names are distinct, and a discarded
// ref's name is free again.
func TestNameTaken(t *testing.T) {
	s := newSetup(t, sqlite.Options{}, "")
	ctx := context.Background()
	main := must(s.engine.CreatePrimary(ctx, cook, bread, "main"))(t)
	if _, err := s.engine.CreatePrimary(ctx, cook, bread, "main"); !errors.Is(err, storage.ErrNameTaken) || engine.ErrorCode(err) != "name_taken" {
		t.Fatalf("a second primary line named main = %v, want name_taken", err)
	}
	draft := must(s.engine.Branch(ctx, cook, main.ID, "draft"))(t)
	if _, err := s.engine.Branch(ctx, cook, main.ID, "draft"); engine.ErrorCode(err) != "name_taken" {
		t.Fatalf("a second draft named draft = %v, want name_taken", err)
	}
	// Another root takes the name.
	must(s.engine.CreatePrimary(ctx, cook, "Soup", "main"))(t)
	if err := s.engine.Discard(ctx, cook, draft.ID, draft.Version); err != nil {
		t.Fatal(err)
	}
	if again := must(s.engine.Branch(ctx, cook, main.ID, "draft"))(t); again.Version != 1 {
		t.Fatalf("a draft of a discarded draft's name is at version %d, want 1", again.Version)
	}
}

// edits is one kind's upserts.
func upserts(kind string, rows ...json.RawMessage) engine.Edits {
	return engine.Edits{kind: {Upsert: rows}}
}

// TestHistory: a member's versions and images, a delete's actor, the
// columns history leaves out, and a ref's history.
func TestHistory(t *testing.T) {
	s := newSetup(t, sqlite.Options{}, "")
	ctx := context.Background()
	main := must(s.engine.CreatePrimary(ctx, cook, bread, "main"))(t)
	draft := must(s.engine.Branch(ctx, cook, main.ID, "draft"))(t)
	first := must(s.engine.Save(ctx, cook, draft.ID, draft.Version, upserts("step", stepRow(t, "Mix", "Mix", map[string]any{"scratch": "note to self"}))))(t)
	second := must(s.engine.Save(ctx, "Baker", draft.ID, first.Ref.Version, upserts("step", stepRow(t, "Mix", "Mix well"))))(t)
	row := second.Saved["step"][0]
	if v := memberText(t, row, "_version"); v != "2" {
		t.Fatalf("the updated row is at version %s, want 2", v)
	}
	// A column the update leaves out keeps its value on the live row.
	if v := memberText(t, row, "scratch"); v != `"note to self"` {
		t.Fatalf("the update left scratch %s, want it kept", v)
	}
	var id string
	if err := json.Unmarshal([]byte(memberText(t, row, "id")), &id); err != nil {
		t.Fatal(err)
	}
	must(s.engine.Save(ctx, "Janitor", draft.ID, second.Ref.Version, engine.Edits{"step": {Unset: []string{"Mix"}}}))(t)
	images := historyOf(t, s.db, `"graph_member_history"`, id)
	var ops []string
	for _, i := range images {
		ops = append(ops, fmt.Sprintf("%d %s", i.version, i.operation))
	}
	if want := []string{"1 INSERT", "2 UPDATE", "3 DELETE"}; !slices.Equal(ops, want) {
		t.Fatalf("the row's history is %q, want %q", ops, want)
	}
	for _, i := range images {
		assertCanonicalRow(t, "step", i.data)
		if _, ok := member(t, i.data, "scratch"); ok {
			t.Fatalf("image %d keeps scratch, which history leaves out: %s", i.version, i.data)
		}
		if v := memberText(t, i.data, "_version"); v != fmt.Sprint(i.version) {
			t.Fatalf("image %d holds _version %s", i.version, v)
		}
	}
	// An update's image is the row as stored, less scratch.
	edited := func(edit func(m map[string]json.RawMessage)) string {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(row, &m); err != nil {
			t.Fatal(err)
		}
		edit(m)
		out, err := canonical.Row(columns(t, "step"), mustJSON(t, m))
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if want := edited(func(m map[string]json.RawMessage) { delete(m, "scratch") }); images[1].data != want {
		t.Fatalf("the update's image is\n%s\nwant the row less scratch\n%s", images[1].data, want)
	}
	// The delete's image is the row at its version plus 1, naming the
	// delete's actor in the kind's actor column, updated_by.
	if want := edited(func(m map[string]json.RawMessage) {
		delete(m, "scratch")
		m["_version"] = json.RawMessage("3")
		m["updated_by"] = json.RawMessage(`"Janitor"`)
	}); images[2].data != want {
		t.Fatalf("the delete's image is\n%s\nwant\n%s", images[2].data, want)
	}
	if rows := must(in(s.storage, func(ctx context.Context, tx storage.Tx) ([]json.RawMessage, error) {
		return tx.Rows(ctx, "step", draft.ID)
	}))(t); len(rows) != 0 {
		t.Fatalf("the unset left rows %s", rows)
	}
	// A kind with no actor column keeps the row's values in its delete's
	// image.
	readRef := func(id string) storage.Ref {
		return must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) { return tx.ReadRef(ctx, id) }))(t)
	}
	whisk := must(s.engine.Save(ctx, cook, draft.ID, readRef(draft.ID).Version, upserts("utensil", json.RawMessage(`{"entity_key": "Whisk", "name": "whisk"}`))))(t)
	utensil := whisk.Saved["utensil"][0]
	must(s.engine.Save(ctx, "Janitor", draft.ID, whisk.Ref.Version, engine.Edits{"utensil": {Unset: []string{"Whisk"}}}))(t)
	var utensilID string
	if err := json.Unmarshal([]byte(memberText(t, utensil, "id")), &utensilID); err != nil {
		t.Fatal(err)
	}
	gone := historyOf(t, s.db, `"graph_member_history"`, utensilID)
	var kept map[string]json.RawMessage
	if err := json.Unmarshal(utensil, &kept); err != nil {
		t.Fatal(err)
	}
	kept["_version"] = json.RawMessage("2")
	if want := string(must(canonical.Row(columns(t, "utensil"), mustJSON(t, kept)))(t)); len(gone) != 2 || gone[1].data != want {
		t.Fatalf("the utensil's delete image is %+v, want\n%s", gone, want)
	}
	// A ref's history: its insert and each update at its version, the
	// discard's naming its actor.
	if err := s.engine.Discard(ctx, "Janitor", draft.ID, readRef(draft.ID).Version); err != nil {
		t.Fatal(err)
	}
	refImages := historyOf(t, s.db, `"graph_ref_history"`, draft.ID)
	if len(refImages) != 7 {
		t.Fatalf("the draft has %d history images, want 7", len(refImages))
	}
	for i, image := range refImages {
		want := "UPDATE"
		if i == 0 {
			want = "INSERT"
		}
		if image.version != int64(i+1) || image.operation != want {
			t.Fatalf("the draft's image %d is version %d %s, want version %d %s", i, image.version, image.operation, i+1, want)
		}
	}
	last := refImages[6].data
	if v := memberText(t, last, "deleted_by"); v != `"Janitor"` {
		t.Fatalf("the discard's image names deleted_by %s", v)
	}
	if v := memberText(t, last, "_version"); v != "7" {
		t.Fatalf("the discard's image holds _version %s", v)
	}
	if v := memberText(t, last, "deleted_at"); !regexp.MustCompile(`^"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+Z"$`).MatchString(v) {
		t.Fatalf("the discard's image holds deleted_at %s, want a canonical date-time", v)
	}
}

// TestPrune: prune deletes a kind's images past its retention but each
// row's newest and every pinned one, at most a batch, and nothing of a kind
// without retention.
func TestPrune(t *testing.T) {
	now := int64(1_800_000_000_000_000)
	s := newSetup(t, sqlite.Options{Clock: func() int64 { return now }}, "")
	ctx := context.Background()
	main := must(s.engine.CreatePrimary(ctx, cook, bread, "main"))(t)
	draft := must(s.engine.Branch(ctx, cook, main.ID, "draft"))(t)
	for _, instruction := range []string{"Knead", "Knead well", "Knead hard"} {
		draft = must(s.engine.Save(ctx, cook, draft.ID, draft.Version, engine.Edits{
			"step":    {Upsert: []json.RawMessage{stepRow(t, "Knead", instruction)}},
			"utensil": {Upsert: []json.RawMessage{mustJSON(t, map[string]any{"entity_key": "Whisk", "name": instruction})}},
		}))(t).Ref
	}
	// The commit pins version 3 of each; versions 1 and 2 are unpinned.
	draft = must(s.engine.Commit(ctx, cook, draft.ID, draft.Version, engine.CommitOptions{}))(t).Ref
	must(s.engine.Save(ctx, cook, draft.ID, draft.Version, upserts("step", stepRow(t, "Knead", "Knead softly"))))(t)
	prune := func(kind string, days, batch int) (int64, error) {
		return in(s.storage, func(ctx context.Context, tx storage.Tx) (int64, error) { return tx.Prune(ctx, kind, days, batch) })
	}
	expect := func(kind string, days, batch int, want int64) {
		t.Helper()
		if got := must(prune(kind, days, batch))(t); got != want {
			t.Fatalf("prune(%s, %d, %d) deleted %d images, want %d", kind, days, batch, got, want)
		}
	}
	versions := func(kind string) []int64 {
		return queryInts(t, s.db, `SELECT _version FROM "graph_member_history" WHERE kind = ?1 ORDER BY _version`, kind)
	}
	// Within the step kind's 365 days, nothing goes.
	now += 364 * 86_400_000_000
	expect("step", 0, 0, 0)
	// An argument other than 0 is the retention, in days.
	expect("step", 400, 0, 0)
	now += 2 * 86_400_000_000
	expect("step", 400, 0, 0)
	// Past the declared 365 days: versions 1 and 2, a batch at a time.
	expect("step", 0, 1, 1)
	if got := versions("step"); !slices.Equal(got, []int64{2, 3, 4}) {
		t.Fatalf("after a batch of 1 step history holds %v", got)
	}
	expect("step", 0, 0, 1)
	if got := versions("step"); !slices.Equal(got, []int64{3, 4}) {
		t.Fatalf("step history holds %v, want the pinned version 3 and the newest, version 4", got)
	}
	expect("step", 1, 0, 0)
	// utensil declares no retention, so nothing of it goes, whatever the
	// argument.
	expect("utensil", 0, 0, 0)
	expect("utensil", 1, 0, 0)
	if got := versions("utensil"); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("utensil history holds %v", got)
	}
	_, err := prune("step", 0, -1)
	errorContains(t, err, "a batch is a whole number", "prune with a batch of -1")
}

// TestPruneBoundary: an image exactly its retention old stays and one a
// microsecond older goes, and the oldest goes first.
func TestPruneBoundary(t *testing.T) {
	start := int64(1_800_000_000_000_000)
	now := start
	s := newSetup(t, sqlite.Options{Clock: func() int64 { return now }}, "")
	ctx := context.Background()
	main := must(s.engine.CreatePrimary(ctx, cook, bread, "main"))(t)
	draft := must(s.engine.Branch(ctx, cook, main.ID, "draft"))(t)
	for _, instruction := range []string{"Mix", "Mix well"} {
		draft = must(s.engine.Save(ctx, cook, draft.ID, draft.Version,
			upserts("step", stepRow(t, "Mix", instruction), stepRow(t, "Rest", instruction), stepRow(t, "Bake", instruction))))(t).Ref
	}
	prune := func(batch int) int64 {
		return must(in(s.storage, func(ctx context.Context, tx storage.Tx) (int64, error) { return tx.Prune(ctx, "step", 0, batch) }))(t)
	}
	left := func() []string {
		rows, err := s.db.Query(`SELECT id FROM "graph_member_history" WHERE kind = 'step' AND _version = 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return ids
	}
	// Each row's version 1 is prunable once it is past 365 days old, and
	// not at exactly 365 days.
	now = start + 365*86_400_000_000
	if n := prune(0); n != 0 {
		t.Fatalf("pruned %d images exactly 365 days old, want none", n)
	}
	// Make the row with the greatest id the oldest by a microsecond more
	// than the next, so oldest first and id order disagree.
	ids := left()
	for i, by := range map[int]int{2: 2, 1: 1} {
		if _, err := s.db.Exec(`UPDATE "graph_member_history" SET recorded_at = recorded_at - ?2 WHERE id = ?1 AND _version = 1`, ids[i], by); err != nil {
			t.Fatal(err)
		}
	}
	if n := prune(1); n != 1 {
		t.Fatalf("a batch of 1 pruned %d", n)
	}
	if got := left(); !slices.Equal(got, ids[:2]) {
		t.Fatalf("after the first batch %q are left, want %q: the oldest goes first", got, ids[:2])
	}
	if n := prune(0); n != 1 {
		t.Fatalf("pruned %d, want the one a microsecond past 365 days", n)
	}
	if got := left(); !slices.Equal(got, ids[:1]) {
		t.Fatalf("%q are left, want %q: an image exactly 365 days old stays", got, ids[:1])
	}
	now++
	if n := prune(0); n != 1 || len(left()) != 0 {
		t.Fatalf("a microsecond on, pruned %d and left %q", n, left())
	}
}

// TestUpsertRow: the adapter writes a row's ref, root, tombstone, actor and
// time, never its id or version, and keeps or defaults what it lacks.
func TestUpsertRow(t *testing.T) {
	s := newSetup(t, sqlite.Options{Clock: func() int64 { return 1_800_000_000_000_000 }}, "")
	ref := must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) {
		ref, _, err := refAndCommit(ctx, tx, bread, "main")
		return ref, err
	}))(t)
	write := func(row json.RawMessage, tombstone bool, actor string) (json.RawMessage, error) {
		return in(s.storage, func(ctx context.Context, tx storage.Tx) (json.RawMessage, error) {
			return tx.UpsertRow(ctx, "step", storage.RowWrite{Ref: ref.ID, Root: bread, Row: row, Tombstone: tombstone, Actor: actor})
		})
	}
	expect := func(row json.RawMessage, column, want string) {
		t.Helper()
		if got, _ := member(t, row, column); got != want {
			t.Fatalf("%s is %s, want %s: %s", column, got, want, row)
		}
	}
	inserted := must(write(mustJSON(t, map[string]any{
		"entity_key": "Mix", "id": "Elsewhere", "_version": 7, "ref_id": "Other", "recipe_id": "Soup", "deleted_on_ref": true,
		"created_at": "2000-01-01T00:00:00Z", "created_by": "Somebody", "position": 1, "instruction": "Mix", "timings": map[string]any{},
	}), false, cook))(t)
	assertCanonicalRow(t, "step", inserted)
	if memberText(t, inserted, "id") == `"Elsewhere"` {
		t.Fatal("the adapter wrote the row's id")
	}
	expect(inserted, "_version", "1")
	expect(inserted, "ref_id", string(mustJSON(t, ref.ID)))
	expect(inserted, "recipe_id", `"Bread"`)
	expect(inserted, "deleted_on_ref", "false")
	expect(inserted, "created_at", `"2027-01-15T08:00:00Z"`)
	expect(inserted, "created_by", `"Cook"`)
	expect(inserted, "updated_by", `"Cook"`)
	// A column the insert lacks holds null.
	expect(inserted, "scratch", "null")
	updated := must(write(mustJSON(t, map[string]any{"entity_key": "Mix", "instruction": "Stir", "created_by": "Somebody"}), true, "Baker"))(t)
	assertCanonicalRow(t, "step", updated)
	read := must(in(s.storage, func(ctx context.Context, tx storage.Tx) ([]json.RawMessage, error) {
		return tx.Rows(ctx, "step", ref.ID)
	}))(t)
	if len(read) != 1 || string(read[0]) != string(updated) {
		t.Fatalf("Rows reads %s, want the row as written, %s", read, updated)
	}
	expect(updated, "id", memberText(t, inserted, "id"))
	expect(updated, "_version", "2")
	expect(updated, "deleted_on_ref", "true")
	// A column the update lacks keeps its value, and the creation audit
	// stays.
	expect(updated, "position", "1")
	expect(updated, "created_by", `"Cook"`)
	expect(updated, "updated_by", `"Baker"`)
	// A row without an entity key is a new entity.
	fresh := must(write(stepRow(t, nil, "Rest"), false, cook))(t)
	if memberText(t, fresh, "entity_key") == memberText(t, inserted, "entity_key") {
		t.Fatal("a row without an entity key took another row's")
	}
	var key string
	if err := json.Unmarshal([]byte(memberText(t, fresh, "entity_key")), &key); err != nil {
		t.Fatal(err)
	}
	assertCanonicalID(t, key, "a generated entity key")
	// A column the descriptor does not declare is refused.
	_, err := write(json.RawMessage(`{"entity_key": "Mix", "flavour": "salt"}`), false, cook)
	errorContains(t, err, "does not declare", "a row with an undeclared column")
}

// TestGainedColumn: after a kind gains a column, its old rows read it as
// null, as Postgres's ADD COLUMN gives them, and its old images read as
// stored.
func TestGainedColumn(t *testing.T) {
	s := newSetup(t, sqlite.Options{}, "")
	ctx := context.Background()
	main := must(s.engine.CreatePrimary(ctx, cook, bread, "main"))(t)
	draft := must(s.engine.Branch(ctx, cook, main.ID, "draft"))(t)
	saved := must(s.engine.Save(ctx, cook, draft.ID, draft.Version, engine.Edits{
		"step":    {Upsert: []json.RawMessage{stepRow(t, "Mix", "Mix")}},
		"utensil": {Upsert: []json.RawMessage{json.RawMessage(`{"entity_key": "Whisk", "name": "whisk"}`)}},
	}))(t)
	committed := must(s.engine.Commit(ctx, cook, draft.ID, saved.Ref.Version, engine.CommitOptions{}))(t)
	// The schema's next version: utensil gains color, and step gains memo,
	// which its history leaves out.
	d := descriptorDoc(t)
	kindOf(t, d, "utensil")["columns"].(map[string]any)["color"] = "string"
	step := kindOf(t, d, "step")
	step["columns"].(map[string]any)["memo"] = "string"
	step["excluded"] = append(step["excluded"].([]any), "memo")
	history := step["history"].(map[string]any)
	history["exclude"] = append(history["exclude"].([]any), "memo")
	next := mustJSON(t, d)
	adapter := must(sqlite.New(next, sqlite.Options{Graph: graph}))(t)
	st := must(adapter.Storage(ctx, s.client))(t)
	eng := must(engine.New(next, st, engine.Options{SchemaEpoch: 1, SnapshotEvery: 3}))(t)
	rows := func(kind string) []json.RawMessage {
		return must(in(st, func(ctx context.Context, tx storage.Tx) ([]json.RawMessage, error) {
			return tx.Rows(ctx, kind, draft.ID)
		}))(t)
	}
	whisk, mix := rows("utensil")[0], rows("step")[0]
	if v := memberText(t, whisk, "color"); v != "null" {
		t.Fatalf("the old utensil reads color %s, want null", v)
	}
	if v := memberText(t, mix, "memo"); v != "null" {
		t.Fatalf("the old step reads memo %s, want null", v)
	}
	utensilColumns := map[string]string{}
	for column, class := range kindOf(t, d, "utensil")["columns"].(map[string]any) {
		utensilColumns[column] = class.(string)
	}
	if out := must(canonical.Row(utensilColumns, whisk))(t); string(out) != string(whisk) {
		t.Fatalf("the old utensil is not canonical: %s", whisk)
	}
	// An image reads as it was stored, without the gained column, as a
	// Postgres history image does.
	idOf := func(row json.RawMessage) string {
		var id string
		if err := json.Unmarshal([]byte(memberText(t, row, "id")), &id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	imageOf := func(kind string, row json.RawMessage, version int64) json.RawMessage {
		images := must(in(st, func(ctx context.Context, tx storage.Tx) ([]json.RawMessage, error) {
			return tx.Images(ctx, kind, []storage.Pin{{ID: idOf(row), Version: version}})
		}))(t)
		if len(images) != 1 {
			t.Fatalf("%d images of %s version %d", len(images), kind, version)
		}
		return images[0]
	}
	whiskImage, mixImage := imageOf("utensil", whisk, 1), imageOf("step", mix, 1)
	var stored string
	if err := s.db.QueryRow(`SELECT data FROM "graph_member_history" WHERE id = ?1 AND _version = 1`, idOf(whisk)).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(whiskImage) != stored {
		t.Fatalf("the image reads as\n%s\nwhere it is stored as\n%s", whiskImage, stored)
	}
	for _, c := range []struct {
		image  json.RawMessage
		column string
	}{{whiskImage, "color"}, {mixImage, "memo"}, {mixImage, "scratch"}} {
		if _, ok := member(t, c.image, c.column); ok {
			t.Fatalf("the old image has %s: %s", c.column, c.image)
		}
	}
	// The commit's tree lacks color, and the core reads it as null: it
	// hashes as the draft's live rows, which hold it null, and as a tree
	// whose row holds it null.
	tree := must(eng.Materialize(ctx, committed.Commit.ID))(t)
	read := tree.Tree["utensil"][0]
	if _, ok := member(t, read, "color"); ok {
		t.Fatalf("the materialized row has color: %s", read)
	}
	if composed := must(eng.Compose(ctx, draft.ID))(t); tree.ContentHash != composed.ContentHash {
		t.Fatalf("the commit hashes %s and the draft composes to %s", tree.ContentHash, composed.ContentHash)
	}
	var withColor map[string]json.RawMessage
	if err := json.Unmarshal(read, &withColor); err != nil {
		t.Fatal(err)
	}
	withColor["color"] = json.RawMessage("null")
	hashed := must(versiongraph.ContentHash(versiongraph.TreeRequest{Descriptor: next, Tree: mustJSON(t, map[string]any{
		"step": tree.Tree["step"], "utensil": []any{withColor},
	})}))(t)
	if tree.ContentHash != hashed.ContentHash {
		t.Fatalf("the commit hashes %s, and its tree with color null %s", tree.ContentHash, hashed.ContentHash)
	}
	// An update of the old row stores the gained column, null, and its
	// image carries it.
	updated := must(eng.Save(ctx, cook, draft.ID, committed.Ref.Version, upserts("utensil", json.RawMessage(`{"entity_key": "Whisk", "name": "big whisk"}`))))(t)
	if v := memberText(t, updated.Saved["utensil"][0], "color"); v != "null" {
		t.Fatalf("the update returned color %s", v)
	}
	var data string
	if err := s.db.QueryRow(`SELECT data FROM "graph_member" WHERE id = ?1`, idOf(whisk)).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if v := memberText(t, data, "color"); v != "null" {
		t.Fatalf("the updated row stores color %s, want null", v)
	}
	updatedImage := imageOf("utensil", whisk, 2)
	if memberText(t, updatedImage, "color") != "null" || memberText(t, updatedImage, "name") != `"big whisk"` {
		t.Fatalf("the update's image is %s", updatedImage)
	}
}

// TestUniqueIndexes: a unique index backs each key the adapter's own reads
// keep unique, and refuses a second row with SQLITE_CONSTRAINT_UNIQUE.
func TestUniqueIndexes(t *testing.T) {
	s := newSetup(t, sqlite.Options{}, "")
	var ref storage.Ref
	var commit string
	must(in(s.storage, func(ctx context.Context, tx storage.Tx) (struct{}, error) {
		var err error
		ref, commit, err = refAndCommit(ctx, tx, bread, "main")
		return struct{}{}, err
	}))(t)
	n := 0
	fresh := func() string { n++; return "Row" + string(rune('A'+n)) }
	for _, c := range []struct {
		what   string
		insert func() (string, []any)
	}{
		{"a commit's entity in its patches", func() (string, []any) {
			return `INSERT INTO "graph_patch" (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version, operation) VALUES (?1, 'recipe', ?2, 'step', 'Mix', 'B', 1, 'ADD')`, []any{fresh(), commit}
		}},
		{"a commit's entity in its snapshot", func() (string, []any) {
			return `INSERT INTO "graph_snapshot_entry" (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version) VALUES (?1, 'recipe', ?2, 'step', 'Mix', 'B', 1)`, []any{fresh(), commit}
		}},
		{"an entity of a kind on a ref", func() (string, []any) {
			return `INSERT INTO "graph_member" (id, graph, kind, entity_key, ref_id, root_id, tombstone, _version, data) VALUES (?1, 'recipe', 'step', 'Mix', ?2, 'Bread', 0, 1, '{}')`, []any{fresh(), ref.ID}
		}},
		{"a member's image at a version", func() (string, []any) {
			return `INSERT INTO "graph_member_history" (history_id, graph, kind, id, _version, operation, data, recorded_at) VALUES (?1, 'recipe', 'step', 'Same', 1, 'INSERT', '{}', 0)`, []any{fresh()}
		}},
		{"a ref's image at a version", func() (string, []any) {
			return `INSERT INTO "graph_ref_history" (history_id, graph, id, _version, operation, data, recorded_at) VALUES (?1, 'recipe', 'Same', 1, 'INSERT', '{}', 0)`, []any{fresh()}
		}},
		{"a pointer's image at a version", func() (string, []any) {
			return `INSERT INTO "graph_release_history" (history_id, graph, id, _version, operation, data, recorded_at) VALUES (?1, 'recipe', 'Same', 1, 'INSERT', '{}', 0)`, []any{fresh()}
		}},
		{"a root's release pointer", func() (string, []any) {
			return `INSERT INTO "graph_release" (id, graph, root_id, commit_id, created_at, created_by, updated_at, updated_by, _version) VALUES (?1, 'recipe', 'Soup', ?2, 0, 'Cook', 0, 'Cook', 1)`, []any{fresh(), commit}
		}},
		{"a root's sequence", func() (string, []any) {
			return `INSERT INTO "graph_commit" (id, graph, root_id, ref_id, schema_epoch, content_hash, sequence, created_at, created_by) VALUES (?1, 'recipe', 'Bread', ?2, 1, 'h', 7, 0, 'Cook')`, []any{fresh(), ref.ID}
		}},
		{"a root's live ref name", func() (string, []any) {
			return `INSERT INTO "graph_ref" (id, graph, root_id, name, created_at, created_by, updated_at, updated_by, _version) VALUES (?1, 'recipe', 'Soup', 'main', 0, 'Cook', 0, 'Cook', 1)`, []any{fresh()}
		}},
	} {
		statement, args := c.insert()
		if _, err := s.db.Exec(statement, args...); err != nil {
			t.Fatalf("%s: the first row: %v", c.what, err)
		}
		statement, args = c.insert()
		_, err := s.db.Exec(statement, args...)
		if code, ok := sqlite.ResultCode(err); !ok || code != sqlite.ResultConstraintUnique {
			t.Fatalf("%s: the second row = %v (code %d), want SQLITE_CONSTRAINT_UNIQUE", c.what, err, code)
		}
	}
}

// TestStrict: every table is STRICT, so a value of the wrong type is
// refused, not stored.
func TestStrict(t *testing.T) {
	s := newSetup(t, sqlite.Options{}, "")
	_, err := s.db.Exec(`INSERT INTO "graph_ref" (id, graph, root_id, name, created_at, created_by, updated_at, updated_by, _version) ` +
		`VALUES ('A', 'recipe', 'Bread', 'main', 'today', 'Cook', 0, 'Cook', 1)`)
	// SQLITE_CONSTRAINT_DATATYPE.
	if code, ok := sqlite.ResultCode(err); !ok || code != 3091 {
		t.Fatalf("text in an INTEGER column = %v (code %d), want SQLITE_CONSTRAINT_DATATYPE", err, code)
	}
	for _, table := range sqlite.Tables() {
		var statement string
		if err := s.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?1`, sqlite.DefaultTableName(table)).Scan(&statement); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if !strings.HasSuffix(statement, ") STRICT") {
			t.Fatalf("%s is not STRICT: %s", table, statement)
		}
	}
}

// TestForeignKeys: foreign keys check each of the layout's edges on a
// connection the adapter binds.
func TestForeignKeys(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, "", "")
	conn := must(db.Conn(ctx))(t)
	t.Cleanup(func() { _ = conn.Close() })
	s := setupOn(t, db, sqlite.DBConn(conn), sqlite.Options{}, readDescriptor(t))
	var ref storage.Ref
	var commit string
	must(in(s.storage, func(ctx context.Context, tx storage.Tx) (struct{}, error) {
		var err error
		ref, commit, err = refAndCommit(ctx, tx, bread, "main")
		return struct{}{}, err
	}))(t)
	n := 0
	fresh := func() string { n++; return "Row" + strings.Repeat("x", n) }
	// Each edge as an insert of a row that names the target, which holds a
	// valid row of every other column.
	for _, edge := range []struct {
		what, target string
		insert       func(target string) (string, []any)
	}{
		{"a ref's parent", ref.ID, func(target string) (string, []any) {
			return `INSERT INTO "graph_ref" (id, graph, root_id, parent_ref_id, name, created_at, created_by, updated_at, updated_by, _version) VALUES (?1, 'recipe', 'Bread', ?2, ?3, 0, 'Cook', 0, 'Cook', 1)`, []any{fresh(), target, fresh()}
		}},
		{"a ref's base", commit, func(target string) (string, []any) {
			return `INSERT INTO "graph_ref" (id, graph, root_id, base_commit_id, name, created_at, created_by, updated_at, updated_by, _version) VALUES (?1, 'recipe', 'Bread', ?2, ?3, 0, 'Cook', 0, 'Cook', 1)`, []any{fresh(), target, fresh()}
		}},
		{"a ref's head", commit, func(target string) (string, []any) {
			return `INSERT INTO "graph_ref" (id, graph, root_id, head_commit_id, name, created_at, created_by, updated_at, updated_by, _version) VALUES (?1, 'recipe', 'Bread', ?2, ?3, 0, 'Cook', 0, 'Cook', 1)`, []any{fresh(), target, fresh()}
		}},
		{"a commit's ref", ref.ID, func(target string) (string, []any) {
			return `INSERT INTO "graph_commit" (id, graph, root_id, ref_id, schema_epoch, content_hash, created_at, created_by) VALUES (?1, 'recipe', 'Bread', ?2, 1, 'h', 0, 'Cook')`, []any{fresh(), target}
		}},
		{"a commit's parent", commit, func(target string) (string, []any) {
			return `INSERT INTO "graph_commit" (id, graph, root_id, ref_id, parent_commit_id, schema_epoch, content_hash, created_at, created_by) VALUES (?1, 'recipe', 'Bread', ?2, ?3, 1, 'h', 0, 'Cook')`, []any{fresh(), ref.ID, target}
		}},
		{"a patch's commit", commit, func(target string) (string, []any) {
			return `INSERT INTO "graph_patch" (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version, operation) VALUES (?1, 'recipe', ?2, 'step', ?3, 'B', 1, 'ADD')`, []any{fresh(), target, fresh()}
		}},
		{"a snapshot entry's commit", commit, func(target string) (string, []any) {
			return `INSERT INTO "graph_snapshot_entry" (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version) VALUES (?1, 'recipe', ?2, 'step', ?3, 'B', 1)`, []any{fresh(), target, fresh()}
		}},
		{"a release pointer's commit", commit, func(target string) (string, []any) {
			return `INSERT INTO "graph_release" (id, graph, root_id, commit_id, created_at, created_by, updated_at, updated_by, _version) VALUES (?1, 'recipe', ?3, ?2, 0, 'Cook', 0, 'Cook', 1)`, []any{fresh(), target, fresh()}
		}},
		{"a member's ref", ref.ID, func(target string) (string, []any) {
			return `INSERT INTO "graph_member" (id, graph, kind, entity_key, ref_id, root_id, tombstone, _version, data) VALUES (?1, 'recipe', 'step', ?3, ?2, 'Bread', 0, 1, '{}')`, []any{fresh(), target, fresh()}
		}},
	} {
		statement, args := edge.insert(edge.target)
		if _, err := conn.ExecContext(ctx, statement, args...); err != nil {
			t.Fatalf("%s: a row that names its target: %v", edge.what, err)
		}
		statement, args = edge.insert("Missing")
		_, err := conn.ExecContext(ctx, statement, args...)
		// SQLITE_CONSTRAINT_FOREIGNKEY.
		if code, ok := sqlite.ResultCode(err); !ok || code != 787 {
			t.Fatalf("%s: a row that names a missing one = %v (code %d), want SQLITE_CONSTRAINT_FOREIGNKEY", edge.what, err, code)
		}
	}
}

// TestGraphAndRootChecks: the adapter refuses a write whose ref or commit
// is another graph's or another root's, and takes the graph's own.
func TestGraphAndRootChecks(t *testing.T) {
	s := newSetup(t, sqlite.Options{}, "")
	ctx := context.Background()
	other := must(must(sqlite.New(readDescriptor(t), sqlite.Options{Graph: "menu"}))(t).Storage(ctx, s.client))(t)
	type pair struct {
		ref    storage.Ref
		commit string
	}
	pairOf := func(st storage.Storage, root string) pair {
		var p pair
		must(in(st, func(ctx context.Context, tx storage.Tx) (struct{}, error) {
			var err error
			p.ref, p.commit, err = refAndCommit(ctx, tx, root, "main")
			return struct{}{}, err
		}))(t)
		return p
	}
	mine, soup, theirs := pairOf(s.storage, bread), pairOf(s.storage, "Soup"), pairOf(other, bread)
	refuses := func(what, want string, fn func(ctx context.Context, tx storage.Tx) error) {
		t.Helper()
		_, err := in(s.storage, func(ctx context.Context, tx storage.Tx) (struct{}, error) { return struct{}{}, fn(ctx, tx) })
		errorContains(t, err, want, what)
	}
	newCommit := func(ref, parent string) storage.NewCommit {
		return storage.NewCommit{Root: bread, Ref: ref, Parent: parent, SchemaEpoch: 1, ContentHash: strings.Repeat("0", 64), Actor: cook}
	}
	patch := storage.Patch{Kind: "step", EntityKey: "Mix", EntityID: "Row", EntityVersion: 1, Operation: "ADD"}
	entry := storage.SnapshotEntry{Kind: "step", EntityKey: "Mix", EntityID: "Row", EntityVersion: 1}
	for _, c := range []struct {
		whose string
		p     pair
	}{{"another graph's", theirs}, {"another root's", soup}} {
		refuses("CreateRef with "+c.whose+" parent", "is not a ref of root", func(ctx context.Context, tx storage.Tx) error {
			_, err := tx.CreateRef(ctx, storage.NewRef{Root: bread, Parent: c.p.ref.ID, Name: "x", Actor: cook})
			return err
		})
		refuses("CreateRef with "+c.whose+" base", "is not a commit of root", func(ctx context.Context, tx storage.Tx) error {
			_, err := tx.CreateRef(ctx, storage.NewRef{Root: bread, Base: c.p.commit, Name: "x", Actor: cook})
			return err
		})
		refuses("UpdateRef to "+c.whose+" head", "is not a commit of root", func(ctx context.Context, tx storage.Tx) error {
			_, err := tx.UpdateRef(ctx, storage.RefUpdate{ID: mine.ref.ID, Version: 1, Head: c.p.commit, Actor: cook})
			return err
		})
		refuses("UpdateRef to "+c.whose+" base", "is not a commit of root", func(ctx context.Context, tx storage.Tx) error {
			_, err := tx.UpdateRef(ctx, storage.RefUpdate{ID: mine.ref.ID, Version: 1, Base: c.p.commit, Actor: cook})
			return err
		})
		refuses("InsertCommit on "+c.whose+" ref", "is not a ref of root", func(ctx context.Context, tx storage.Tx) error {
			_, err := tx.InsertCommit(ctx, newCommit(c.p.ref.ID, ""))
			return err
		})
		refuses("InsertCommit after "+c.whose+" commit", "is not a commit of root", func(ctx context.Context, tx storage.Tx) error {
			_, err := tx.InsertCommit(ctx, newCommit(mine.ref.ID, c.p.commit))
			return err
		})
		refuses("WriteRelease of "+c.whose+" commit", "is not a commit of root", func(ctx context.Context, tx storage.Tx) error {
			_, err := tx.WriteRelease(ctx, storage.ReleaseWrite{Root: bread, Commit: c.p.commit, Actor: cook})
			return err
		})
		refuses("UpsertRow on "+c.whose+" ref", "is not a ref of root", func(ctx context.Context, tx storage.Tx) error {
			_, err := tx.UpsertRow(ctx, "step", storage.RowWrite{Ref: c.p.ref.ID, Root: bread, Row: stepRow(t, "Mix", "Mix"), Actor: cook})
			return err
		})
	}
	refuses("InsertPatches of another graph's commit", "is not a commit of graph", func(ctx context.Context, tx storage.Tx) error {
		return tx.InsertPatches(ctx, theirs.commit, []storage.Patch{patch})
	})
	refuses("InsertSnapshot of another graph's commit", "is not a commit of graph", func(ctx context.Context, tx storage.Tx) error {
		return tx.InsertSnapshot(ctx, theirs.commit, []storage.SnapshotEntry{entry})
	})
	refuses("UpsertRow of an unknown kind", `unknown kind "flavour"`, func(ctx context.Context, tx storage.Tx) error {
		_, err := tx.UpsertRow(ctx, "flavour", storage.RowWrite{Ref: mine.ref.ID, Root: bread, Row: json.RawMessage(`{}`), Actor: cook})
		return err
	})
	// The graph's own refs and commits of the root are taken.
	must(in(s.storage, func(ctx context.Context, tx storage.Tx) (struct{}, error) {
		steps := []func() error{
			func() error {
				_, err := tx.CreateRef(ctx, storage.NewRef{Root: bread, Parent: mine.ref.ID, Base: mine.commit, Name: "draft", Actor: cook})
				return err
			},
			func() error {
				_, err := tx.UpdateRef(ctx, storage.RefUpdate{ID: mine.ref.ID, Version: 1, Head: mine.commit, Base: mine.commit, Actor: cook})
				return err
			},
			func() error { _, err := tx.InsertCommit(ctx, newCommit(mine.ref.ID, mine.commit)); return err },
			func() error {
				_, err := tx.WriteRelease(ctx, storage.ReleaseWrite{Root: bread, Commit: mine.commit, Actor: cook})
				return err
			},
			func() error {
				_, err := tx.UpsertRow(ctx, "step", storage.RowWrite{Ref: mine.ref.ID, Root: bread, Row: stepRow(t, "Mix", "Mix"), Actor: cook})
				return err
			},
			func() error { return tx.InsertPatches(ctx, mine.commit, []storage.Patch{patch}) },
			func() error { return tx.InsertSnapshot(ctx, mine.commit, []storage.SnapshotEntry{entry}) },
		}
		for _, step := range steps {
			if err := step(); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	}))(t)
}

// TestTwoGraphs: two graphs in one file keep apart: each reads, names,
// sequences, releases, prunes and sweeps only its own.
func TestTwoGraphs(t *testing.T) {
	now := int64(1_800_000_000_000_000)
	clock := func() int64 { return now }
	a := newSetup(t, sqlite.Options{Clock: clock}, "")
	ctx := context.Background()
	// Graph menu keeps a day of step history, where recipe keeps 365.
	d := descriptorDoc(t)
	kindOf(t, d, "step")["history"].(map[string]any)["retentionDays"] = 1
	menu := mustJSON(t, d)
	b := must(must(sqlite.New(menu, sqlite.Options{Graph: "menu", Clock: clock}))(t).Storage(ctx, a.client))(t)
	bEngine := must(engine.New(menu, b, engine.Options{SchemaEpoch: 1, SnapshotEvery: 3}))(t)
	main := must(a.engine.CreatePrimary(ctx, cook, bread, "main"))(t)
	// The same root and name in the other graph is not taken.
	other := must(bEngine.CreatePrimary(ctx, cook, bread, "main"))(t)
	if _, err := bEngine.Compose(ctx, main.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("menu composes recipe's ref: %v", err)
	}
	// Each graph saves a step three times on a change set, commits it
	// tagged, and merges it into its primary line, tagged.
	type work struct {
		draft          storage.Ref
		commit, merged *storage.Commit
	}
	do := func(eng *engine.Engine, primary storage.Ref) work {
		draft := must(eng.Branch(ctx, cook, primary.ID, "draft"))(t)
		for _, instruction := range []string{"Mix", "Mix well", "Mix hard"} {
			draft = must(eng.Save(ctx, cook, draft.ID, draft.Version, upserts("step", stepRow(t, "Mix", instruction))))(t).Ref
		}
		committed := must(eng.Commit(ctx, cook, draft.ID, draft.Version, engine.CommitOptions{Tag: true}))(t)
		merged := must(eng.Merge(ctx, cook, draft.ID, primary.ID, primary.Version, nil, engine.CommitOptions{Tag: true}))(t)
		return work{draft: committed.Ref, commit: committed.Commit, merged: merged.Commit}
	}
	aw := do(a.engine, main)
	if rows := must(in(b, func(ctx context.Context, tx storage.Tx) ([]json.RawMessage, error) {
		return tx.Rows(ctx, "step", aw.draft.ID)
	}))(t); len(rows) != 0 {
		t.Fatalf("menu reads recipe's rows %s", rows)
	}
	if commits := must(in(b, func(ctx context.Context, tx storage.Tx) ([]storage.CommitNode, error) { return tx.Commits(ctx) }))(t); len(commits) != 0 {
		t.Fatalf("menu reads recipe's commits %+v", commits)
	}
	if _, err := bEngine.Materialize(ctx, aw.commit.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("menu materializes recipe's commit: %v", err)
	}
	if _, err := bEngine.Release(ctx, cook, bread, aw.commit.ID, 0); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("menu releases recipe's commit: %v", err)
	}
	if _, err := bEngine.History(ctx, aw.draft.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("menu reads recipe's history: %v", err)
	}
	if _, err := bEngine.Branch(ctx, cook, aw.draft.ID, "x"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("menu branches recipe's ref: %v", err)
	}
	if next := must(in(b, func(ctx context.Context, tx storage.Tx) (int64, error) { return tx.NextSequence(ctx, bread) }))(t); next != 1 {
		t.Fatalf("menu's next sequence of Bread is %d, want 1", next)
	}
	// Both graphs tag the same root, each numbering its own tags from 1.
	bw := do(bEngine, other)
	if got := []int64{*aw.commit.Sequence, *aw.merged.Sequence, *bw.commit.Sequence, *bw.merged.Sequence}; !slices.Equal(got, []int64{1, 2, 1, 2}) {
		t.Fatalf("the graphs' tags are %v, want each graph's 1 and 2", got)
	}
	// A first pointer in each graph, then a move of menu's while recipe's is
	// at the same version, then a move of recipe's.
	must(a.engine.Release(ctx, cook, bread, aw.commit.ID, 0))(t)
	if _, err := bEngine.Released(ctx, bread); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("menu reads recipe's release: %v", err)
	}
	must(bEngine.Release(ctx, cook, bread, bw.commit.ID, 0))(t)
	must(bEngine.Release(ctx, cook, bread, bw.merged.ID, 1))(t)
	must(a.engine.Release(ctx, cook, bread, aw.merged.ID, 1))(t)
	ar, br := must(a.engine.Released(ctx, bread))(t).Release, must(bEngine.Released(ctx, bread))(t).Release
	if ar.Commit != aw.merged.ID || ar.Version != 2 || br.Commit != bw.merged.ID || br.Version != 2 {
		t.Fatalf("the releases are %+v and %+v, want each graph's merge at version 2", ar, br)
	}
	// A write through one graph's ref from the other is refused.
	_, err := in(b, func(ctx context.Context, tx storage.Tx) (json.RawMessage, error) {
		return tx.UpsertRow(ctx, "step", storage.RowWrite{Ref: aw.draft.ID, Root: bread, Row: stepRow(t, "Mix", "Mix"), Actor: cook})
	})
	errorContains(t, err, "is not a ref of root", "menu writing through recipe's ref")
	// Thirty days on, menu's sweep prunes its own step images past its day,
	// and none of recipe's, which keeps 365.
	images := func(graph string) int64 {
		return count(t, a.db, `SELECT count(*) FROM "graph_member_history" WHERE graph = ?1`, graph)
	}
	kept := images("recipe")
	now += 30 * 86_400_000_000
	if swept := must(bEngine.Sweep(ctx, engine.SweepOptions{Actor: cook}))(t); len(swept.Pruned) != 1 || swept.Pruned["step"] != 2 {
		t.Fatalf("menu's sweep pruned %v, want 2 step images", swept.Pruned)
	}
	if images("recipe") != kept {
		t.Fatalf("menu's sweep pruned recipe's images: %d of %d left", images("recipe"), kept)
	}
	if swept := must(a.engine.Sweep(ctx, engine.SweepOptions{Actor: cook}))(t); len(swept.Pruned) != 0 {
		t.Fatalf("recipe's sweep pruned %v within its 365 days", swept.Pruned)
	}
	if bh, ah := must(bEngine.Compose(ctx, other.ID))(t).ContentHash, must(a.engine.Compose(ctx, main.ID))(t).ContentHash; bh != ah {
		t.Fatalf("the primary lines hash %s and %s after the sweeps", bh, ah)
	}
	// Each graph discards its draft, and a week and a day on, past the
	// grace, menu's sweep collects its own draft's rows and leaves recipe's,
	// which recipe's own sweep collects.
	rowsOf := func(graph, ref string) int64 {
		return count(t, a.db, `SELECT count(*) FROM "graph_member" WHERE graph = ?1 AND ref_id = ?2`, graph, ref)
	}
	if err := a.engine.Discard(ctx, cook, aw.draft.ID, aw.draft.Version); err != nil {
		t.Fatal(err)
	}
	if err := bEngine.Discard(ctx, cook, bw.draft.ID, bw.draft.Version); err != nil {
		t.Fatal(err)
	}
	if rowsOf("recipe", aw.draft.ID) != 1 || rowsOf("menu", bw.draft.ID) != 1 {
		t.Fatal("a discard removed a draft's rows")
	}
	now += 8 * 86_400_000_000
	if swept := must(bEngine.Sweep(ctx, engine.SweepOptions{Actor: cook}))(t); swept.CollectedRefs != 1 || len(swept.CollectedRows) != 1 || swept.CollectedRows["step"] != 1 {
		t.Fatalf("menu's sweep collected %d refs and %v rows, want its draft's one step", swept.CollectedRefs, swept.CollectedRows)
	}
	if rowsOf("recipe", aw.draft.ID) != 1 || rowsOf("menu", bw.draft.ID) != 0 {
		t.Fatal("menu's sweep collected recipe's draft, or left its own")
	}
	if swept := must(a.engine.Sweep(ctx, engine.SweepOptions{Actor: cook}))(t); swept.CollectedRefs != 1 || swept.CollectedRows["step"] != 1 {
		t.Fatalf("recipe's sweep collected %d refs and %v rows", swept.CollectedRefs, swept.CollectedRows)
	}
	if rowsOf("recipe", aw.draft.ID) != 0 {
		t.Fatal("recipe's sweep left its draft's rows")
	}
}

// TestNameFunction: the name function names every table and index, and the
// default's statements name each object graph_ and its local name.
func TestNameFunction(t *testing.T) {
	name := func(local string) string { return "vg_" + local }
	s := newSetup(t, sqlite.Options{TableName: name}, "")
	ctx := context.Background()
	rows, err := s.db.Query(`SELECT type, name, tbl_name FROM sqlite_schema WHERE name NOT LIKE 'sqlite_autoindex_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	indexes := 0
	for rows.Next() {
		var kind, object, table string
		if err := rows.Scan(&kind, &object, &table); err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^vg_[a-z_]+$`).MatchString(object) || !strings.HasPrefix(table, "vg_") {
			t.Fatalf("%s %s on %s is not named by the function", kind, object, table)
		}
		switch kind {
		case "table":
			tables = append(tables, object)
		case "index":
			indexes++
		}
	}
	_ = rows.Close()
	var want []string
	for _, table := range sqlite.Tables() {
		want = append(want, name(table))
	}
	sort.Strings(want)
	if !slices.Equal(tables, want) || indexes != 12 {
		t.Fatalf("the file holds tables %q and %d indexes, want %q and 12", tables, indexes, want)
	}
	main := must(s.engine.CreatePrimary(ctx, cook, bread, "main"))(t)
	draft := must(s.engine.Branch(ctx, cook, main.ID, "draft"))(t)
	must(s.engine.Save(ctx, cook, draft.ID, draft.Version, upserts("step", stepRow(t, "Mix", "Mix"))))(t)
	if n := count(t, s.db, `SELECT count(*) FROM "vg_member"`); n != 1 {
		t.Fatalf("vg_member holds %d rows, want the one saved", n)
	}
	statements := must(sqlite.Layout(nil))(t)
	if len(statements) != 21 {
		t.Fatalf("the layout is %d statements, want 9 tables and 12 indexes", len(statements))
	}
	for _, statement := range statements {
		if !regexp.MustCompile(`^CREATE (UNIQUE )?(TABLE|INDEX) IF NOT EXISTS "graph_[a-z_]+" `).MatchString(statement) {
			t.Fatalf("a layout statement does not name its object graph_: %s", statement)
		}
	}
	// A name function that gives no name is refused.
	empty := func(local string) string {
		if local == "member_entity" {
			return ""
		}
		return name(local)
	}
	_, err = sqlite.Layout(empty)
	errorContains(t, err, `named "member_entity" ""`, "a layout whose name function names an index nothing")
	_, err = sqlite.New(readDescriptor(t), sqlite.Options{Graph: graph, TableName: func(string) string { return "" }})
	errorContains(t, err, "the table name function named", "an adapter whose name function names nothing")
}

// TestClockOncePerTransaction: a transaction reads the clock once, and
// every write in it has its time; one begun inside another, with its
// context, takes the outer one's.
func TestClockOncePerTransaction(t *testing.T) {
	calls := int64(0)
	start := int64(1_800_000_000_000_000)
	s := newSetup(t, sqlite.Options{Clock: func() int64 { calls++; return start + calls*1_000_001 }}, "")
	ctx := context.Background()
	calls = 0
	at := func(n int64) int64 { return start + n*1_000_001 }
	main := must(s.engine.CreatePrimary(ctx, cook, bread, "main"))(t)
	draft := must(s.engine.Branch(ctx, cook, main.ID, "draft"))(t)
	saved := must(s.engine.Save(ctx, cook, draft.ID, draft.Version, upserts("step", stepRow(t, "Mix", "Mix"), stepRow(t, "Rest", "Rest"))))(t)
	if calls != 3 {
		t.Fatalf("three operations read the clock %d times, want once each", calls)
	}
	for _, row := range saved.Saved["step"] {
		for _, column := range []string{"created_at", "updated_at"} {
			if v := memberText(t, row, column); v != `"2027-01-15T08:00:03.000003Z"` {
				t.Fatalf("a saved row's %s is %s, want the save's time", column, v)
			}
		}
	}
	if recorded := queryInts(t, s.db, `SELECT DISTINCT recorded_at FROM "graph_member_history"`); !slices.Equal(recorded, []int64{at(3)}) {
		t.Fatalf("the save's images are recorded at %v, want %d", recorded, at(3))
	}
	if times := queryInts(t, s.db, `SELECT created_at FROM "graph_ref" WHERE id = ?1 UNION ALL SELECT updated_at FROM "graph_ref" WHERE id = ?1`, draft.ID); !slices.Equal(times, []int64{at(2), at(3)}) {
		t.Fatalf("the draft was created and updated at %v, want %d and %d", times, at(2), at(3))
	}
	committed := must(s.engine.Commit(ctx, cook, draft.ID, saved.Ref.Version, engine.CommitOptions{}))(t)
	if calls != 4 || committed.Commit.CreatedAt != "2027-01-15T08:00:04.000004Z" {
		t.Fatalf("the commit is at %s after %d reads of the clock", committed.Commit.CreatedAt, calls)
	}
	// A transaction inside another is a savepoint at the outer one's time.
	var inner storage.Ref
	must(in(s.storage, func(ctx context.Context, tx storage.Tx) (struct{}, error) {
		if _, err := tx.CreateRef(ctx, storage.NewRef{Root: "Soup", Name: "outer", Actor: cook}); err != nil {
			return struct{}{}, err
		}
		var err error
		inner, err = in2(ctx, s.storage, func(ctx context.Context, nested storage.Tx) (storage.Ref, error) {
			return nested.CreateRef(ctx, storage.NewRef{Root: "Soup", Name: "inner", Actor: cook})
		})
		return struct{}{}, err
	}))(t)
	if calls != 5 {
		t.Fatalf("a transaction and one inside it read the clock %d times in all, want 5", calls)
	}
	if created := queryInts(t, s.db, `SELECT created_at FROM "graph_ref" WHERE id = ?1`, inner.ID); !slices.Equal(created, []int64{at(5)}) {
		t.Fatalf("the inner ref was created at %v, want the outer transaction's %d", created, at(5))
	}
}

// in2 is in, begun with a context: inside another transaction's function,
// its context.
func in2[T any](ctx context.Context, s storage.Storage, fn func(ctx context.Context, tx storage.Tx) (T, error)) (T, error) {
	var out T
	err := s.Transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		var err error
		out, err = fn(ctx, tx)
		return err
	})
	return out, err
}

// TestClockAfterLock: a transaction reads the clock once it holds the
// file's write lock.
func TestClockAfterLock(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/clock.sqlite"
	second := must(openDB(t, path, "_pragma=busy_timeout(0)").Conn(ctx))(t)
	t.Cleanup(func() { _ = second.Close() })
	// Whether another connection is kept from the write lock when the clock
	// is read.
	var held []bool
	clock := func() int64 {
		if _, err := second.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			code, _ := sqlite.ResultCode(err)
			held = append(held, code == sqlite.ResultBusy)
		} else {
			_, _ = second.ExecContext(ctx, "ROLLBACK")
			held = append(held, false)
		}
		return 1_800_000_000_000_000
	}
	s := newSetup(t, sqlite.Options{Clock: clock}, path)
	must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) {
		return tx.CreateRef(ctx, storage.NewRef{Root: bread, Name: "main", Actor: cook})
	}))(t)
	must(s.engine.CreatePrimary(ctx, cook, "Soup", "main"))(t)
	// Three transactions: the layout's, which reads the clock as the
	// TypeScript adapter's createTables does, the ref's and the primary
	// line's.
	if !slices.Equal(held, []bool{true, true, true}) {
		t.Fatalf("when each transaction read the clock, another connection was kept from the write lock: %v, want [true true true]", held)
	}
}

// TestCommitTime: a commit's time is a canonical date-time: no trailing
// zeros in its fraction, and no fraction when it is zero.
func TestCommitTime(t *testing.T) {
	now := int64(1_800_000_000_120_000)
	s := newSetup(t, sqlite.Options{Clock: func() int64 { return now }}, "")
	ref := must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) {
		return tx.CreateRef(ctx, storage.NewRef{Root: bread, Name: "main", Actor: cook})
	}))(t)
	commit := func() storage.Commit {
		return must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Commit, error) {
			return tx.InsertCommit(ctx, storage.NewCommit{Root: bread, Ref: ref.ID, SchemaEpoch: 1, ContentHash: strings.Repeat("0", 64), Actor: cook})
		}))(t)
	}
	if c := commit(); c.CreatedAt != "2027-01-15T08:00:00.12Z" {
		t.Fatalf("a commit at .120000 is at %s", c.CreatedAt)
	}
	now = 1_800_000_000_000_000
	whole := commit()
	read := must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Commit, error) { return tx.ReadCommit(ctx, whole.ID) }))(t)
	if whole.CreatedAt != "2027-01-15T08:00:00Z" || read.CreatedAt != whole.CreatedAt {
		t.Fatalf("a commit at a whole second is at %s, and reads back at %s", whole.CreatedAt, read.CreatedAt)
	}
	now = 1_800_000_000_000_001
	if c := commit(); c.CreatedAt != "2027-01-15T08:00:00.000001Z" {
		t.Fatalf("a commit a microsecond past is at %s", c.CreatedAt)
	}
}

// TestIDs: every id the adapter writes is a version-4 UUID in its
// canonical form.
func TestIDs(t *testing.T) {
	s := newSetup(t, sqlite.Options{}, "")
	ctx := context.Background()
	main := must(s.engine.CreatePrimary(ctx, cook, bread, "main"))(t)
	draft := must(s.engine.Branch(ctx, cook, main.ID, "draft"))(t)
	saved := must(s.engine.Save(ctx, cook, draft.ID, draft.Version, upserts("step", stepRow(t, nil, "Mix"))))(t)
	committed := must(s.engine.Commit(ctx, cook, draft.ID, saved.Ref.Version, engine.CommitOptions{}))(t)
	merged := must(s.engine.Merge(ctx, cook, draft.ID, main.ID, main.Version, nil, engine.CommitOptions{Tag: true}))(t)
	must(s.engine.Release(ctx, cook, bread, merged.Commit.ID, 0))(t)
	var ids []string
	for _, c := range []struct {
		table   string
		columns []string
	}{
		{"ref", []string{"id"}}, {"ref_history", []string{"history_id"}}, {"commit", []string{"id"}}, {"patch", []string{"id"}},
		{"snapshot_entry", []string{"id"}}, {"release", []string{"id"}}, {"release_history", []string{"history_id"}},
		{"member", []string{"id", "entity_key"}}, {"member_history", []string{"history_id"}},
	} {
		rows, err := s.db.Query(`SELECT ` + strings.Join(c.columns, ", ") + ` FROM "graph_` + c.table + `"`)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for rows.Next() {
			values := make([]string, len(c.columns))
			dest := make([]any, len(values))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			for i, value := range values {
				assertCanonicalID(t, value, c.table+"."+c.columns[i])
				ids = append(ids, value)
			}
			n++
		}
		_ = rows.Close()
		if n == 0 {
			t.Fatalf("%s has no rows", c.table)
		}
	}
	unique := map[string]bool{}
	for _, id := range ids {
		unique[id] = true
	}
	if len(unique) != len(ids)-1 {
		t.Fatalf("%d ids, %d distinct: want every id new but the entity key two rows share", len(ids), len(unique))
	}
	assertCanonicalID(t, committed.Commit.ID, "a commit's id")
}

// TestSavepoints: a transaction that fails rolls back, and one inside
// another, begun with its context, is a savepoint that rolls back alone.
func TestSavepoints(t *testing.T) {
	s := newSetup(t, sqlite.Options{}, "")
	create := func(ctx context.Context, tx storage.Tx, name string) error {
		_, err := tx.CreateRef(ctx, storage.NewRef{Root: bread, Name: name, Actor: cook})
		return err
	}
	failed := errors.New("rolled back")
	if _, err := in(s.storage, func(ctx context.Context, tx storage.Tx) (struct{}, error) {
		if err := create(ctx, tx, "gone"); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, failed
	}); !errors.Is(err, failed) {
		t.Fatalf("a transaction that fails = %v, want its error", err)
	}
	must(in(s.storage, func(ctx context.Context, tx storage.Tx) (struct{}, error) {
		if err := create(ctx, tx, "kept"); err != nil {
			return struct{}{}, err
		}
		_, err := in2(ctx, s.storage, func(ctx context.Context, nested storage.Tx) (struct{}, error) {
			if err := create(ctx, nested, "inner"); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, create(ctx, nested, "kept")
		})
		if !errors.Is(err, storage.ErrNameTaken) {
			t.Errorf("the nested transaction = %v, want name taken", err)
		}
		return struct{}{}, create(ctx, tx, "after")
	}))(t)
	names := func() []string {
		rows, err := s.db.Query(`SELECT name FROM "graph_ref" ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var out []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			out = append(out, name)
		}
		return out
	}
	if got := names(); !slices.Equal(got, []string{"after", "kept"}) {
		t.Fatalf("the refs are %q, want after and kept", got)
	}
	// A transaction that panics rolls back, and the panic goes on.
	func() {
		defer func() {
			if p := recover(); p != "boom" {
				t.Fatalf("recovered %v, want the transaction's panic", p)
			}
		}()
		_ = s.storage.Transact(context.Background(), func(ctx context.Context, tx storage.Tx) error {
			if err := create(ctx, tx, "panicked"); err != nil {
				return err
			}
			panic("boom")
		})
	}()
	if got := names(); !slices.Equal(got, []string{"after", "kept"}) {
		t.Fatalf("after a panic the refs are %q, want after and kept", got)
	}
	// So does one whose goroutine exits inside it, as a test's FailNow
	// does, and the write lock is free again.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.storage.Transact(context.Background(), func(ctx context.Context, tx storage.Tx) error {
			if err := create(ctx, tx, "exited"); err != nil {
				return err
			}
			runtime.Goexit()
			return nil
		})
	}()
	<-done
	if got := names(); !slices.Equal(got, []string{"after", "kept"}) {
		t.Fatalf("after a goroutine exited in a transaction the refs are %q, want after and kept", got)
	}
	must(in(s.storage, func(ctx context.Context, tx storage.Tx) (struct{}, error) {
		return struct{}{}, create(ctx, tx, "later")
	}))(t)
}
