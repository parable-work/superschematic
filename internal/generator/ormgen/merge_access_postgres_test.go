package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mergeAccessORMCases are the cases mergeAccessORMTest runs over
// fixture-merge-access-db, by name (D147, D156, D180), as merge_rules.rs
// holds the Rust repositories to them (D181).
var mergeAccessORMCases = []string{
	"a user who may not write one of the two labels merges neither into the other, and writes nothing",
	"a party whose grant reaches one of the two labels merges neither into the other, and writes nothing",
	"a user who may write both labels merges one into the other",
}

// TestMergeIntoKeepsToTheCallerOnPostgres holds the generated Go ORM's
// MergeInto to per-record access (D156, D180): under WithPrincipal, a merge
// of two rows finds only the rows its caller may write, so a merge of a
// row the caller may not write, or into one, is ErrNotFound before any
// statement runs, as UpdateOne and DeleteOne are, and writes nothing. A
// user is held to the labels' audience, and a party to the label its grant
// names. It loads fixture-merge-access-db, generates its Go types, ORM and
// DDL into a temporary tree, and runs mergeAccessORMTest on the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL; every case must pass. It skips
// without the database and in -short mode.
func TestMergeIntoKeepsToTheCallerOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated ORM's merge under per-record access in -short mode")
	}
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated ORM's merges under per-record access against Postgres")
	}
	ormDir := generateORMModule(t, "fixture-merge-access-db")
	if err := os.WriteFile(filepath.Join(ormDir, "merge_access_test.go"), []byte(mergeAccessORMTest), 0o644); err != nil {
		t.Fatalf("write the merge test: %v", err)
	}
	out := runORMModule(t, ormDir)
	for _, name := range mergeAccessORMCases {
		if want := "--- PASS: TestMergeIntoKeepsToTheCaller/" + strings.ReplaceAll(name, " ", "_"); !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q:\n%s", name, out)
		}
	}
}

// mergeAccessORMTest is the generated module's test: labels written with
// no principal, which reads and writes every row, then merged under one,
// with the rows read back past the repositories with SQL.
const mergeAccessORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-merge-access-db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

func TestMergeIntoKeepsToTheCaller(t *testing.T) {
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
	schema := fmt.Sprintf("ormgen_merge_access_%d", time.Now().UnixNano())
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

	id := func(t *testing.T, u uuid.UUID) types.IdentityUUID {
		t.Helper()
		parsed, err := types.ParseIdentityUUID(u.String())
		if err != nil {
			t.Fatalf("an id: %v", err)
		}
		return parsed
	}
	ada, bob, party := uuid.New(), uuid.New(), uuid.New()
	// No principal: the writes that set the rows up read and write every
	// row, as a job's do.
	ctx := WithUserID(background, id(t, ada))
	asUser := func(who uuid.UUID) context.Context {
		return WithPrincipal(ctx, access.Principal{Subject: who.String(), Kind: access.KindUser})
	}
	label := func(t *testing.T, name string, owner uuid.UUID, audience string) types.IdentityUUID {
		t.Helper()
		row, err := db.Label.CreateOne(ctx, &types.Label{Name: name, Owner: id(t, owner), Audience: audience, AgentAccess: "readOnly"})
		if err != nil {
			t.Fatalf("create the label %s: %v", name, err)
		}
		return *row.Id
	}
	note := func(t *testing.T, on types.IdentityUUID) {
		t.Helper()
		if _, err := pool.Exec(ctx, "INSERT INTO label_note (id, label_id, text) VALUES (gen_random_uuid(), $1, 'a note')", on.ToUUID()); err != nil {
			t.Fatalf("a note: %v", err)
		}
	}
	notes := func(t *testing.T, on types.IdentityUUID) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM label_note WHERE label_id = $1", on.ToUUID()).Scan(&n); err != nil {
			t.Fatalf("count the notes: %v", err)
		}
		return n
	}
	held := func(t *testing.T, of types.IdentityUUID) bool {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM label WHERE id = $1", of.ToUUID()).Scan(&n); err != nil {
			t.Fatalf("count the labels: %v", err)
		}
		return n == 1
	}
	refused := func(t *testing.T, caller context.Context, from, into types.IdentityUUID) {
		t.Helper()
		if _, err := db.Label.MergeInto(caller, from, into); !errors.Is(err, ErrNotFound) {
			t.Fatalf("merge %v into %v: %v, want ErrNotFound", from, into, err)
		}
	}

	t.Run("a user who may not write one of the two labels merges neither into the other, and writes nothing", func(t *testing.T) {
		mine, theirs := label(t, "bug", ada, "private"), label(t, "defect", bob, "private")
		note(t, theirs)
		refused(t, asUser(ada), theirs, mine)
		refused(t, asUser(ada), mine, theirs)
		if !held(t, mine) || !held(t, theirs) || notes(t, theirs) != 1 || notes(t, mine) != 0 {
			t.Fatal("a merge the caller may not make wrote")
		}
	})

	t.Run("a party whose grant reaches one of the two labels merges neither into the other, and writes nothing", func(t *testing.T) {
		reached, other := label(t, "open", ada, "everyone"), label(t, "shut", ada, "everyone")
		note(t, reached)
		asParty := WithPrincipal(ctx, access.Principal{
			Subject: party.String(),
			Kind:    access.KindParty,
			Grants:  []access.GrantReach{{ID: "g1", TargetType: "Label", TargetID: reached.String()}},
		})
		refused(t, asParty, reached, other)
		refused(t, asParty, other, reached)
		if !held(t, reached) || !held(t, other) || notes(t, reached) != 1 || notes(t, other) != 0 {
			t.Fatal("a merge the caller may not make wrote")
		}
	})

	t.Run("a user who may write both labels merges one into the other", func(t *testing.T) {
		stays, goes := label(t, "crash", ada, "private"), label(t, "fault", ada, "private")
		note(t, goes)
		result, err := db.Label.MergeInto(asUser(ada), goes, stays)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if result.Repointed["LabelNote"] != 1 || held(t, goes) || notes(t, stays) != 1 {
			t.Fatalf("the merge repointed %v, and left the label merged away: %v", result.Repointed, held(t, goes))
		}
	})
}
`
