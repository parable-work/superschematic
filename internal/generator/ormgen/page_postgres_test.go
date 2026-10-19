package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedFindPageOnPostgres generates the ORM module for fixture-db,
// then builds, vets and tests it. Its generated test, against the Postgres
// at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (it skips without the
// database), pages the tenants through FindPage (D60, amended): by text, a
// citext column among them, by a number and by a time, each ascending and
// descending, across pages whose keys go through JSON as a cursor holds
// them; ties of one seatCount break by id in the sort's direction; a field
// filter and a search sit beside the keyset; a soft-deleted member is left
// out of every page and kept with IncludeDeleted; a page hydrates a hasMany
// relation as FindMany does, and reads in a transaction. A key of another
// sort, of two fields or without an id is ErrPageKey, and a sort field the
// table has no column for is refused before any SQL runs.
func TestGeneratedFindPageOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	ormDir := generateORMModule(t, "fixture-db")
	if err := os.WriteFile(filepath.Join(ormDir, "find_page_test.go"), []byte(findPageORMTest), 0o644); err != nil {
		t.Fatalf("write find page test: %v", err)
	}
	out := runORMModule(t, ormDir)
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" && !strings.Contains(out, "--- PASS: TestFindPageOnPostgres") {
		t.Fatalf("the generated ORM did not run TestFindPageOnPostgres:\n%s", out)
	}
}

// TestGeneratedFindPageKeysOnPostgres generates the ORM module for
// sortRulesSchema, whose key is entryId, and pages its entries on Postgres
// by an integer, a number, a date, a UUID, text and the key, each value
// bound as its column's type: ranks 2, 10 and 1000 read in that order, as
// they would not as text.
func TestGeneratedFindPageKeysOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	ormDir := generateORMModuleAt(t, sortRulesService(t), "sort-rules")
	if err := os.WriteFile(filepath.Join(ormDir, "find_page_keys_test.go"), []byte(findPageKeysORMTest), 0o644); err != nil {
		t.Fatalf("write find page keys test: %v", err)
	}
	out := runORMModule(t, ormDir)
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" && !strings.Contains(out, "--- PASS: TestFindPageKeysOnPostgres") {
		t.Fatalf("the generated ORM did not run TestFindPageKeysOnPostgres:\n%s", out)
	}
}

// pageTestSetup is the start of each generated test: a schema of its own on
// the Postgres the environment names, create.sql applied, and db, the ORM
// over it. %s names the schema's prefix.
const pageTestSetup = `
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to page a table on Postgres")
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
	schema := fmt.Sprintf("PREFIX_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	// pgcrypto, citext and pg_trgm, which the tables take, are in public.
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
`

