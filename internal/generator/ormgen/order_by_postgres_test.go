package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedORMOrdersAKeywordTable generates the ORM module for
// fixture-queue-db, whose Order table is named order, a Postgres keyword,
// then builds, vets and tests it. Its generated test, against the Postgres
// at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (it skips without the
// database), sorts the table through FindMany and FindOne: the ORDER BY
// names the table as the FROM clause does, "order", where a bare order
// would be a syntax error.
func TestGeneratedORMOrdersAKeywordTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	ormDir := generateORMModule(t, queueFixture)
	if err := os.WriteFile(filepath.Join(ormDir, "order_by_test.go"), []byte(orderByORMTest), 0o644); err != nil {
		t.Fatalf("write order by test: %v", err)
	}
	out := runORMModule(t, ormDir)
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" && !strings.Contains(out, "--- PASS: TestOrderByOnAKeywordTable") {
		t.Fatalf("the generated ORM did not run TestOrderByOnAKeywordTable:\n%s", out)
	}
}

const orderByORMTest = `package orm

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	types "example.com/schemas/types/go/fixture-queue-db"
)

func TestOrderByOnAKeywordTable(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to sort a keyword-named table on Postgres")
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
	schema := fmt.Sprintf("order_by_orm_%d", time.Now().UnixNano())
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

	// Three orders, created out of the order they were placed in.
	day := func(d int) types.TemporalDateTime {
		return types.TemporalDateTime(time.Date(2026, 10, d, 12, 0, 0, 0, time.UTC))
	}
	for _, d := range []int{2, 3, 1} {
		if _, err := db.Order.CreateOne(ctx, &types.Order{PlacedAt: day(d)}); err != nil {
			t.Fatalf("create the order of day %d: %v", d, err)
		}
	}
	placed := func(orders []*types.Order) []int {
		days := make([]int, len(orders))
		for i, order := range orders {
			days[i] = time.Time(order.PlacedAt).Day()
		}
		return days
	}

	for _, tc := range []struct {
		desc bool
		want []int
	}{{false, []int{1, 2, 3}}, {true, []int{3, 2, 1}}} {
		orderBy := []OrderOrderBy{{Field: OrderOrderByPlacedAt, Desc: tc.desc}, {Field: OrderOrderById}}
		orders, total, err := db.Order.FindMany(ctx, nil, &OrderFindOptions{Fields: OrderFields{}.All(), OrderBy: orderBy})
		if err != nil {
			t.Fatalf("FindMany ordered by placedAt (desc %t): %v", tc.desc, err)
		}
		if got := placed(orders); total != 3 || fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Fatalf("FindMany ordered by placedAt (desc %t) = days %v of %d, want %v of 3", tc.desc, got, total, tc.want)
		}
		first, err := db.Order.FindOne(ctx, nil, &OrderFindOptions{Fields: OrderFields{}.All(), OrderBy: orderBy})
		if err != nil || first == nil || time.Time(first.PlacedAt).Day() != tc.want[0] {
			t.Fatalf("FindOne ordered by placedAt (desc %t) = %+v, %v; want the order of day %d", tc.desc, first, err, tc.want[0])
		}
	}
}
`
