package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedRawQueryOnPostgres generates the ORM module for
// fixture-optimistic-db, then builds, vets and tests it. Its generated
// tests run the Database's raw reads, Query and QueryRow: on the Postgres
// at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (that test skips without the
// database) they read what a repository wrote, on the pool and outside any
// transaction, so a row a transaction has not committed is not read; a
// read of no row scans pgx.ErrNoRows; the server's error and a cancelled
// context come back as they are. With no database, the no-op database's
// Query reads no rows and its QueryRow scans pgx.ErrNoRows.
func TestGeneratedRawQueryOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	ormDir := generateORMModule(t, "fixture-optimistic-db")
	if err := os.WriteFile(filepath.Join(ormDir, "raw_query_test.go"), []byte(rawQueryORMTest), 0o644); err != nil {
		t.Fatalf("write raw query test: %v", err)
	}
	out := runORMModule(t, ormDir)
	if !strings.Contains(out, "--- PASS: TestNoOpRawQuery") {
		t.Fatalf("the generated ORM did not run TestNoOpRawQuery:\n%s", out)
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" && !strings.Contains(out, "--- PASS: TestRawQueryOnPostgres") {
		t.Fatalf("the generated ORM did not run TestRawQueryOnPostgres:\n%s", out)
	}
}

var rawQueryORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-optimistic-db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRawQueryOnPostgres(t *testing.T) {` + strings.Replace(pageTestSetup, "PREFIX", "raw_query_orm", 1) + `
	ctx = WithUserID(ctx, types.NewUUID())
	for _, label := range []string{"Flour", "Salt"} {
		if _, err := db.Shelf.CreateOne(ctx, &types.Shelf{Label: label}); err != nil {
			t.Fatalf("create shelf %s: %v", label, err)
		}
	}

	// Query reads the rows a repository wrote, in the order the SQL asks.
	rows, err := db.Query(ctx, "SELECT label FROM shelf WHERE label <> $1 ORDER BY label DESC", "None")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			t.Fatalf("scan: %v", err)
		}
		labels = append(labels, label)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if fmt.Sprint(labels) != "[Salt Flour]" {
		t.Fatalf("labels = %v, want [Salt Flour]", labels)
	}

	// QueryRow reads one row, and a read of none scans pgx.ErrNoRows.
	var count int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM shelf").Scan(&count); err != nil || count != 2 {
		t.Fatalf("count = %d, %v; want 2", count, err)
	}
	var label string
	if err := db.QueryRow(ctx, "SELECT label FROM shelf WHERE label = $1", "Sugar").Scan(&label); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("a read of no row = %v, want pgx.ErrNoRows", err)
	}

	// The raw reads run outside any transaction: a row a transaction has
	// not committed is not read, and one it has committed is.
	err = db.Transaction(ctx, func(tx TxInterface) error {
		if _, err := tx.GetShelfRepository().CreateOne(ctx, &types.Shelf{Label: "Yeast"}); err != nil {
			return err
		}
		var during int
		if err := db.QueryRow(ctx, "SELECT count(*) FROM shelf").Scan(&during); err != nil {
			return err
		}
		if during != 2 {
			return fmt.Errorf("a raw read during the transaction counted %d shelves, want 2", during)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}
	if err := db.QueryRow(ctx, "SELECT count(*) FROM shelf").Scan(&count); err != nil || count != 3 {
		t.Fatalf("count after the commit = %d, %v; want 3", count, err)
	}

	// The server's error comes back as it is: a table that does not exist
	// is SQLSTATE 42P01, from Query or from its rows, and from Scan.
	var pgErr *pgconn.PgError
	rows, err = db.Query(ctx, "SELECT 1 FROM no_such_table")
	if err == nil {
		for rows.Next() {
		}
		rows.Close()
		err = rows.Err()
	}
	if !errors.As(err, &pgErr) || pgErr.Code != "42P01" {
		t.Fatalf("query of a missing table = %v, want SQLSTATE 42P01", err)
	}
	if err := db.QueryRow(ctx, "SELECT 1 FROM no_such_table").Scan(&count); !errors.As(err, &pgErr) || pgErr.Code != "42P01" {
		t.Fatalf("query row of a missing table = %v, want SQLSTATE 42P01", err)
	}

	// The context passes through: a cancelled one fails the read.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := db.Query(cancelled, "SELECT 1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("query with a cancelled context = %v, want context.Canceled", err)
	}
	if err := db.QueryRow(cancelled, "SELECT 1").Scan(&count); !errors.Is(err, context.Canceled) {
		t.Fatalf("query row with a cancelled context = %v, want context.Canceled", err)
	}
}

func TestNoOpRawQuery(t *testing.T) {
	db := NewNoOpDatabase()
	rows, err := db.Query(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatalf("no-op query: %v", err)
	}
	if rows.Next() {
		t.Fatal("the no-op query read a row")
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("no-op rows: %v", err)
	}
	var one int
	if err := db.QueryRow(context.Background(), "SELECT 1").Scan(&one); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("no-op query row = %v, want pgx.ErrNoRows", err)
	}
}
`
