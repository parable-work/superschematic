package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// audienceQueueORMCases are the cases audienceQueueORMTest runs over
// fixture-audience-queue-db, by name, as audienceQueueCases are the
// TypeScript repositories' (tsgen) and audience_queue_rules.rs the Rust
// repositories'.
var audienceQueueORMCases = []string{
	"a folder's change of audience commits without its pages, enqueued once on the folder's queue",
	"a page read right after the worker drains the queue shows the folder's new audience",
}

// TestAudienceQueueOnPostgres holds the generated Go ORM to an audience
// resolved through a queue (D156, #734): a folder's change of audience
// enqueues it once and leaves its pages as they were, and a page read right
// after Database.ResolveAudience drains the queue shows the folder's new
// audience. It generates fixture-audience-queue-db's Go types, ORM and DDL
// into a temporary tree and runs audienceQueueORMTest on the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL; every case must pass. It skips
// without the database, and in -short mode.
func TestAudienceQueueOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated ORM's audience queue in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated ORM's audience queue against Postgres")
	}
	ormDir := generateORMModule(t, "fixture-audience-queue-db")
	if _, err := os.Stat(filepath.Join(ormDir, "audience_resolve.go")); err != nil {
		t.Fatalf("the generated ORM has no audience_resolve.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ormDir, "audience_queue_test.go"), []byte(audienceQueueORMTest), 0o644); err != nil {
		t.Fatalf("write the test: %v", err)
	}
	out := runORMModule(t, ormDir)
	for _, name := range audienceQueueORMCases {
		if want := "--- PASS: TestAudienceQueue/" + strings.ReplaceAll(name, " ", "_"); !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q:\n%s", name, out)
		}
	}
}

// audienceQueueORMTest is the generated module's test.
const audienceQueueORMTest = `package orm

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-audience-queue-db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// pages is how many pages a folder holds: more than one batch of
// ResolveAudience's.
const pages = 1234

func TestAudienceQueue(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run against Postgres")
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
	schema := fmt.Sprintf("ormgen_audience_queue_%d", time.Now().UnixNano())
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
	alice, bob := uuid.New(), uuid.New()
	ctx := WithUserID(background, id(t, alice))
	as := func(who uuid.UUID) context.Context {
		return WithPrincipal(ctx, access.Principal{Subject: who.String(), Kind: access.KindUser})
	}
	// folderOfPages is a folder of alice's, everyone's, holding pages
	// pages that inherit its audience, written in one statement.
	folderOfPages := func(t *testing.T) (types.IdentityUUID, string) {
		t.Helper()
		folder, err := db.Folder.CreateOne(as(alice), &types.Folder{Name: "a large folder", Owner: id(t, alice), Audience: "everyone", AgentAccess: "followRole"})
		if err != nil {
			t.Fatalf("create the folder: %v", err)
		}
		var text string
		if err := pool.QueryRow(ctx, "SELECT id::text FROM folder ORDER BY created_at DESC LIMIT 1").Scan(&text); err != nil {
			t.Fatalf("read the folder's key: %v", err)
		}
		if _, err := pool.Exec(ctx, "INSERT INTO page (folder_id, title, audience, created_at, created_by) SELECT $1::uuid, 'page ' || n, 'inherit', now(), $2::uuid FROM generate_series(1, $3::int) AS n", text, alice.String(), pages); err != nil {
			t.Fatalf("write the pages: %v", err)
		}
		return *folder.Id, text
	}
	visible := func(t *testing.T, who uuid.UUID, folder types.IdentityUUID) int {
		t.Helper()
		folderID := folder
		_, total, err := db.Page.FindMany(as(who), &PageFilter{FolderID: &UUIDFilter{Eq: &folderID}}, &PageFindOptions{Fields: PageFields{Id: true}, Limit: 1})
		if err != nil {
			t.Fatalf("read the pages: %v", err)
		}
		return total
	}
	setAudience := func(t *testing.T, folder types.IdentityUUID, audience string) {
		t.Helper()
		if _, err := db.Folder.UpdateOne(as(alice), folder, &FolderUpdate{Audience: &audience}); err != nil {
			t.Fatalf("set the folder's audience to %s: %v", audience, err)
		}
	}
	messages := func(t *testing.T, folder string) []string {
		t.Helper()
		rows, err := pool.Query(ctx, "SELECT child_table FROM audience_resolving_queue WHERE parent_key = $1", folder)
		if err != nil {
			t.Fatalf("read the queue: %v", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var table string
			if err := rows.Scan(&table); err != nil {
				t.Fatal(err)
			}
			out = append(out, table)
		}
		return out
	}
	// drain is what a worker of the queue does: each ready message
	// resolved, then deleted.
	drain := func(t *testing.T) int64 {
		t.Helper()
		rows, err := pool.Query(ctx, "SELECT id::text, child_table, parent_key FROM audience_resolving_queue WHERE state = 'ready'")
		if err != nil {
			t.Fatalf("claim the queue: %v", err)
		}
		type message struct{ id, table, parent string }
		var claimed []message
		for rows.Next() {
			var m message
			if err := rows.Scan(&m.id, &m.table, &m.parent); err != nil {
				t.Fatal(err)
			}
			claimed = append(claimed, m)
		}
		rows.Close()
		var resolved int64
		for _, m := range claimed {
			n, err := db.ResolveAudience(ctx, m.table, m.parent)
			if err != nil {
				t.Fatalf("resolve %s's %s: %v", m.parent, m.table, err)
			}
			resolved += n
			if _, err := pool.Exec(ctx, "DELETE FROM audience_resolving_queue WHERE id = $1::uuid", m.id); err != nil {
				t.Fatal(err)
			}
		}
		return resolved
	}

	t.Run("a folder's change of audience commits without its pages, enqueued once on the folder's queue", func(t *testing.T) {
		folder, text := folderOfPages(t)
		if n := visible(t, bob, folder); n != pages {
			t.Fatalf("bob sees %d pages of an open folder, want %d", n, pages)
		}
		setAudience(t, folder, "private")
		setAudience(t, folder, "everyone")
		setAudience(t, folder, "private")
		if n := visible(t, bob, folder); n != pages {
			t.Errorf("before the worker runs bob sees %d pages, want the %d he saw", n, pages)
		}
		if got := messages(t, text); len(got) != 1 || got[0] != "Page" {
			t.Errorf("the queue holds %v for the folder, want one message of Page", got)
		}
		drain(t)
	})

	t.Run("a page read right after the worker drains the queue shows the folder's new audience", func(t *testing.T) {
		folder, text := folderOfPages(t)
		setAudience(t, folder, "private")
		if n := drain(t); n != pages {
			t.Errorf("the worker resolved %d pages, want %d", n, pages)
		}
		if got := messages(t, text); len(got) != 0 {
			t.Errorf("the queue still holds %v", got)
		}
		if n := visible(t, bob, folder); n != 0 {
			t.Errorf("bob sees %d pages of a private folder, want none", n)
		}
		if n := visible(t, alice, folder); n != pages {
			t.Errorf("alice sees %d pages of her folder, want %d", n, pages)
		}
		setAudience(t, folder, "everyone")
		if n := visible(t, bob, folder); n != 0 {
			t.Errorf("before the worker runs bob sees %d pages, want none", n)
		}
		drain(t)
		if n := visible(t, bob, folder); n != pages {
			t.Errorf("bob sees %d pages once the folder is everyone's again, want %d", n, pages)
		}
	})
}
`