var findPageORMTest = `package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-db"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFindPageOnPostgres(t *testing.T) {` + strings.Replace(pageTestSetup, "PREFIX", "find_page_orm", 1) + `
	// Five tenants. Three hold five seats, by ids that put Easel, Drill and
	// Anvil in that order; the ids put Bellows before Chisel, as their
	// seats do not. Each was created a day apart, at a time with
	// microseconds, which a key keeps.
	created := func(day int) time.Time {
		return time.Date(2026, 10, day, 12, 0, 0, day*1000, time.UTC)
	}
	for _, row := range []struct {
		id    string
		name  string
		seats float64
		day   int
	}{
		{"00000000-0000-0000-0000-000000000003", "Anvil", 5, 5},
		{"00000000-0000-0000-0000-000000000004", "Bellows", 20, 4},
		{"00000000-0000-0000-0000-000000000005", "Chisel", 10, 3},
		{"00000000-0000-0000-0000-000000000002", "Drill", 5, 2},
		{"00000000-0000-0000-0000-000000000001", "Easel", 5, 1},
	} {
		if _, err := pool.Exec(ctx,
			"INSERT INTO tenant (id, name, slug, email, status, is_active, seat_count, metadata, created_at) VALUES ($1, $2, $3, $4, 'active', true, $5, '{}', $6)",
			row.id, row.name, strings.ToLower(row.name), strings.ToLower(row.name)+"@example.com", row.seats, created(row.day)); err != nil {
			t.Fatalf("insert %s: %v", row.name, err)
		}
	}
	text := func(s string) *string { return &s }
	number := func(n float64) *float64 { return &n }
	names := func(rows []*types.Tenant) string {
		got := make([]string, 0, len(rows))
		for _, row := range rows {
			got = append(got, string(row.Name))
		}
		return strings.Join(got, " ")
	}

	// walk reads every page of the tenants filter finds in sort's order,
	// limit a page, each page's Next through JSON as a cursor holds it, and
	// selects the names alone: FindPage reads the sort's column and the id
	// itself. It answers each page's names, in brackets.
	walk := func(filter *TenantFilter, sort string, limit int) string {
		t.Helper()
		parsed, err := ParseTenantSort(sort)
		if err != nil {
			t.Fatalf("ParseTenantSort(%q): %v", sort, err)
		}
		var pages []string
		var after *TenantPageKey
		for len(pages) < 10 {
			page, err := db.Tenant.FindPage(ctx, filter, &TenantPageOptions{
				Fields: TenantFields{Name: true}, Sort: parsed, After: after, Limit: limit,
			})
			if err != nil {
				t.Fatalf("FindPage by %s after %+v: %v", sort, after, err)
			}
			pages = append(pages, "["+names(page.Rows)+"]")
			if page.Next == nil {
				return strings.Join(pages, " ")
			}
			cursor, err := json.Marshal(page.Next)
			if err != nil {
				t.Fatalf("marshal the key %+v: %v", page.Next, err)
			}
			after = &TenantPageKey{}
			if err := json.Unmarshal(cursor, after); err != nil {
				t.Fatalf("unmarshal the key %s: %v", cursor, err)
			}
		}
		t.Fatalf("FindPage by %s read more than 10 pages: %v", sort, pages)
		return ""
	}

	for _, tc := range []struct {
		name   string
		filter *TenantFilter
		sort   string
		limit  int
		want   string
	}{
		{"by name", nil, "name", 2, "[Anvil Bellows] [Chisel Drill] [Easel]"},
		{"by name, descending", nil, "-name", 2, "[Easel Drill] [Chisel Bellows] [Anvil]"},
		{"by slug, a citext column", nil, "slug", 3, "[Anvil Bellows Chisel] [Drill Easel]"},
		{"by email, descending", nil, "-email", 4, "[Easel Drill Chisel Bellows] [Anvil]"},
		{"by seatCount, ties by id", nil, "seatCount", 2, "[Easel Drill] [Anvil Chisel] [Bellows]"},
		{"by seatCount, descending, ties by id descending", nil, "-seatCount", 2, "[Bellows Chisel] [Anvil Drill] [Easel]"},
		{"by createdAt", nil, "createdAt", 2, "[Easel Drill] [Chisel Bellows] [Anvil]"},
		{"by createdAt, descending", nil, "-createdAt", 3, "[Anvil Bellows Chisel] [Drill Easel]"},
		{"by id", nil, "id", 2, "[Easel Drill] [Anvil Bellows] [Chisel]"},
		{"by id, descending", nil, "-id", 2, "[Chisel Bellows] [Anvil Drill] [Easel]"},
		{"a page of every row has no next", nil, "name", 5, "[Anvil Bellows Chisel Drill Easel]"},
		{"a limit of 0 reads 100", nil, "-seatCount", 0, "[Bellows Chisel Anvil Drill Easel]"},
		{"a filter beside the keyset", &TenantFilter{SeatCount: &FloatFilter{Eq: number(5)}}, "-name", 2, "[Easel Drill] [Anvil]"},
		{"a search beside the keyset", &TenantFilter{SearchText: text("EL")}, "name", 2, "[Bellows Chisel] [Easel]"},
		{"a search and a filter", &TenantFilter{SearchText: text("el"), SeatCount: &FloatFilter{Lte: number(10)}}, "-seatCount", 1, "[Chisel] [Easel]"},
		{"a filter no row meets", &TenantFilter{SeatCount: &FloatFilter{Gt: number(20)}}, "name", 2, "[]"},
	} {
		if got := walk(tc.filter, tc.sort, tc.limit); got != tc.want {
			t.Errorf("%s: pages %s, want %s", tc.name, got, tc.want)
		}
	}

	// A key holds its sort field and the id under their schema names, and
	// a time to its microsecond.
	first, err := db.Tenant.FindPage(ctx, nil, &TenantPageOptions{Fields: TenantFields{}.All(), Sort: TenantSort{Field: TenantSortByName}, Limit: 2})
	if err != nil {
		t.Fatalf("the first page by name: %v", err)
	}
	// The id is in the UUID's JSON form, base62.
	if cursor, _ := json.Marshal(first.Next); string(cursor) != ` + "`" + `{"name":"Bellows","id":"4"}` + "`" + ` || first.Next.Id.ToUUID().String() != "00000000-0000-0000-0000-000000000004" {
		t.Errorf("the key after the first page by name = %s", cursor)
	}
	anvil := *first.Rows[0].Id
	byTime, err := db.Tenant.FindPage(ctx, nil, &TenantPageOptions{Fields: TenantFields{Name: true}, Sort: TenantSort{Field: TenantSortByCreatedAt}, Limit: 1})
	if err != nil {
		t.Fatalf("the first page by createdAt: %v", err)
	}
	if byTime.Next == nil || byTime.Next.CreatedAt == nil || !time.Time(*byTime.Next.CreatedAt).Equal(created(1)) || byTime.Next.Name != nil {
		t.Errorf("the key after the first page by createdAt = %+v, want createdAt %s alone", byTime.Next, created(1))
	}

	// A key of another sort, of two fields or of none, or without an id,
	// names no row of the page's order.
	seats := 5.0
	twoFields := *first.Next
	twoFields.SeatCount = &seats
	noField := TenantPageKey{Id: first.Next.Id}
	noID := *first.Next
	noID.Id = types.IdentityUUID{}
	for _, tc := range []struct {
		name  string
		sort  TenantSort
		after *TenantPageKey
	}{
		{"a key by name in an order by seatCount", TenantSort{Field: TenantSortBySeatCount}, first.Next},
		{"a key by name in an order by id", TenantSort{Desc: true}, first.Next},
		{"a key of two fields", TenantSort{Field: TenantSortByName}, &twoFields},
		{"a key of the id alone in an order by name", TenantSort{Field: TenantSortByName}, &noField},
		{"a key without an id", TenantSort{Field: TenantSortByName}, &noID},
	} {
		_, err := db.Tenant.FindPage(ctx, nil, &TenantPageOptions{Fields: TenantFields{Name: true}, Sort: tc.sort, After: tc.after, Limit: 2})
		if !errors.Is(err, ErrPageKey) {
			t.Errorf("%s: FindPage = %v, want ErrPageKey", tc.name, err)
		}
	}
	// The id's own key is the id alone.
	byID, err := db.Tenant.FindPage(ctx, nil, &TenantPageOptions{Fields: TenantFields{Name: true}, After: &noField, Limit: 1})
	if err != nil || names(byID.Rows) != "Chisel" {
		t.Errorf("the page by id after Bellows' id = %v, %v; want Chisel", byID, err)
	}

	// A sort is a field of the table's, never SQL: another text is refused
	// before a query runs, and the table is as it was.
	for _, field := range []TenantSortField{"status", "is_active", "name; DROP TABLE tenant; --"} {
		_, err := db.Tenant.FindPage(ctx, nil, &TenantPageOptions{Fields: TenantFields{Name: true}, Sort: TenantSort{Field: field}})
		if err == nil || errors.Is(err, ErrPageKey) || !strings.Contains(err.Error(), "Tenant sorts by id, createdAt, name, slug, email and seatCount") {
			t.Errorf("FindPage by %q = %v, want the refusal that lists Tenant's sort fields", field, err)
		}
	}
	if all := walk(nil, "name", 10); all != "[Anvil Bellows Chisel Drill Easel]" {
		t.Errorf("the tenants after the refused sorts: %s", all)
	}
	for _, opts := range []*TenantPageOptions{nil, {}, {Fields: TenantFields{Name: true}, Limit: -1}} {
		if _, err := db.Tenant.FindPage(ctx, nil, opts); err == nil {
			t.Errorf("FindPage(%+v) answered a page, want an error", opts)
		}
	}

	// ParseTenantSort reads a page request's sort and refuses any other.
	if got, err := ParseTenantSort("-seatCount"); err != nil || got != (TenantSort{Field: TenantSortBySeatCount, Desc: true}) {
		t.Errorf("ParseTenantSort(-seatCount) = %+v, %v", got, err)
	}
	if got, err := ParseTenantSort("id"); err != nil || got != (TenantSort{Field: TenantSortById}) {
		t.Errorf("ParseTenantSort(id) = %+v, %v", got, err)
	}
	for _, refused := range []string{"status", "isActive", "metadata", "updatedAt", "", "-", "--name", "Name", "seat_count"} {
		if got, err := ParseTenantSort(refused); err == nil || got != (TenantSort{}) {
			t.Errorf("ParseTenantSort(%q) = %+v, %v; want an error", refused, got, err)
		} else if want := fmt.Sprintf("orm: Tenant sorts by id, createdAt, name, slug, email and seatCount, not %q", refused); err.Error() != want {
			t.Errorf("ParseTenantSort(%q) = %q, want %q", refused, err, want)
		}
	}

	// Anvil's members, one deleted: pages leave it out, and keep it with
	// IncludeDeleted.
	for _, member := range []struct {
		name    string
		day     int
		deleted bool
	}{{"Ann", 1, false}, {"Bo", 2, true}, {"Cy", 3, false}} {
		var deletedAt *time.Time
		if member.deleted {
			at := created(4)
			deletedAt = &at
		}
		if _, err := pool.Exec(ctx, "INSERT INTO tenant_user (tenant_id, display_name, created_at, deleted_at) VALUES ($1, $2, $3, $4)",
			anvil.ToUUID(), member.name, created(member.day), deletedAt); err != nil {
			t.Fatalf("insert the member %s: %v", member.name, err)
		}
	}
	members := func(desc, includeDeleted bool) string {
		t.Helper()
		var pages []string
		var after *TenantUserPageKey
		for len(pages) < 10 {
			page, err := db.TenantUser.FindPage(ctx, nil, &TenantUserPageOptions{
				Fields: TenantUserFields{DisplayName: true}, Sort: TenantUserSort{Field: TenantUserSortByCreatedAt, Desc: desc},
				After: after, Limit: 1, IncludeDeleted: includeDeleted,
			})
			if err != nil {
				t.Fatalf("the members' page after %+v: %v", after, err)
			}
			for _, row := range page.Rows {
				pages = append(pages, row.DisplayName)
			}
			if page.Next == nil {
				return strings.Join(pages, " ")
			}
			after = page.Next
		}
		t.Fatalf("the members read more than 10 pages: %v", pages)
		return ""
	}
	if got := members(false, false); got != "Ann Cy" {
		t.Errorf("the members = %s, want Ann Cy", got)
	}
	if got := members(true, false); got != "Cy Ann" {
		t.Errorf("the members, descending = %s, want Cy Ann", got)
	}
	if got := members(false, true); got != "Ann Bo Cy" {
		t.Errorf("the members with the deleted = %s, want Ann Bo Cy", got)
	}

	// A page hydrates a hasMany relation as FindMany does, here with a
	// filter, and reads in a transaction as on the pool.
	err = db.Transaction(ctx, func(tx TxInterface) error {
		page, err := tx.GetTenantRepository().FindPage(ctx, &TenantFilter{SeatCount: &FloatFilter{Eq: number(5)}}, &TenantPageOptions{
			Fields: TenantFields{Name: true, UsersNested: &TenantUserFields{DisplayName: true}},
			Sort:   TenantSort{Field: TenantSortByName},
			Limit:  1,
		})
		if err != nil {
			return err
		}
		if names(page.Rows) != "Anvil" || page.Next == nil || page.Next.Name == nil || *page.Next.Name != "Anvil" {
			return fmt.Errorf("the first page of five seats = %s, next %+v; want Anvil and its key", names(page.Rows), page.Next)
		}
		var users []string
		for _, user := range page.Rows[0].Users {
			users = append(users, user.DisplayName)
		}
		if got := strings.Join(users, " "); got != "Ann Cy" && got != "Cy Ann" {
			return fmt.Errorf("Anvil's users = %q, want Ann and Cy", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("the page in a transaction: %v", err)
	}
}
`

