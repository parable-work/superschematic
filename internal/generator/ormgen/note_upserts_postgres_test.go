package ormgen

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
)

// noteUpsertCases are the cases the generated module's test runs over
// fixture-approvals-db's Note, by name (D104): an upsert's update of the
// note its slug holds runs through the core's update as UpdateOne's does,
// so the guards of Note's Constants and StateRules hold it, the caller
// WithBehaviorCaller names among them; its statement writes nothing of the
// held note, whose version the core's update moves once, and only if
// changed not at all when the note holds the update's values; a refusal
// leaves the upsert unwritten, the other inputs' notes among it; and a
// soft-deleted note its key holds is ErrNotFound.
var noteUpsertCases = []string{
	"an upsert of a new slug inserts the note with what the core's create gives it",
	"an upsert of a held slug that changes the author is refused as a constant, and the note stays as it was",
	"a caller holding notes.admin changes the author by an upsert",
	"an upsert that moves pinned forward is saved, its version moved once, and one that moves it back is refused as one_way",
	"an upsert that changes a draft's body is refused as frozen_fields for a caller its by does not name, and saved for its creator",
	"an upsert only if changed of the values a note holds leaves it unmoved, and of another value saves it",
	"an upsert many updates each held note through the core and inserts each new one",
	"an upsert many whose second input's held note would change its author is refused, and its first input's new note is not inserted",
	"an upsert of a deleted note's slug is ErrNotFound and writes nothing, an upsert many's new notes among it",
}

// TestNoteUpsertsOnPostgres holds the generated Go ORM's upserts of a
// table whose behaviors guard its updates to the core on Postgres (D104).
// It loads fixture-approvals-db with the core's registry, generates its Go
// types, its ORM and its DDL into a temporary tree, and builds, vets and
// tests the ORM module with noteUpsertsORMTest, whose every case
// (noteUpsertCases) must pass on the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL. The module links the core's
// static archive, which scripts/rules-archive.sh stages; the test skips
// without it, and in -short mode.
func TestNoteUpsertsOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Note upserts' end-to-end check in -short mode")
	}
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated Note upserts against Postgres")
	}
	paths := testpaths.Local(t)
	archive := filepath.Join(paths.RulesGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperschematic_rules.a")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("the behaviors' core's archive is not staged at %s: run scripts/rules-archive.sh (make rules-archive)", archive)
	}

	ormDir := generateBehaviorORMModule(t, paths, approvalsFixture)
	if err := os.WriteFile(filepath.Join(ormDir, "note_upserts_test.go"), []byte(noteUpsertsORMTest), 0o644); err != nil {
		t.Fatalf("write the Note upserts test: %v", err)
	}
	out := runORMModule(t, ormDir)
	for _, name := range noteUpsertCases {
		if want := "--- PASS: TestNoteUpsertsOnPostgres/" + strings.ReplaceAll(name, " ", "_"); !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q:\n%s", name, out)
		}
	}
}

