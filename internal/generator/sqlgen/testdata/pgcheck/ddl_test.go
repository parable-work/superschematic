package pgcheck

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestDDLOnPostgres applies create.sql for the fixture-ddl-db service,
// whose tables say what their schema says (D83), and checks what Postgres
// makes of it:
//
//   - a raw INSERT that leaves out a column with a Default<T, V> takes V,
//     and one with a value outside an enum or a Validate bound is refused
//     by the column's CHECK;
//   - one that leaves out a required timestamp is refused, unless it is an
//     audit timestamp or declares Default<T, "now"> (D88);
//   - deleting a category clears the nullable relations that SET NULL
//     names, a child category's parent and a line's category, and keeps
//     their rows;
//   - the expression indexes and the partial indexes exist as declared,
//     unique where the schema says so;
//   - the raw SQL made a materialized view and its unique index, in the
//     order the pieces run, so the view refreshes concurrently.
//
// drop.sql then removes the tables and the view.
// TestDDLCreateSQLOnPostgres in the sqlgen package runs it with
// PGCHECK_DATABASE_URL and PGCHECK_SQL_DIR.
func TestDDLOnPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn := scratchDatabase(t, ctx, "superschematic_ddl")
	execScript(t, ctx, conn, "create.sql", readSQL(t, "create.sql"))

	// Defaults: the columns a raw INSERT leaves out take their schema
	// defaults.
	var food string
	var active bool
	var sortOrder float64
	if err := conn.QueryRow(ctx, `INSERT INTO category (label, kind) VALUES ('Food', 'personal') RETURNING id::text, active, sort_order`).Scan(&food, &active, &sortOrder); err != nil {
		t.Fatalf("a category that leaves out active and sort_order: %v", err)
	}
	if active || sortOrder != 0 {
		t.Errorf("active = %v, sort_order = %v; want the defaults false and 0", active, sortOrder)
	}
	var memo string
	if err := conn.QueryRow(ctx, `INSERT INTO line_item (category_id, amount_cents, posted_at) VALUES ($1, 1250, now()) RETURNING memo`, food).Scan(&memo); err != nil {
		t.Fatalf("a line that leaves out memo: %v", err)
	}
	if memo != "it's unexplained" {
		t.Errorf("memo = %q, want the default", memo)
	}

	// The time of the insert (D88): a required timestamp with no
	// Default<T, "now"> has no default, so a line that leaves out when it
	// was posted is refused rather than posted now; the audit createdAt and
	// enteredOn, declared Default<Temporal.Date, "now">, take the time and
	// the day of the insert.
	_, err := conn.Exec(ctx, `INSERT INTO line_item (amount_cents) VALUES (700)`)
	var notNull *pgconn.PgError
	if !errors.As(err, &notNull) || notNull.Code != "23502" || notNull.ColumnName != "posted_at" {
		t.Errorf("a line that leaves out posted_at: got %v, want a not-null violation on posted_at", err)
	}
	var stamped bool
	if err := conn.QueryRow(ctx, `INSERT INTO line_item (amount_cents, posted_at) VALUES (700, '2026-01-02T03:04:05Z') RETURNING created_at = now() AND entered_on = CURRENT_DATE AND posted_at = '2026-01-02T03:04:05Z'`).Scan(&stamped); err != nil || !stamped {
		t.Errorf("a line that leaves out created_at and entered_on: stamped %v (%v), want the time and the day of the insert", stamped, err)
	}
	mustExec(t, ctx, conn, `DELETE FROM line_item WHERE amount_cents = 700`)

	// Checks: an enum's other values and a Validate bound's outside are
	// refused, each by its column's constraint.
	for _, tc := range []struct {
		name, sql, constraint string
	}{
		{"a kind outside the enum", `INSERT INTO category (label, kind) VALUES ('Rent', 'bogus')`, "ck_category_kind"},
		{"an empty label", `INSERT INTO category (label, kind) VALUES ('', 'business')`, "ck_category_label"},
		{"a label over 60 characters", `INSERT INTO category (label, kind) VALUES (repeat('x', 61), 'business')`, "ck_category_label"},
		{"a sort order over 1000", `INSERT INTO category (label, kind, sort_order) VALUES ('Rent', 'business', 1001)`, "ck_category_sort_order"},
		{"a confidence over 1", `INSERT INTO line_item (amount_cents, confidence, posted_at) VALUES (1, 1.5, now())`, "ck_line_item_confidence"},
		{"a kind changed to one outside the enum", `UPDATE category SET kind = 'bogus'`, "ck_category_kind"},
	} {
		_, err := conn.Exec(ctx, tc.sql)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != tc.constraint {
			t.Errorf("%s: got %v, want a check violation of %s", tc.name, err, tc.constraint)
		}
	}
	// NULL passes a check, and a label of 60 two-byte characters passes the
	// length bound, which counts characters as every validator does.
	if _, err := conn.Exec(ctx, `INSERT INTO line_item (amount_cents, confidence, posted_at) VALUES (1, NULL, now())`); err != nil {
		t.Errorf("a line with no confidence: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO category (label, kind) VALUES (repeat(chr(233), 60), 'accounting')`); err != nil {
		t.Errorf("a label of 60 characters: %v", err)
	}

	// SET NULL: deleting a category keeps the rows that referenced it, with
	// the reference cleared.
	var child string
	if err := conn.QueryRow(ctx, `INSERT INTO category (label, kind, parent_id) VALUES ('Groceries', 'personal', $1) RETURNING id::text`, food).Scan(&child); err != nil {
		t.Fatal(err)
	}
	mustExec(t, ctx, conn, `DELETE FROM category WHERE id = $1`, food)
	var parentCleared, categoryCleared bool
	if err := conn.QueryRow(ctx, `SELECT parent_id IS NULL FROM category WHERE id = $1`, child).Scan(&parentCleared); err != nil {
		t.Fatalf("the child category is gone: %v", err)
	}
	if err := conn.QueryRow(ctx, `SELECT bool_and(category_id IS NULL) FROM line_item WHERE amount_cents = 1250`).Scan(&categoryCleared); err != nil {
		t.Fatal(err)
	}
	if !parentCleared || !categoryCleared {
		t.Errorf("deleting a category cleared the child's parent: %v, the line's category: %v; want both", parentCleared, categoryCleared)
	}

	// The indexes, as declared.
	for name, want := range map[string]string{
		"uq_category_label_lower":  "USING btree (lower(label))",
		"idx_category_active_kind": "USING btree (kind) WHERE active",
		"idx_line_item_posted_at":  "USING btree (posted_at)",
		"uq_line_item_external":    "USING btree (external_id) WHERE (external_id IS NOT NULL)",
	} {
		var def string
		if err := conn.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE indexname = $1`, name).Scan(&def); err != nil {
			t.Errorf("index %s: %v", name, err)
			continue
		}
		if !strings.Contains(def, want) || strings.HasPrefix(name, "uq_") != strings.HasPrefix(def, "CREATE UNIQUE INDEX") {
			t.Errorf("index %s is %s, want %s", name, def, want)
		}
	}

	// The raw SQL: the view holds the lines' totals, and its unique index,
	// which a piece that runs after the view's makes, lets it refresh
	// concurrently.
	mustExec(t, ctx, conn, `REFRESH MATERIALIZED VIEW CONCURRENTLY category_totals`)
	var lines int64
	if err := conn.QueryRow(ctx, `SELECT sum(line_count) FROM category_totals`).Scan(&lines); err != nil || lines != 2 {
		t.Errorf("category_totals counts %d lines (%v), want 2", lines, err)
	}
	var comment string
	if err := conn.QueryRow(ctx, `SELECT obj_description('category_totals'::regclass)`).Scan(&comment); err != nil || comment != "Each category's total, for the dashboard." {
		t.Errorf("the view's comment is %q (%v)", comment, err)
	}

	execScript(t, ctx, conn, "drop.sql", readSQL(t, "drop.sql"))
	var left int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_class WHERE relname IN ('category', 'line_item', 'category_totals')`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("drop.sql left %d relations: %v", left, err)
	}
}
