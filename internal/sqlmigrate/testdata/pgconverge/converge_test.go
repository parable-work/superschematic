// Package pgconverge checks migration plans against a real Postgres.
// TestConvergenceOnPostgres in the sqlmigrate package writes one directory
// per case and runs TestConvergence here with:
//
//	PGCONVERGE_DATABASE_URL  a Postgres URL whose role may create databases
//	PGCONVERGE_CASES         the directory holding the cases
//
// A case directory holds:
//
//	from.sql          create.sql of the previous version (absent: an empty database)
//	seed.sql          rows to insert before the plan (optional)
//	plan.json         the plan from the previous version to the new one, or
//	plan-NN.json      several plans, applied in name order
//	to.sql            create.sql of the new version
//	checks.json       queries that must return true after the plan (optional)
//	constraints.json  the primary key and unique constraint names the model
//	                  gives to.sql's tables (optional)
//
// Each case builds two databases: one from from.sql, seed.sql and the plan's
// steps, run as the runner runs them, and one from to.sql. Their catalogs
// must be the same.
package pgconverge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func env(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s is not set; run through TestConvergenceOnPostgres", name)
	}
	return v
}

type plan struct {
	Steps []struct {
		Index         int      `json:"index"`
		Subject       string   `json:"subject"`
		Statements    []string `json:"statements"`
		Transactional bool     `json:"transactional"`
	} `json:"steps"`
}

type check struct {
	Name string `json:"name"`
	SQL  string `json:"sql"`
}

type constraintName struct {
	Table string `json:"table"`
	Name  string `json:"name"`
}

var databaseCount atomic.Int64

