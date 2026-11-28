package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// audienceQueriesORMCases are the cases audienceQueriesORMTest runs over
// fixture-audience-queries-db, by name, as audience_queries.test.ts's are
// the TypeScript ORM's (tsgen).
var audienceQueriesORMCases = []string{
	"two callers get each their own rows of one query, an anonymous one everyone's and the system every one",
	"an agent run sees what it or whom it acts for may, and its access lets it read",
	"a join holds each table it marks to the caller",
	"a paged query numbers the audience after its page key, page by page",
	"a query over a projection's view keeps to its source's audience, in a transaction too",
}

// TestAudienceQueriesOnPostgres holds the generated Go ORM's declared
// queries to the audience of the caller (#735): each :audience(d) mark of
// fixture-audience-queries-db's queries is filled by queryAudience for the
// context's principal, so two users get each their own rows of one query,
// an anonymous caller everyone's, an agent run what it and whom it acts
// for may see and its access lets it read, and a context outside any
// operation, or AsSystem's, every row. It generates the fixture's Go types,
// ORM and DDL and runs audienceQueriesORMTest on the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL; every case must pass.
func TestAudienceQueriesOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated ORM's audience queries in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated ORM's audience queries against Postgres")
	}
	ormDir := generateORMModule(t, "fixture-audience-queries-db")
	if err := os.WriteFile(filepath.Join(ormDir, "audience_queries_test.go"), []byte(audienceQueriesORMTest), 0o644); err != nil {
		t.Fatalf("write the test: %v", err)
	}
	out := runORMModule(t, ormDir)
	for _, name := range audienceQueriesORMCases {
		if want := "--- PASS: TestAudienceQueries/" + strings.ReplaceAll(name, " ", "_"); !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q:\n%s", name, out)
		}
	}
}