var findPageKeysORMTest = `package orm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFindPageKeysOnPostgres(t *testing.T) {` + strings.Replace(pageTestSetup, "PREFIX", "find_page_keys_orm", 1) + `
	for _, row := range []struct {
		id, title string
		rank      int64
		score     float64
		due       string
		owner     string
	}{
		{"00000000-0000-0000-0000-000000000001", "b", 10, 1.5, "2026-10-03", "00000000-0000-0000-0000-000000000004"},
		{"00000000-0000-0000-0000-000000000002", "a", 2, 2.5, "2026-10-01", "00000000-0000-0000-0000-000000000001"},
		{"00000000-0000-0000-0000-000000000003", "c", 10, 0.5, "2026-10-02", "00000000-0000-0000-0000-000000000003"},
		{"00000000-0000-0000-0000-000000000004", "d", 1000, 2.5, "2026-10-04", "00000000-0000-0000-0000-000000000002"},
	} {
		if _, err := pool.Exec(ctx,
			"INSERT INTO entry (entry_id, title, token, rank, score, due, owner_id, done, mood, at, tags, extra, created_by) VALUES ($1, $2, 'secret', $3, $4, $5, $6, false, 'calm', point(0, 0), '{}', '{}', $6)",
			row.id, row.title, row.rank, row.score, row.due, row.owner); err != nil {
			t.Fatalf("insert %s: %v", row.title, err)
		}
	}

	walk := func(sort string) string {
		t.Helper()
		parsed, err := ParseEntrySort(sort)
		if err != nil {
			t.Fatalf("ParseEntrySort(%q): %v", sort, err)
		}
		var pages []string
		var after *EntryPageKey
		for len(pages) < 10 {
			page, err := db.Entry.FindPage(ctx, nil, &EntryPageOptions{Fields: EntryFields{Title: true}, Sort: parsed, After: after, Limit: 2})
			if err != nil {
				t.Fatalf("FindPage by %s after %+v: %v", sort, after, err)
			}
			titles := make([]string, 0, len(page.Rows))
			for _, row := range page.Rows {
				titles = append(titles, row.Title)
			}
			pages = append(pages, "["+strings.Join(titles, " ")+"]")
			if page.Next == nil {
				return strings.Join(pages, " ")
			}
			cursor, err := json.Marshal(page.Next)
			if err != nil {
				t.Fatalf("marshal the key %+v: %v", page.Next, err)
			}
			after = &EntryPageKey{}
			if err := json.Unmarshal(cursor, after); err != nil {
				t.Fatalf("unmarshal the key %s: %v", cursor, err)
			}
		}
		t.Fatalf("FindPage by %s read more than 10 pages: %v", sort, pages)
		return ""
	}

	for sort, want := range map[string]string{
		"rank":      "[a b] [c d]",
		"-rank":     "[d c] [b a]",
		"score":     "[c b] [a d]",
		"-score":    "[d a] [b c]",
		"due":       "[a c] [b d]",
		"-due":      "[d b] [c a]",
		"ownerId":   "[a d] [c b]",
		"title":     "[a b] [c d]",
		"-entryId":  "[d c] [a b]",
		"createdAt": "[b a] [c d]",
	} {
		if got := walk(sort); got != want {
			t.Errorf("by %s: pages %s, want %s", sort, got, want)
		}
	}
}
`
