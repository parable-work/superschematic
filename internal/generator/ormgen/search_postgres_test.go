package ormgen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedSearchFilterOnPostgres generates the ORM module for
// fixture-db, whose Tenant.name is a search field, then builds, vets and
// tests it. Its generated test, against the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (it skips without the database),
// searches the tenants through TenantFilter.SearchText (D62, amended): each
// word, in any case and in any order, keeps the rows whose search_text
// holds it; a % and a _ match themselves; a blank text and nil keep every
// row; a ninth word does not count; and TenantFilterConditions numbers its
// placeholders after the values a caller's query already holds.
func TestGeneratedSearchFilterOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	ormDir := generateORMModule(t, "fixture-db")
	if err := os.WriteFile(filepath.Join(ormDir, "search_filter_test.go"), []byte(searchFilterORMTest), 0o644); err != nil {
		t.Fatalf("write search filter test: %v", err)
	}
	out := runORMModule(t, ormDir)
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" && !strings.Contains(out, "--- PASS: TestSearchTextOnPostgres") {
		t.Fatalf("the generated ORM did not run TestSearchTextOnPostgres:\n%s", out)
	}
}

// TestGeneratedSearchHelpersMatchTheParity holds the generated module's
// searchTerms and searchLikePattern to the vectors the three HTTP
// runtimes answer, runtime/http/testdata/search_parity.json, which its
// generated test reads by the absolute path a constant gives it. It needs
// no database.
func TestGeneratedSearchHelpersMatchTheParity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	vectors, err := filepath.Abs(filepath.Join("..", "..", "..", "runtime", "http", "testdata", "search_parity.json"))
	if err != nil {
		t.Fatalf("parity vectors: %v", err)
	}
	if _, err := os.Stat(vectors); err != nil {
		t.Fatalf("parity vectors: %v", err)
	}
	ormDir := generateORMModule(t, "fixture-db")
	test := fmt.Sprintf(searchParityORMTest, fmt.Sprintf("%q", vectors))
	if err := os.WriteFile(filepath.Join(ormDir, "search_parity_test.go"), []byte(test), 0o644); err != nil {
		t.Fatalf("write search parity test: %v", err)
	}
	out := runORMModule(t, ormDir)
	if !strings.Contains(out, "--- PASS: TestSearchHelpersMatchTheParity") {
		t.Fatalf("the generated ORM did not run TestSearchHelpersMatchTheParity:\n%s", out)
	}
}