// audienceQueriesORMTest is the generated module's test.
const audienceQueriesORMTest = `package orm

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-audience-queries-db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

func TestAudienceQueries(t *testing.T) {
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
	schema := fmt.Sprintf("ormgen_audience_queries_%d", time.Now().UnixNano())
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

	id := func(u uuid.UUID) types.IdentityUUID {
		parsed, err := types.ParseIdentityUUID(u.String())
		if err != nil {
			t.Fatalf("an id: %v", err)
		}
		return parsed
	}
	alice, bob, agentRun := uuid.New(), uuid.New(), uuid.New()
	as := func(who uuid.UUID) context.Context {
		return WithPrincipal(WithUserID(background, id(who)), access.Principal{Subject: who.String(), Kind: access.KindUser})
	}
	folder := func(owner uuid.UUID, name, audience string) types.IdentityUUID {
		created, err := db.Folder.CreateOne(as(owner), &types.Folder{Name: name, Owner: id(owner), Audience: audience})
		if err != nil {
			t.Fatalf("create folder %s: %v", name, err)
		}
		return *created.Id
	}
	shared := folder(alice, "shared", "everyone")
	alices := folder(alice, "alice's", "private")
	bobs := folder(bob, "bob's", "private")
	document := func(by uuid.UUID, row *types.Document) {
		if _, err := db.Document.CreateOne(as(by), row); err != nil {
			t.Fatalf("create document %s: %v", row.Title, err)
		}
	}
	document(alice, &types.Document{Title: "Alpha", Folder: &types.Folder{Id: &shared}})
	document(alice, &types.Document{Title: "Bravo", Folder: &types.Folder{Id: &alices}})
	document(bob, &types.Document{Title: "Charlie", Folder: &types.Folder{Id: &bobs}})
	document(bob, &types.Document{Title: "Delta", Audience: "named", AudienceNamed: []types.IdentityUUID{id(alice)}})
	document(alice, &types.Document{Title: "Echo", Folder: &types.Folder{Id: &shared}, AgentAccess: "none"})

	titles := func(rows []types.DocumentRow, err error) []string {
		t.Helper()
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		out := []string{}
		for _, row := range rows {
			out = append(out, row.Title)
		}
		return out
	}
	expect := func(t *testing.T, got []string, want ...string) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	all := types.DocumentSearch{}

	t.Run("two callers get each their own rows of one query, an anonymous one everyone's and the system every one", func(t *testing.T) {
		expect(t, titles(db.Queries.VisibleDocuments(as(alice), all)), "Alpha", "Bravo", "Delta", "Echo")
		expect(t, titles(db.Queries.VisibleDocuments(as(bob), all)), "Alpha", "Charlie", "Delta", "Echo")
		expect(t, titles(db.Queries.VisibleDocuments(WithPrincipal(background, access.Principal{}), all)), "Alpha", "Echo")
		expect(t, titles(db.Queries.VisibleDocuments(background, all)), "Alpha", "Bravo", "Charlie", "Delta", "Echo")
		expect(t, titles(db.Queries.VisibleDocuments(AsSystem(as(bob)), all)), "Alpha", "Bravo", "Charlie", "Delta", "Echo")
		search := "a"
		expect(t, titles(db.Queries.VisibleDocuments(as(bob), types.DocumentSearch{Search: search})), "Alpha", "Charlie", "Delta")
	})

	t.Run("an agent run sees what it or whom it acts for may, and its access lets it read", func(t *testing.T) {
		agent := WithPrincipal(background, access.Principal{Subject: agentRun.String(), Kind: access.KindAgent, ActingFor: &access.ActingFor{Kind: access.KindUser, Subject: alice.String()}})
		expect(t, titles(db.Queries.VisibleDocuments(agent, all)), "Alpha", "Bravo", "Delta")
	})

	t.Run("a join holds each table it marks to the caller", func(t *testing.T) {
		rows, err := db.Queries.FolderDocuments(as(alice), types.FolderParams{Folder: shared})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || rows[0].Title != "Alpha" || rows[0].FolderName != "shared" || rows[1].Title != "Echo" {
			t.Fatalf("alice's shared folder: %+v", rows)
		}
		if rows, err := db.Queries.FolderDocuments(as(alice), types.FolderParams{Folder: bobs}); err != nil || len(rows) != 0 {
			t.Fatalf("bob's folder for alice: %+v, %v", rows, err)
		}
		if rows, err := db.Queries.FolderDocuments(as(bob), types.FolderParams{Folder: bobs}); err != nil || len(rows) != 1 || rows[0].Title != "Charlie" {
			t.Fatalf("bob's folder for bob: %+v, %v", rows, err)
		}
		if rows, err := db.Queries.FolderDocuments(background, types.FolderParams{Folder: alices}); err != nil || len(rows) != 1 || rows[0].Title != "Bravo" {
			t.Fatalf("alice's folder outside an operation: %+v, %v", rows, err)
		}
	})

	t.Run("a paged query numbers the audience after its page key, page by page", func(t *testing.T) {
		first, err := db.Queries.DocumentPage(as(bob), all, &QueryPageOptions[DocumentPagePageKey]{Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		expect(t, titles(first.Rows, nil), "Alpha", "Charlie")
		if first.Next == nil {
			t.Fatal("the first page has no next key")
		}
		second, err := db.Queries.DocumentPage(as(bob), all, &QueryPageOptions[DocumentPagePageKey]{Limit: 2, After: first.Next})
		if err != nil {
			t.Fatal(err)
		}
		expect(t, titles(second.Rows, nil), "Delta", "Echo")
		if second.Next != nil {
			t.Fatalf("the last page has a next key: %+v", second.Next)
		}
		every, err := db.Queries.DocumentPage(background, all, &QueryPageOptions[DocumentPagePageKey]{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		expect(t, titles(every.Rows, nil), "Alpha", "Bravo", "Charlie", "Delta", "Echo")
	})

	t.Run("a query over a projection's view keeps to its source's audience, in a transaction too", func(t *testing.T) {
		expect(t, titles(db.Queries.ListedDocuments(as(alice), all)), "Alpha", "Bravo", "Delta", "Echo")
		var inTx []string
		if err := db.Transaction(as(bob), func(tx TxInterface) error {
			rows, err := tx.GetQueries().ListedDocuments(as(bob), all)
			inTx = titles(rows, err)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		expect(t, inTx, "Alpha", "Charlie", "Delta", "Echo")
		expect(t, titles(db.Queries.ListedDocuments(WithPrincipal(background, access.Principal{}), all)), "Alpha", "Echo")
	})
}
`