// scratchDatabase creates a database, drops it when t ends, and returns a
// connection to it.
func scratchDatabase(t *testing.T, ctx context.Context, admin *pgx.Conn, adminURL string) *pgx.Conn {
	t.Helper()
	name := fmt.Sprintf("sqlmigrate_converge_%d_%d", time.Now().UnixNano(), databaseCount.Add(1))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})
	dbURL, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	dbURL.Path = "/" + name
	conn, err := pgx.Connect(ctx, dbURL.String())
	if err != nil {
		t.Fatalf("connect %s: %v", name, err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// execScript runs a file of statements in one simple-protocol call.
func execScript(t *testing.T, ctx context.Context, conn *pgx.Conn, dir, name string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.PgConn().Exec(ctx, string(b)).ReadAll(); err != nil {
		t.Fatalf("apply %s: %v", name, err)
	}
}

// applyPlans applies the case's plans in name order.
func applyPlans(t *testing.T, ctx context.Context, conn *pgx.Conn, dir string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "plan*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("the case has no plan")
	}
	sort.Strings(files)
	for _, file := range files {
		applyPlan(t, ctx, conn, file)
	}
}

// applyPlan runs the plan's steps as the runner does: a transactional step
// in one transaction, any other step one statement at a time.
func applyPlan(t *testing.T, ctx context.Context, conn *pgx.Conn, file string) {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var p plan
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	for _, step := range p.Steps {
		if !step.Transactional {
			for _, stmt := range step.Statements {
				if _, err := conn.Exec(ctx, stmt); err != nil {
					t.Fatalf("step %d %s: %v\n%s", step.Index, step.Subject, err, stmt)
				}
			}
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, stmt := range step.Statements {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				_ = tx.Rollback(ctx)
				t.Fatalf("step %d %s: %v\n%s", step.Index, step.Subject, err, stmt)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("step %d %s: commit: %v", step.Index, step.Subject, err)
		}
	}
}

// userSchemas excludes the system schemas from every catalog query.
const userSchemas = `n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND n.nspname NOT LIKE 'pg_temp%'`

// catalogQueries read what a plan must leave as create.sql does, one line
// per object, compared by name.
var catalogQueries = map[string]string{
	"schema":    `SELECT n.nspname FROM pg_namespace n WHERE ` + userSchemas,
	"extension": `SELECT extname FROM pg_extension`,
	"relation": `SELECT n.nspname || '.' || c.relname || ' ' || c.relkind::text
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE ` + userSchemas + ` AND c.relkind IN ('r', 'p', 'v', 'm', 'i', 'I', 'S')
		AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = c.oid AND d.deptype = 'e')`,
	"column": `SELECT n.nspname || '.' || c.relname || '.' || a.attname || ' ' || format_type(a.atttypid, a.atttypmod)
			|| ' notnull=' || a.attnotnull || ' default=' || coalesce(pg_get_expr(d.adbin, d.adrelid), '')
			|| ' generated=' || a.attgenerated::text
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE ` + userSchemas + ` AND c.relkind IN ('r', 'p', 'v') AND a.attnum > 0 AND NOT a.attisdropped
		AND NOT EXISTS (SELECT 1 FROM pg_depend e WHERE e.objid = c.oid AND e.deptype = 'e')`,
	"constraint": `SELECT n.nspname || '.' || c.relname || ' ' || con.conname || ' ' || pg_get_constraintdef(con.oid)
			|| ' valid=' || con.convalidated
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE ` + userSchemas,
	"index": `SELECT n.nspname || '.' || ic.relname || ' ' || pg_get_indexdef(i.indexrelid) || ' valid=' || i.indisvalid
		FROM pg_index i
		JOIN pg_class ic ON ic.oid = i.indexrelid
		JOIN pg_namespace n ON n.oid = ic.relnamespace
		WHERE ` + userSchemas + `
		AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = i.indrelid AND d.deptype = 'e')`,
	"trigger": `SELECT c.relname || ' ' || t.tgname || ' ' || pg_get_triggerdef(t.oid)
		FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE NOT t.tgisinternal AND ` + userSchemas,
	"function": `SELECT n.nspname || '.' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ') '
			|| pg_get_functiondef(p.oid)
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE ` + userSchemas + `
		AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = p.oid AND d.deptype = 'e')`,
	"view": `SELECT schemaname || '.' || viewname || ' ' || definition FROM pg_views
		WHERE schemaname NOT IN ('pg_catalog', 'information_schema')`,
	"comment": `SELECT n.nspname || '.' || c.relname || coalesce('.' || a.attname, '') || ' ' || d.description
		FROM pg_description d
		JOIN pg_class c ON c.oid = d.objoid AND d.classoid = 'pg_class'::regclass
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = d.objsubid AND d.objsubid > 0
		WHERE ` + userSchemas,
	"partition": `SELECT inhrelid::regclass::text || ' of ' || inhparent::regclass::text || ' ' || coalesce(pg_get_expr(c.relpartbound, c.oid), '')
		FROM pg_inherits JOIN pg_class c ON c.oid = inhrelid`,
}

// catalog reads every catalog query into sorted lines, by kind.
func catalog(t *testing.T, ctx context.Context, conn *pgx.Conn) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for kind, query := range catalogQueries {
		rows, err := conn.Query(ctx, query)
		if err != nil {
			t.Fatalf("catalog %s: %v", kind, err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("catalog %s: %v", kind, err)
		}
		sort.Strings(lines)
		out[kind] = lines
	}
	return out
}

// kept are the kinds a plan creates and never drops: an extension, as
// drop.sql leaves it, and a projection pool's schema. The planned database
// may hold more of them than to.sql creates.
var kept = map[string]bool{"extension": true, "schema": true}

func compare(t *testing.T, planned, created map[string][]string) {
	t.Helper()
	kinds := make([]string, 0, len(created))
	for kind := range created {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		var missing, extra []string
		for _, line := range created[kind] {
			if !slices.Contains(planned[kind], line) {
				missing = append(missing, line)
			}
		}
		for _, line := range planned[kind] {
			if !kept[kind] && !slices.Contains(created[kind], line) {
				extra = append(extra, line)
			}
		}
		if len(missing) > 0 || len(extra) > 0 {
			t.Errorf("%s differs:\nonly from to.sql:\n  %s\nonly from the plan:\n  %s",
				kind, strings.Join(missing, "\n  "), strings.Join(extra, "\n  "))
		}
	}
}

func readJSON(t *testing.T, path string, v any) bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return true
}

func TestConvergence(t *testing.T) {
	adminURL := env(t, "PGCONVERGE_DATABASE_URL")
	casesDir := env(t, "PGCONVERGE_CASES")
	entries, err := os.ReadDir(casesDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(casesDir, entry.Name())
		t.Run(entry.Name(), func(t *testing.T) {
			planned := scratchDatabase(t, ctx, admin, adminURL)
			execScript(t, ctx, planned, dir, "from.sql")
			execScript(t, ctx, planned, dir, "seed.sql")
			applyPlans(t, ctx, planned, dir)
			var checks []check
			readJSON(t, filepath.Join(dir, "checks.json"), &checks)
			for _, c := range checks {
				var ok bool
				if err := planned.QueryRow(ctx, c.SQL).Scan(&ok); err != nil {
					t.Errorf("check %s: %v\n%s", c.Name, err, c.SQL)
				} else if !ok {
					t.Errorf("check %s failed:\n%s", c.Name, c.SQL)
				}
			}

			created := scratchDatabase(t, ctx, admin, adminURL)
			execScript(t, ctx, created, dir, "to.sql")
			var names []constraintName
			if readJSON(t, filepath.Join(dir, "constraints.json"), &names) {
				for _, n := range names {
					var found bool
					err := created.QueryRow(ctx,
						`SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = $1::regclass AND conname = $2)`,
						pgx.Identifier{n.Table}.Sanitize(), n.Name).Scan(&found)
					if err != nil || !found {
						t.Errorf("to.sql gives table %s no constraint %s (%v)", n.Table, n.Name, err)
					}
				}
			}
			compare(t, catalog(t, ctx, planned), catalog(t, ctx, created))
		})
	}
}
