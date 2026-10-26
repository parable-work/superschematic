package pgcheck

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestNamingOnPostgres applies fixture-naming-db's create.sql, whose
// tables and columns @table and @column name in camel case (D85), and
// checks that every name the DDL derives from them is the one Postgres
// holds, case and all: the tables, the columns, the CHECKs, the foreign
// key, the indexes, the history table, its function and triggers, the
// queue's table and the trigger that enqueues to it. A raw INSERT that
// leaves out a defaulted column, a value a CHECK refuses, the partial
// unique index over the live rows, SET NULL, the history trigger's image
// and the enqueue trigger's message all work under the quoted names. drop.sql then removes
// them all. TestNamingCreateSQLOnPostgres in the sqlgen package runs it
// with PGCHECK_DATABASE_URL and PGCHECK_SQL_DIR.
func TestNamingOnPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn := scratchDatabase(t, ctx, "superschematic_naming")
	execScript(t, ctx, conn, "create.sql", readSQL(t, "create.sql"))

	names := func(sql string) []string {
		t.Helper()
		rows, err := conn.Query(ctx, sql)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			out = append(out, name)
		}
		slices.Sort(out)
		return out
	}
	for _, tc := range []struct {
		what, sql, want string
	}{
		{"tables", `SELECT relname FROM pg_class WHERE relkind IN ('r', 'p') AND relnamespace = 'public'::regnamespace`, "budgetCategory ledgerNote ledgerNote_history lineItem linePosted"},
		{"lineItem's columns", `SELECT attname FROM pg_attribute WHERE attrelid = '"lineItem"'::regclass AND attnum > 0 AND NOT attisdropped`, "amountCents categoryId createdAt deletedAt externalId id memo postedOn search_text updatedAt"},
		{"constraints", `SELECT conname FROM pg_constraint WHERE connamespace = 'public'::regnamespace AND contype IN ('c', 'f')`, "ck_budgetCategory_displayName ck_budgetCategory_kind fk_lineItem_categoryId"},
		{"indexes", `SELECT indexname FROM pg_indexes WHERE schemaname = 'public' AND indexname NOT LIKE '%_pkey'`, "idx_ledgerNote_history_id_recorded idx_lineItem_postedOn idx_lineItem_search_trgm idx_linePosted_claim_expires_at idx_linePosted_due_at idx_linePosted_updated_at uq_ledgerNote_history_id_version uq_lineItem_external uq_linePosted_ready_key"},
		{"triggers", `SELECT tgname FROM pg_trigger WHERE NOT tgisinternal`, "trg_ledgerNote_bump_version trg_ledgerNote_capture_history_delete trg_ledgerNote_capture_history_write trg_lineItem_enqueue_linePosted"},
		{"functions", `SELECT proname FROM pg_proc WHERE pronamespace = 'public'::regnamespace AND prorettype = 'trigger'::regtype`, "ledgerNote_capture_history lineItem_enqueue_linePosted"},
	} {
		if got := strings.Join(names(tc.sql), " "); got != tc.want {
			t.Errorf("%s = %s\nwant %s", tc.what, got, tc.want)
		}
	}

	var food string
	if err := conn.QueryRow(ctx, `INSERT INTO "budgetCategory" ("displayName", kind) VALUES ('Food', 'personal') RETURNING id::text`).Scan(&food); err != nil {
		t.Fatalf("a category that leaves out createdAt: %v", err)
	}
	_, err := conn.Exec(ctx, `INSERT INTO "budgetCategory" ("displayName", kind) VALUES ('', 'personal')`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "ck_budgetCategory_displayName" {
		t.Errorf("an empty display name: %v, want a violation of ck_budgetCategory_displayName", err)
	}
	mustExec(t, ctx, conn, `INSERT INTO "lineItem" ("amountCents", "categoryId", "externalId") VALUES (100, $1, 'x1')`, food)
	_, err = conn.Exec(ctx, `INSERT INTO "lineItem" ("amountCents", "categoryId", "externalId") VALUES (200, $1, 'x1')`, food)
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "uq_lineItem_external" {
		t.Errorf("a second live line of x1: %v, want a violation of uq_lineItem_external", err)
	}
	mustExec(t, ctx, conn, `UPDATE "lineItem" SET "deletedAt" = now()`)
	mustExec(t, ctx, conn, `INSERT INTO "lineItem" ("amountCents", "categoryId", "externalId") VALUES (300, $1, 'x1')`, food)
	mustExec(t, ctx, conn, `DELETE FROM "budgetCategory" WHERE id = $1`, food)
	var orphans int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM "lineItem" WHERE "categoryId" IS NULL`).Scan(&orphans); err != nil || orphans != 2 {
		t.Errorf("SET NULL cleared %d lines (%v), want 2", orphans, err)
	}
	// Each line from the bank enqueued its posting, and a change of the
	// amount replaced the one that waits; the soft delete and SET NULL left
	// the amounts as they were.
	mustExec(t, ctx, conn, `UPDATE "lineItem" SET "amountCents" = "amountCents" + 50 WHERE "amountCents" = 300`)
	var postings string
	if err := conn.QueryRow(ctx, `SELECT string_agg(p."amountCents"::text, ',' ORDER BY p."amountCents") FROM "linePosted" p JOIN "lineItem" l ON l.id = p."lineId" WHERE p.state = 'ready'`).Scan(&postings); err != nil || postings != "100,350" {
		t.Errorf("the ready postings are %q (%v), want 100,350", postings, err)
	}

	var note string
	if err := conn.QueryRow(ctx, `INSERT INTO "ledgerNote" ("bodyText", "updatedBy") VALUES ('first', gen_random_uuid()) RETURNING id::text`).Scan(&note); err != nil {
		t.Fatal(err)
	}
	mustExec(t, ctx, conn, `UPDATE "ledgerNote" SET "bodyText" = 'second' WHERE id = $1`, note)
	var images []string
	rows, err := conn.Query(ctx, `SELECT data->>'bodyText' FROM "ledgerNote_history" WHERE id = $1 ORDER BY _version`, note)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		images = append(images, body)
	}
	rows.Close()
	if strings.Join(images, ",") != "first,second" {
		t.Errorf("the history holds %v, want first and second", images)
	}

	execScript(t, ctx, conn, "drop.sql", readSQL(t, "drop.sql"))
	var left int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_class WHERE relnamespace = 'public'::regnamespace AND relkind IN ('r', 'p', 'i')`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("drop.sql left %d relations: %v", left, err)
	}
	if got := names(`SELECT proname FROM pg_proc WHERE pronamespace = 'public'::regnamespace AND prorettype = 'trigger'::regtype`); len(got) != 0 {
		t.Errorf("drop.sql left the functions %v", got)
	}
}