const searchFilterORMTest = `package orm

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSearchTextOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to search a table on Postgres")
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
	schema := fmt.Sprintf("search_orm_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	// pg_trgm, which the search index takes, is in public.
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

	all := []string{"100% cotton", "1000 threads", "Smooth anvil", "smoky Kettle", "under_score"}
	for i, name := range all {
		if _, err := pool.Exec(ctx,
			"INSERT INTO tenant (name, slug, email, status, is_active, seat_count, metadata) VALUES ($1, $2, 'a@example.com', 'active', true, 5, '{}')",
			name, fmt.Sprintf("tenant-%d", i)); err != nil {
			t.Fatalf("insert %q: %v", name, err)
		}
	}
	names := func(tenants []*types.Tenant) []string {
		got := make([]string, 0, len(tenants))
		for _, tenant := range tenants {
			got = append(got, string(tenant.Name))
		}
		slices.Sort(got)
		return got
	}
	text := func(s string) *string { return &s }
	nine := "s m o t h an vil smooth kettle"

	for _, tc := range []struct {
		name   string
		filter *TenantFilter
		want   []string
	}{
		{"one word, in another case", &TenantFilter{SearchText: text("smooth")}, []string{"Smooth anvil"}},
		{"two words, in any order and case", &TenantFilter{SearchText: text("ANVIL smooth")}, []string{"Smooth anvil"}},
		{"a word of two rows", &TenantFilter{SearchText: text("smo")}, []string{"Smooth anvil", "smoky Kettle"}},
		{"a percent sign matches itself", &TenantFilter{SearchText: text("100%")}, []string{"100% cotton"}},
		{"an underscore matches itself", &TenantFilter{SearchText: text("_")}, []string{"under_score"}},
		{"a word no row holds", &TenantFilter{SearchText: text("smooth kettle")}, []string{}},
		{"a blank text", &TenantFilter{SearchText: text("  \t ")}, all},
		{"an empty text", &TenantFilter{SearchText: text("")}, all},
		{"no text", &TenantFilter{}, all},
		{"no filter", nil, all},
		{"a ninth word does not count", &TenantFilter{SearchText: text(nine)}, []string{"Smooth anvil"}},
		{"with another field's condition", &TenantFilter{Slug: &StringFilter{Eq: text("tenant-3")}, SearchText: text("KETTLE")}, []string{"smoky Kettle"}},
		{"another field's condition that excludes", &TenantFilter{Slug: &StringFilter{Eq: text("tenant-2")}, SearchText: text("kettle")}, []string{}},
	} {
		tenants, total, err := db.Tenant.FindMany(ctx, tc.filter, &TenantFindOptions{Fields: TenantFields{}.All()})
		if err != nil {
			t.Fatalf("%s: FindMany: %v", tc.name, err)
		}
		if got := names(tenants); !slices.Equal(got, tc.want) || total != len(tc.want) {
			t.Errorf("%s: FindMany = %q of %d, want %q", tc.name, got, total, tc.want)
		}
	}

	// The conditions of a query of the caller's own number their
	// placeholders after the two values it already holds.
	args := []any{"nothing", 5.0}
	conditions := TenantFilterConditions(&TenantFilter{SearchText: text("anvil SMOOTH")}, &args)
	if len(conditions) != 2 || !strings.Contains(conditions[0], "$3") || !strings.Contains(conditions[1], "$4") || len(args) != 4 {
		t.Fatalf("TenantFilterConditions = %q with args %q, want two conditions on $3 and $4", conditions, args)
	}
	rows, err := pool.Query(ctx, "SELECT name FROM tenant WHERE name <> $1 AND seat_count = $2 AND "+strings.Join(conditions, " AND ")+" ORDER BY name", args...)
	if err != nil {
		t.Fatalf("the caller's query: %v", err)
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("the caller's query: %v", err)
	}
	if !slices.Equal(found, []string{"Smooth anvil"}) {
		t.Fatalf("the caller's query found %q, want [Smooth anvil]", found)
	}
	none := []any{"kept"}
	if conditions := TenantFilterConditions(nil, &none); len(conditions) != 0 || len(none) != 1 {
		t.Fatalf("TenantFilterConditions(nil) = %q with args %q, want none and the args as they were", conditions, none)
	}
}
`

// searchParityORMTest is the generated module's parity test; %s is the
// quoted path of search_parity.json.
const searchParityORMTest = `package orm

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// searchParityVectors is runtime/http/testdata/search_parity.json, by its
// absolute path in the checkout that generated this module.
const searchParityVectors = %s

func TestSearchHelpersMatchTheParity(t *testing.T) {
	data, err := os.ReadFile(searchParityVectors)
	if err != nil {
		t.Fatalf("read the parity vectors: %%v", err)
	}
	var vectors struct {
		Terms []struct {
			Name  string   ` + "`json:\"name\"`" + `
			Text  string   ` + "`json:\"text\"`" + `
			Terms []string ` + "`json:\"terms\"`" + `
		} ` + "`json:\"terms\"`" + `
		Patterns []struct {
			Name    string ` + "`json:\"name\"`" + `
			Term    string ` + "`json:\"term\"`" + `
			Pattern string ` + "`json:\"pattern\"`" + `
		} ` + "`json:\"patterns\"`" + `
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("decode the parity vectors: %%v", err)
	}
	if len(vectors.Terms) == 0 || len(vectors.Patterns) == 0 {
		t.Fatalf("the parity vectors hold %%d terms and %%d patterns cases", len(vectors.Terms), len(vectors.Patterns))
	}
	for _, tc := range vectors.Terms {
		if got := searchTerms(tc.Text); !slices.Equal(got, tc.Terms) || got == nil {
			t.Errorf("%%s: searchTerms(%%q) = %%q, want %%q", tc.Name, tc.Text, got, tc.Terms)
		}
	}
	for _, tc := range vectors.Patterns {
		if got := searchLikePattern(tc.Term); got != tc.Pattern {
			t.Errorf("%%s: searchLikePattern(%%q) = %%q, want %%q", tc.Name, tc.Term, got, tc.Pattern)
		}
	}
}
`
