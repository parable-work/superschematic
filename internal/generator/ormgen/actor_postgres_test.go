package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
)

const actorFixture = "fixture-actor-db"

// TestGeneratedORMActsAsTheCallerAndTheSystem generates the Go ORM of
// fixture-actor-db, whose Note has a required createdBy, a nullable
// updatedBy and deletedBy, and runs actorORMTest in it on the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (D87): a write with no user
// refuses where createdBy needs one; WithCaller, as a generated route
// binds a request's caller by its principal's ID, stamps createdBy and
// updatedBy, the nullable one through its pointer; AsSystem stamps the
// system actor the DB service names, as a job's or a worker's write acts;
// and an update with no user leaves the nullable updatedBy as it was. The
// module is generated again with requireUpdatedBy, under which that update
// refuses, by UpdateOne, UpdateMany and UpsertOne alike. UpsertMany,
// UpsertOneResult and a savepoint (D89) stamp the caller too.
func TestGeneratedORMActsAsTheCallerAndTheSystem(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	for name, actor := range map[string]sqlutil.Actor{
		"named system actor":                   {SystemID: "3f2c1a9e-7b4d-4e8a-9c61-0d5e2f7a8b10", SystemName: "jobs", SystemSet: true},
		"named system actor, updates need one": {SystemID: "3f2c1a9e-7b4d-4e8a-9c61-0d5e2f7a8b10", SystemName: "jobs", SystemSet: true, RequireUpdatedBy: true},
	} {
		t.Run(name, func(t *testing.T) {
			ormDir := generateORMModuleAt(t, filepath.Join(fixturesDir, actorFixture), actorFixture, func(o *Options) { o.Actor = actor })
			test := strings.Replace(actorORMTest, "REQUIRE_UPDATED_BY", map[bool]string{true: "true", false: "false"}[actor.RequireUpdatedBy], 1)
			if err := os.WriteFile(filepath.Join(ormDir, "actor_test.go"), []byte(test), 0o644); err != nil {
				t.Fatal(err)
			}
			out := runORMModule(t, ormDir)
			if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" && !strings.Contains(out, "--- PASS: TestActorOnPostgres") {
				t.Fatalf("the generated ORM did not run TestActorOnPostgres:\n%s", out)
			}
		})
	}
}

// TestDefaultSystemActor: without outputs.sql.systemActor the ORM acts as
// the ID the generated Go APIs export as SystemUserID, named "system".
func TestDefaultSystemActor(t *testing.T) {
	if actor := actorOf(sqlutil.Actor{}); actor.SystemID != "00000000-0000-4000-8000-000000000000" || actor.SystemName != "system" {
		t.Fatalf("the default system actor is %+v", actor)
	}
}

const actorORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	types "example.com/schemas/types/go/fixture-actor-db"
)

const requireUpdatedBy = REQUIRE_UPDATED_BY

func TestActorOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the acting user against Postgres")
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
	schema := fmt.Sprintf("actor_orm_%d", time.Now().UnixNano())
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

	// A principal's ID in its base62 wire form, as the identity runtime
	// holds it, and hyphenated.
	alice, err := ParseUserID("s04Ur0Zhc5xp2izfzlb6K")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := ParseUserID("6f1d2c3b-4a5e-4f60-8172-93a4b5c6d7e8")
	if err != nil {
		t.Fatal(err)
	}
	text := func(s string) *string { return &s }

	// No user: a create of a table with a required createdBy refuses.
	if _, err := db.Note.CreateOne(ctx, &types.Note{Text: "nobody's"}); !errors.Is(err, ErrNoUserInContext) {
		t.Fatalf("a create with no user = %v, want ErrNoUserInContext", err)
	}

	// The caller a route binds stamps createdBy and the nullable updatedBy.
	byAlice := WithCaller(ctx, "s04Ur0Zhc5xp2izfzlb6K")
	note, err := db.Note.CreateOne(byAlice, &types.Note{Text: "alice's"})
	if err != nil {
		t.Fatalf("create as the caller: %v", err)
	}
	if note.CreatedBy != alice || note.UpdatedBy == nil || *note.UpdatedBy != alice {
		t.Fatalf("a create as alice recorded createdBy %v, updatedBy %v", note.CreatedBy, note.UpdatedBy)
	}
	updated, err := db.Note.UpdateOne(WithCaller(ctx, "6f1d2c3b-4a5e-4f60-8172-93a4b5c6d7e8"), *note.Id, &NoteUpdate{Text: text("bob's edit")})
	if err != nil {
		t.Fatalf("update as the caller: %v", err)
	}
	if updated.UpdatedBy == nil || *updated.UpdatedBy != bob || updated.CreatedBy != alice {
		t.Fatalf("an update as bob recorded createdBy %v, updatedBy %v", updated.CreatedBy, updated.UpdatedBy)
	}

	// WithCaller keeps a user the context has, and an ID that is no user's
	// binds no one.
	if got := GetUserID(WithCaller(AsSystem(ctx), "s04Ur0Zhc5xp2izfzlb6K")); got != SystemActorID {
		t.Fatalf("WithCaller over AsSystem acts as %v", got)
	}
	if HasUserID(WithCaller(ctx, "not a user")) || HasUserID(WithCaller(ctx, "")) {
		t.Fatal("WithCaller bound an ID that is no user's")
	}

	// AsSystem acts as the DB service's system actor, which needs no user
	// row: the audit fields have no foreign key.
	system, err := ParseUserID("3f2c1a9e-7b4d-4e8a-9c61-0d5e2f7a8b10")
	if err != nil {
		t.Fatal(err)
	}
	if SystemActorID != system || SystemActorName != "jobs" {
		t.Fatalf("the system actor is %v %q", SystemActorID, SystemActorName)
	}
	jobs, err := db.Note.CreateOne(AsSystem(ctx), &types.Note{Text: "a worker's"})
	if err != nil {
		t.Fatalf("create as the system: %v", err)
	}
	if jobs.CreatedBy != system || jobs.UpdatedBy == nil || *jobs.UpdatedBy != system {
		t.Fatalf("a create as the system recorded createdBy %v, updatedBy %v", jobs.CreatedBy, jobs.UpdatedBy)
	}

	// An update with no user leaves the nullable updatedBy as it was, or,
	// under requireUpdatedBy, refuses, as UpdateMany and UpsertOne do.
	unattributed, err := db.Note.UpdateOne(ctx, *jobs.Id, &NoteUpdate{Text: text("nobody's edit")})
	many, manyErr := db.Note.UpdateMany(ctx, &NoteFilter{Id: &UUIDFilter{Eq: jobs.Id}}, &NoteUpdate{Text: text("nobody's edits")})
	if requireUpdatedBy {
		if !errors.Is(err, ErrNoUserInContext) || !errors.Is(manyErr, ErrNoUserInContext) {
			t.Fatalf("an update with no user under requireUpdatedBy = %v and %v (%d rows), want ErrNoUserInContext", err, manyErr, many)
		}
		if _, err := db.Note.UpsertOne(ctx, &types.Note{Id: jobs.Id, Text: "upserted"}, NoteUniqueById, &NoteUpdate{Text: text("upserted")}); !errors.Is(err, ErrNoUserInContext) {
			t.Fatalf("an upsert with no user under requireUpdatedBy = %v, want ErrNoUserInContext", err)
		}
	} else {
		if err != nil || manyErr != nil || many != 1 {
			t.Fatalf("an update with no user = %v, %v (%d rows)", err, manyErr, many)
		}
		if unattributed.UpdatedBy == nil || *unattributed.UpdatedBy != system {
			t.Fatalf("an update with no user changed updatedBy to %v", unattributed.UpdatedBy)
		}
	}

	// UpsertMany (D89) stamps as a create and an update do, the caller in
	// the nullable updatedBy's pointer, and refuses with no user, as the
	// required createdBy needs one; UpsertOneResult only if changed, which
	// runs in a transaction of its own, and a savepoint act as the caller.
	byBob := WithCaller(ctx, "6f1d2c3b-4a5e-4f60-8172-93a4b5c6d7e8")
	results, err := db.Note.UpsertMany(byBob, []*types.Note{{Id: jobs.Id, Text: "bob's upsert"}, {Text: "bob's new note"}}, NoteUniqueById, NoteFields{Text: true}, nil)
	if err != nil {
		t.Fatalf("UpsertMany as bob: %v", err)
	}
	if len(results) != 2 || results[0].Status != UpsertUpdated || results[0].Row.UpdatedBy == nil || *results[0].Row.UpdatedBy != bob ||
		results[1].Status != UpsertInserted || results[1].Row.CreatedBy != bob || results[1].Row.UpdatedBy == nil || *results[1].Row.UpdatedBy != bob {
		t.Fatalf("UpsertMany as bob = %+v", results)
	}
	if _, err := db.Note.UpsertMany(ctx, []*types.Note{{Id: jobs.Id, Text: "nobody's upsert"}}, NoteUniqueById, NoteFields{Text: true}, nil); !errors.Is(err, ErrNoUserInContext) {
		t.Fatalf("UpsertMany with no user = %v, want ErrNoUserInContext", err)
	}
	changed, err := db.Note.UpsertOneResult(byAlice, &types.Note{Id: jobs.Id, Text: "alice's upsert"}, NoteUniqueById, &NoteUpdate{Text: text("alice's upsert")}, &UpsertOptions{OnlyIfChanged: true})
	if err != nil || changed.Status != UpsertUpdated || changed.Row.UpdatedBy == nil || *changed.Row.UpdatedBy != alice {
		t.Fatalf("UpsertOneResult only if changed as alice = %+v, %v", changed, err)
	}
	err = db.TransactionWith(byBob, TxOptions{}, func(tx TxInterface) error {
		return tx.Transaction(byBob, func(nested TxInterface) error {
			edited, err := nested.GetNoteRepository().UpdateOne(byBob, *jobs.Id, &NoteUpdate{Text: text("bob's savepoint edit")})
			if err == nil && (edited.UpdatedBy == nil || *edited.UpdatedBy != bob) {
				err = fmt.Errorf("an update in a savepoint as bob recorded updatedBy %v", edited.UpdatedBy)
			}
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	// A soft delete stamps deletedBy with the actor.
	if err := db.Note.DeleteOne(AsSystem(ctx), *note.Id); err != nil {
		t.Fatalf("delete as the system: %v", err)
	}
	var deletedBy string
	if err := pool.QueryRow(ctx, "SELECT deleted_by::text FROM note WHERE id = $1", note.Id.ToUUID()).Scan(&deletedBy); err != nil || deletedBy != "3f2c1a9e-7b4d-4e8a-9c61-0d5e2f7a8b10" {
		t.Fatalf("deleted_by is %q (%v)", deletedBy, err)
	}
}
`