// noteUpsertsORMTest is the generated module's test: the cases of
// noteUpsertCases on the generated repository, in a schema of its own.
const noteUpsertsORMTest = `package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-approvals-db"
	"github.com/jackc/pgx/v5/pgxpool"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
)

// refusalOf is the *rules.Refusal err holds, failing the test without one.
func refusalOf(t *testing.T, what string, err error) *rules.Refusal {
	t.Helper()
	refusal, ok := rules.AsRefusal(err)
	if !ok {
		t.Fatalf("%s = %v, want the core's refusal", what, err)
	}
	return refusal
}

// sameJSON reports whether two JSON documents hold the same values, members
// in any order.
func sameJSON(t *testing.T, got, want string) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("decode %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("decode %s: %v", want, err)
	}
	return reflect.DeepEqual(g, w)
}

// wantVetoed holds err to the core's veto: 409 vetoed, the veto's code and
// its details.
func wantVetoed(t *testing.T, what string, err error, vetoCode, vetoDetails string) {
	t.Helper()
	refusal := refusalOf(t, what, err)
	var details struct {
		VetoCode    string          ` + "`" + `json:"vetoCode"` + "`" + `
		VetoDetails json.RawMessage ` + "`" + `json:"vetoDetails"` + "`" + `
	}
	if err := json.Unmarshal(refusal.Details, &details); err != nil {
		t.Fatalf("%s: details %s: %v", what, refusal.Details, err)
	}
	if refusal.Status != 409 || refusal.Code != rules.CodeVetoed || details.VetoCode != vetoCode || !sameJSON(t, string(details.VetoDetails), vetoDetails) {
		t.Fatalf("%s refused with %d %s (vetoCode %s, vetoDetails %s), want 409 vetoed, vetoCode %s, vetoDetails %s", what, refusal.Status, refusal.Code, details.VetoCode, details.VetoDetails, vetoCode, vetoDetails)
	}
}

// wantConstant holds err to Constants' refusal of a change of author: 422
// invalid_instance, one issue at author, the rule constant.
func wantConstant(t *testing.T, what string, err error) {
	t.Helper()
	refusal := refusalOf(t, what, err)
	var details struct {
		Issues []struct{ Path, Rule string } ` + "`" + `json:"issues"` + "`" + `
	}
	if err := json.Unmarshal(refusal.Details, &details); err != nil {
		t.Fatalf("%s: details %s: %v", what, refusal.Details, err)
	}
	if refusal.Status != 422 || refusal.Code != rules.CodeInvalidInstance || len(details.Issues) != 1 || details.Issues[0].Path != "author" || details.Issues[0].Rule != "constant" {
		t.Fatalf("%s refused with %d %s %s, want 422 invalid_instance, one issue at author, rule constant", what, refusal.Status, refusal.Code, refusal.Details)
	}
}

func TestNoteUpsertsOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the Note upserts against Postgres")
	}
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer base.Close()
	schema := fmt.Sprintf("ormgen_note_upserts_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		pool.Close()
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatalf("connect generated orm: %v", err)
	}
	defer db.Close()

	alice, err := types.ParseIdentityUUID("5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := types.ParseIdentityUUID("6a1d4b63-9b6f-4d2c-8e2f-3a7a2c8d9ea1")
	if err != nil {
		t.Fatal(err)
	}
	asAlice := WithUserID(ctx, alice)
	asBob := WithUserID(ctx, bob)
	text := func(value string) *string { return &value }
	flag := func(value bool) *bool { return &value }
	number := func(value float64) *float64 { return &value }
	create := func(slug string) *NoteCreate {
		return &NoteCreate{Slug: slug, Title: "A note", Body: "First body", Author: alice, Views: 1}
	}

	// note is a draft of slug, which alice creates by an upsert.
	note := func(t *testing.T, slug string) *types.Note {
		t.Helper()
		result, err := db.Note.UpsertOneResult(asAlice, create(slug), NoteUniqueBySlug, &NoteUpdate{Title: text("A note")}, nil)
		if err != nil {
			t.Fatalf("alice's upsert of %s: %v", slug, err)
		}
		if result.Status != UpsertInserted {
			t.Fatalf("alice's upsert of %s = %s, want inserted", slug, result.Status)
		}
		return result.Row
	}
	read := func(t *testing.T, row *types.Note) *types.Note {
		t.Helper()
		got, err := db.Note.GetOne(ctx, *row.Id, nil)
		if err != nil {
			t.Fatalf("read %s: %v", row.Slug, err)
		}
		return got
	}
	// unmoved holds the note to what it was before a refused upsert: its
	// fields and its version.
	unmoved := func(t *testing.T, what string, before *types.Note) {
		t.Helper()
		after := read(t, before)
		if after.Version != before.Version || after.Author != before.Author || after.Pinned != before.Pinned || after.Views != before.Views || after.Body != before.Body || after.Title != before.Title {
			t.Fatalf("after %s the note is %+v, want it as it was, %+v", what, after, before)
		}
	}
	// upsert is an upsert of slug's note as the caller ctx names, which
	// wants it held and updated.
	upsert := func(t *testing.T, ctx context.Context, slug string, update *NoteUpdate) (*types.Note, error) {
		t.Helper()
		result, err := db.Note.UpsertOneResult(ctx, create(slug), NoteUniqueBySlug, update, nil)
		if err != nil {
			return nil, err
		}
		if result.Status != UpsertUpdated {
			t.Fatalf("the upsert of %s = %s, want updated", slug, result.Status)
		}
		return result.Row, nil
	}

	t.Run("an upsert of a new slug inserts the note with what the core's create gives it", func(t *testing.T) {
		row := note(t, "new")
		if row.Slug != "new" || row.Title != "A note" || row.Author != alice || row.CreatedBy != alice || row.Status != string(NoteStatusStateDraft) || row.StatusEnteredAt == nil || row.Version != 1 {
			t.Fatalf("the inserted note = %+v, want new, a draft alice created, at version 1", row)
		}
	})

	t.Run("an upsert of a held slug that changes the author is refused as a constant, and the note stays as it was", func(t *testing.T) {
		before := note(t, "constant")
		_, err := db.Note.UpsertOneResult(asAlice, create("constant"), NoteUniqueBySlug, &NoteUpdate{Author: &bob}, nil)
		wantConstant(t, "alice's upsert of bob as author", err)
		unmoved(t, "alice's upsert of bob as author", before)
		// The same author is no change of it.
		row, err := upsert(t, asAlice, "constant", &NoteUpdate{Author: &alice, Title: text("Same author")})
		if err != nil {
			t.Fatalf("alice's upsert of the same author: %v", err)
		}
		if row.Author != alice || row.Title != "Same author" || row.Version != before.Version+1 {
			t.Fatalf("the note = %+v, want alice's, its title saved, at version %d", row, before.Version+1)
		}
	})

	t.Run("a caller holding notes.admin changes the author by an upsert", func(t *testing.T) {
		before := note(t, "admin")
		admin := WithBehaviorCaller(asAlice, rules.Caller{Subject: fmt.Sprint(alice), Permissions: []string{"notes.admin"}})
		row, err := upsert(t, admin, "admin", &NoteUpdate{Author: &bob})
		if err != nil {
			t.Fatalf("the admin's upsert of bob as author: %v", err)
		}
		if row.Author != bob || row.Version != before.Version+1 {
			t.Fatalf("the note = %+v, want bob's at version %d", row, before.Version+1)
		}
	})

	t.Run("an upsert that moves pinned forward is saved, its version moved once, and one that moves it back is refused as one_way", func(t *testing.T) {
		before := note(t, "pinned")
		row, err := upsert(t, asAlice, "pinned", &NoteUpdate{Pinned: flag(true), Views: number(4)})
		if err != nil {
			t.Fatalf("alice's pin: %v", err)
		}
		if !row.Pinned || row.Views != 4 || row.Version != before.Version+1 {
			t.Fatalf("the pinned note = %+v, want pinned, 4 views, at version %d", row, before.Version+1)
		}
		if got := read(t, row); !got.Pinned || got.Views != 4 || got.Version != row.Version {
			t.Fatalf("the note read back = %+v, want it as the upsert returned it, %+v", got, row)
		}
		_, err = db.Note.UpsertOneResult(asAlice, create("pinned"), NoteUniqueBySlug, &NoteUpdate{Pinned: flag(false)}, nil)
		wantVetoed(t, "alice's unpin", err, "one_way", ` + "`" + `{"attempted":false,"current":true,"direction":"up","field":"pinned"}` + "`" + `)
		unmoved(t, "alice's unpin", row)
		_, err = db.Note.UpsertOneResult(asAlice, create("pinned"), NoteUniqueBySlug, &NoteUpdate{Views: number(2)}, nil)
		wantVetoed(t, "alice's fewer views", err, "one_way", ` + "`" + `{"attempted":2,"current":4,"direction":"up","field":"views"}` + "`" + `)
		unmoved(t, "alice's fewer views", row)
	})

	t.Run("an upsert that changes a draft's body is refused as frozen_fields for a caller its by does not name, and saved for its creator", func(t *testing.T) {
		before := note(t, "frozen")
		_, err := db.Note.UpsertOneResult(asBob, create("frozen"), NoteUniqueBySlug, &NoteUpdate{Body: text("Bob's body")}, nil)
		wantVetoed(t, "bob's body", err, "frozen_fields", ` + "`" + `{"by":["createdBy"],"fields":["body"],"state":"draft"}` + "`" + `)
		unmoved(t, "bob's body", before)
		// bob may change what the draft does not freeze.
		row, err := upsert(t, asBob, "frozen", &NoteUpdate{Title: text("Bob's title")})
		if err != nil {
			t.Fatalf("bob's title: %v", err)
		}
		if row.Title != "Bob's title" || row.Body != "First body" || row.CreatedBy != alice {
			t.Fatalf("the note = %+v, want bob's title, the first body, alice's", row)
		}
		row, err = upsert(t, asAlice, "frozen", &NoteUpdate{Body: text("Second body")})
		if err != nil {
			t.Fatalf("alice's body: %v", err)
		}
		if row.Body != "Second body" || row.Version != before.Version+2 {
			t.Fatalf("the note = %+v, want the second body at version %d", row, before.Version+2)
		}
	})

	t.Run("an upsert only if changed of the values a note holds leaves it unmoved, and of another value saves it", func(t *testing.T) {
		before := note(t, "changed")
		only := &UpsertOptions{OnlyIfChanged: true}
		same, err := db.Note.UpsertOneResult(asAlice, create("changed"), NoteUniqueBySlug, &NoteUpdate{Title: text("A note"), Body: text("First body"), Views: number(1)}, only)
		if err != nil {
			t.Fatalf("an upsert of the values the note holds: %v", err)
		}
		if same.Status != UpsertUnchanged || same.Row.Version != before.Version || *same.Row.Id != *before.Id {
			t.Fatalf("an upsert of the values the note holds = %s, %+v; want unchanged at version %d", same.Status, same.Row, before.Version)
		}
		unmoved(t, "an upsert of the values the note holds", before)
		// Only if changed, a refusal is the core's still.
		_, err = db.Note.UpsertOneResult(asBob, create("changed"), NoteUniqueBySlug, &NoteUpdate{Body: text("Bob's body")}, only)
		wantVetoed(t, "bob's body only if changed", err, "frozen_fields", ` + "`" + `{"by":["createdBy"],"fields":["body"],"state":"draft"}` + "`" + `)
		other, err := db.Note.UpsertOneResult(asAlice, create("changed"), NoteUniqueBySlug, &NoteUpdate{Title: text("A note"), Views: number(3)}, only)
		if err != nil {
			t.Fatalf("an upsert of another value: %v", err)
		}
		if other.Status != UpsertUpdated || other.Row.Views != 3 || other.Row.Version != before.Version+1 {
			t.Fatalf("an upsert of another value = %s, %+v; want updated, 3 views, at version %d", other.Status, other.Row, before.Version+1)
		}
	})

	type got struct {
		Status UpsertStatus
		Slug   string
		Title  string
		Author types.IdentityUUID
	}
	results := func(many []UpsertResult[*types.Note]) []got {
		var out []got
		for _, result := range many {
			out = append(out, got{result.Status, result.Row.Slug, result.Row.Title, result.Row.Author})
		}
		return out
	}

	t.Run("an upsert many updates each held note through the core and inserts each new one", func(t *testing.T) {
		before := note(t, "many-held")
		inputs := []*NoteCreate{create("many-held"), create("many-new")}
		inputs[0].Title = "Held title"
		inputs[1].Title = "New title"
		many, err := db.Note.UpsertMany(asAlice, inputs, NoteUniqueBySlug, NoteFields{Title: true}, nil)
		if err != nil {
			t.Fatalf("upsert many: %v", err)
		}
		if want := []got{{UpsertUpdated, "many-held", "Held title", alice}, {UpsertInserted, "many-new", "New title", alice}}; !reflect.DeepEqual(results(many), want) {
			t.Fatalf("upsert many = %+v, want %+v", results(many), want)
		}
		if many[0].Row.Version != before.Version+1 || many[1].Row.Version != 1 {
			t.Fatalf("the versions = %d and %d, want %d and 1", many[0].Row.Version, many[1].Row.Version, before.Version+1)
		}
		// Only if changed, a held note that holds the input's values is
		// left as it is.
		again, err := db.Note.UpsertMany(asAlice, inputs, NoteUniqueBySlug, NoteFields{Title: true}, &UpsertOptions{OnlyIfChanged: true})
		if err != nil {
			t.Fatalf("upsert many only if changed: %v", err)
		}
		if want := []got{{UpsertUnchanged, "many-held", "Held title", alice}, {UpsertUnchanged, "many-new", "New title", alice}}; !reflect.DeepEqual(results(again), want) {
			t.Fatalf("upsert many only if changed = %+v, want %+v", results(again), want)
		}
		if again[0].Row.Version != many[0].Row.Version || again[1].Row.Version != 1 {
			t.Fatalf("the versions only if changed = %d and %d, want %d and 1", again[0].Row.Version, again[1].Row.Version, many[0].Row.Version)
		}
		// A held note's guards hold an upsert many: bob's change of a
		// draft's body is refused.
		inputs[0].Body = "Bob's body"
		_, err = db.Note.UpsertMany(asBob, inputs[:1], NoteUniqueBySlug, NoteFields{Body: true}, nil)
		wantVetoed(t, "bob's upsert many of the body", err, "frozen_fields", ` + "`" + `{"by":["createdBy"],"fields":["body"],"state":"draft"}` + "`" + `)
		unmoved(t, "bob's upsert many of the body", many[0].Row)
	})

	t.Run("an upsert many whose second input's held note would change its author is refused, and its first input's new note is not inserted", func(t *testing.T) {
		before := note(t, "many-constant")
		inputs := []*NoteCreate{create("many-fresh"), create("many-constant")}
		inputs[1].Author = bob
		_, err := db.Note.UpsertMany(asAlice, inputs, NoteUniqueBySlug, NoteFields{Author: true, Title: true}, nil)
		wantConstant(t, "an upsert many of bob as the held note's author", err)
		unmoved(t, "an upsert many of bob as the held note's author", before)
		if _, err := db.Note.FindOne(ctx, &NoteFilter{Slug: &StringFilter{Eq: text("many-fresh")}}, nil); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a read of the refused upsert's new note = %v, want ErrNotFound", err)
		}
	})

	t.Run("an upsert of a deleted note's slug is ErrNotFound and writes nothing, an upsert many's new notes among it", func(t *testing.T) {
		deleted := note(t, "deleted")
		if err := db.Note.DeleteOne(asAlice, *deleted.Id); err != nil {
			t.Fatalf("delete: %v", err)
		}
		// stored is the deleted note as its row holds it.
		stored := func(t *testing.T) (string, int64) {
			t.Helper()
			var title string
			var version int64
			var at *time.Time
			if err := pool.QueryRow(ctx, "SELECT title, _version, deleted_at FROM note WHERE id = $1", deleted.Id.ToUUID()).Scan(&title, &version, &at); err != nil {
				t.Fatalf("read the deleted note: %v", err)
			}
			if at == nil {
				t.Fatal("the deleted note holds no deleted_at")
			}
			return title, version
		}
		title, version := stored(t)
		for _, update := range []*NoteUpdate{{Title: text("Revived")}, {}} {
			for _, only := range []bool{false, true} {
				_, err := db.Note.UpsertOneResult(asAlice, create("deleted"), NoteUniqueBySlug, update, &UpsertOptions{OnlyIfChanged: only})
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("an upsert of the deleted note's slug (update %+v, only if changed %t) = %v, want ErrNotFound", update, only, err)
				}
			}
		}
		_, err := db.Note.UpsertMany(asAlice, []*NoteCreate{create("deleted-fresh"), create("deleted")}, NoteUniqueBySlug, NoteFields{Title: true}, nil)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("an upsert many of the deleted note's slug = %v, want ErrNotFound", err)
		}
		if gotTitle, gotVersion := stored(t); gotTitle != title || gotVersion != version {
			t.Fatalf("the deleted note holds %q at version %d, want %q at version %d", gotTitle, gotVersion, title, version)
		}
		if _, err := db.Note.FindOne(ctx, &NoteFilter{Slug: &StringFilter{Eq: text("deleted-fresh")}}, nil); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a read of the refused upsert many's new note = %v, want ErrNotFound", err)
		}
	})
}
`
