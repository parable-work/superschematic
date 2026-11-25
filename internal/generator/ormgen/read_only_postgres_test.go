package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedReadOnlyOnPostgres generates the ORM module for
// fixture-optimistic-db, then builds, vets and tests it. Its generated
// tests run the read-only scope (D150) on the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (that test skips without the
// database), over the read replica replicaTestSetup stands up. ReadOnly's
// repositories and raw reads, and ReadOnlyTransaction, read on the
// replica; with no replica they read on the primary, and ReadOnly is the
// Database itself. A ReadOnlyTransaction is read only at repeatable read
// on either: a write in it fails with SQLSTATE 25006 and leaves no row, a
// row another transaction commits during it is not read, and the error its
// function returns, or a cancelled context's, comes back. The read-only
// interfaces have no write method. With no database, the no-op database's
// scope is itself and its read-only transaction runs the function.
func TestGeneratedReadOnlyOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	ormDir := generateORMModule(t, "fixture-optimistic-db")
	if err := os.WriteFile(filepath.Join(ormDir, "read_only_test.go"), []byte(readOnlyORMTest), 0o644); err != nil {
		t.Fatalf("write read-only test: %v", err)
	}
	out := runORMModule(t, ormDir)
	for _, name := range []string{"TestNoOpReadOnly", "TestReadOnlyInterfacesHaveNoWrite"} {
		if !strings.Contains(out, "--- PASS: "+name) {
			t.Fatalf("the generated ORM did not run %s:\n%s", name, out)
		}
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" && !strings.Contains(out, "--- PASS: TestReadOnlyOnPostgres") {
		t.Fatalf("the generated ORM did not run TestReadOnlyOnPostgres:\n%s", out)
	}
}

var readOnlyORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-optimistic-db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// readOnlyCount counts the shelves a read-only scope's repository finds.
func readOnlyCount(t *testing.T, ctx context.Context, db ReadOnlyDatabaseInterface) int {
	t.Helper()
	_, total, err := db.GetShelfReadOnlyRepository().FindMany(ctx, nil, &ShelfFindOptions{Fields: ShelfFields{Label: true}})
	if err != nil {
		t.Fatalf("find shelves: %v", err)
	}
	return total
}

// checkReadOnlyTransaction runs ReadOnlyTransaction on db, whose reads
// find want shelves, while writer, a pool over the same schema, commits
// one more.
func checkReadOnlyTransaction(t *testing.T, ctx context.Context, name string, db *Database, writer *pgxpool.Pool, want int) {
	t.Helper()
	err := db.ReadOnlyTransaction(ctx, func(tx ReadOnlyTxInterface) error {
		var isolation, readOnly string
		if err := scanOne(ctx, tx, "SELECT current_setting('transaction_isolation'), current_setting('transaction_read_only')", &isolation, &readOnly); err != nil {
			return err
		}
		if isolation != "repeatable read" || readOnly != "on" {
			return fmt.Errorf("the transaction is %s, read only %s; want repeatable read, on", isolation, readOnly)
		}
		_, before, err := tx.GetShelfReadOnlyRepository().FindMany(ctx, nil, &ShelfFindOptions{Fields: ShelfFields{Label: true}})
		if err != nil {
			return err
		}
		if before != want {
			return fmt.Errorf("the transaction found %d shelves, want %d", before, want)
		}
		// A row committed during the transaction is not in its snapshot.
		if _, err := writer.Exec(ctx, "INSERT INTO shelf (label, created_by, updated_by) VALUES ('During', gen_random_uuid(), gen_random_uuid())"); err != nil {
			return err
		}
		var after int
		if err := scanOne(ctx, tx, "SELECT count(*) FROM shelf WHERE deleted_at IS NULL", &after); err != nil {
			return err
		}
		if after != want {
			return fmt.Errorf("after a commit outside it the transaction counted %d shelves, want %d", after, want)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("%s: read-only transaction: %v", name, err)
	}
	if _, err := writer.Exec(ctx, "DELETE FROM shelf WHERE label = 'During'"); err != nil {
		t.Fatalf("%s: remove the row written during the transaction: %v", name, err)
	}

	// A write in it fails with SQLSTATE 25006, which the transaction
	// returns, and leaves no row.
	var pgErr *pgconn.PgError
	err = db.ReadOnlyTransaction(ctx, func(tx ReadOnlyTxInterface) error {
		var id string
		return scanOne(ctx, tx, "INSERT INTO shelf (label, created_by, updated_by) VALUES ('Refused', gen_random_uuid(), gen_random_uuid()) RETURNING id::text", &id)
	})
	if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Fatalf("%s: a write in the read-only transaction = %v, want SQLSTATE 25006", name, err)
	}
	var refused int
	if err := writer.QueryRow(ctx, "SELECT count(*) FROM shelf WHERE label = 'Refused'").Scan(&refused); err != nil || refused != 0 {
		t.Fatalf("%s: the refused write left %d rows, %v", name, refused, err)
	}

	// The function's error comes back as it is, after the rollback.
	stop := errors.New("stop")
	if err := db.ReadOnlyTransaction(ctx, func(ReadOnlyTxInterface) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("%s: the function's error = %v, want stop", name, err)
	}

	// A cancelled context fails the transaction before the function runs.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	ran := false
	err = db.ReadOnlyTransaction(cancelled, func(ReadOnlyTxInterface) error {
		ran = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || ran {
		t.Fatalf("%s: with a cancelled context = %v, ran %v; want context.Canceled before the function", name, err, ran)
	}
}

// scanOne scans the one row of a raw read in a read-only transaction.
func scanOne(ctx context.Context, tx ReadOnlyTxInterface, sql string, dest ...any) error {
	rows, err := tx.Query(ctx, sql)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return pgx.ErrNoRows
	}
	if err := rows.Scan(dest...); err != nil {
		return err
	}
	rows.Close()
	return rows.Err()
}

func TestReadOnlyOnPostgres(t *testing.T) {` + strings.Replace(replicaTestSetup, "PREFIX", "read_only_orm", 1) + `
	ctx = WithUserID(ctx, types.NewUUID())
	for _, label := range []string{"Replica one", "Replica two"} {
		if _, err := seed.Exec(ctx, "INSERT INTO shelf (label, created_by, updated_by) VALUES ($1, gen_random_uuid(), gen_random_uuid())", label); err != nil {
			t.Fatalf("seed the replica: %v", err)
		}
	}
	db, err := ConnectWithPools(primary, replica)
	if err != nil {
		t.Fatalf("connect with pools: %v", err)
	}
	defer db.Close()
	if _, err := db.Shelf.CreateOne(ctx, &types.Shelf{Label: "Primary"}); err != nil {
		t.Fatalf("create on the primary: %v", err)
	}

	// ReadOnly reads on the replica: its repositories and its raw reads.
	if got := readOnlyCount(t, ctx, db.ReadOnly()); got != 2 {
		t.Fatalf("ReadOnly found %d shelves, want the replica's 2", got)
	}
	if db.ReadOnly() != ReadOnlyDatabaseInterface(db.readReplica) {
		t.Fatal("ReadOnly is not the replica's Database")
	}
	var count int
	if err := db.ReadOnly().QueryRow(ctx, "SELECT count(*) FROM shelf").Scan(&count); err != nil || count != 2 {
		t.Fatalf("a raw read through ReadOnly counted %d, %v; want the replica's 2", count, err)
	}
	if err := db.ReadOnly().Ping(ctx); err != nil {
		t.Fatalf("ping through ReadOnly: %v", err)
	}
	// ReadOnlyTransaction reads on the replica, as does the one ReadOnly
	// takes.
	checkReadOnlyTransaction(t, ctx, "replica", db, seed, 2)
	err = db.ReadOnly().ReadOnlyTransaction(ctx, func(tx ReadOnlyTxInterface) error {
		_, total, err := tx.GetShelfReadOnlyRepository().FindMany(ctx, nil, &ShelfFindOptions{Fields: ShelfFields{Label: true}})
		if err == nil && total != 2 {
			err = fmt.Errorf("found %d shelves, want the replica's 2", total)
		}
		return err
	})
	if err != nil {
		t.Fatalf("ReadOnly's read-only transaction: %v", err)
	}

	// With no replica the scope is the Database itself, on the primary,
	// and its transaction is still read only.
	alonePool, err := pgxpool.NewWithConfig(ctx, primary.Config())
	if err != nil {
		t.Fatalf("connect the primary again: %v", err)
	}
	alone, err := ConnectWithPools(alonePool, nil)
	if err != nil {
		t.Fatalf("connect with no replica: %v", err)
	}
	defer alone.Close()
	if alone.ReadOnly() != ReadOnlyDatabaseInterface(alone) {
		t.Fatal("with no replica ReadOnly is not the Database itself")
	}
	if got := readOnlyCount(t, ctx, alone.ReadOnly()); got != 1 {
		t.Fatalf("with no replica ReadOnly found %d shelves, want the primary's 1", got)
	}
	checkReadOnlyTransaction(t, ctx, "primary", alone, primary, 1)
}

func TestReadOnlyInterfacesHaveNoWrite(t *testing.T) {
	for _, iface := range []reflect.Type{
		reflect.TypeOf((*ReadOnlyShelfRepositoryInterface)(nil)).Elem(),
		reflect.TypeOf((*ReadOnlyStockRepositoryInterface)(nil)).Elem(),
		reflect.TypeOf((*ReadOnlyDatabaseInterface)(nil)).Elem(),
		reflect.TypeOf((*ReadOnlyTxInterface)(nil)).Elem(),
	} {
		for i := 0; i < iface.NumMethod(); i++ {
			name := iface.Method(i).Name
			for _, write := range []string{"Create", "Update", "Upsert", "Delete", "Exec", "Transaction"} {
				if len(name) >= len(write) && name[:len(write)] == write {
					t.Errorf("%s has the write %s", iface.Name(), name)
				}
			}
		}
	}
	for _, iface := range []reflect.Type{
		reflect.TypeOf((*ReadOnlyShelfRepositoryInterface)(nil)).Elem(),
	} {
		for _, read := range []string{"GetOne", "GetManyByIDs", "FindOne", "FindMany", "FindPage"} {
			if _, ok := iface.MethodByName(read); !ok {
				t.Errorf("%s has no %s", iface.Name(), read)
			}
		}
	}
}

func TestNoOpReadOnly(t *testing.T) {
	db := NewNoOpDatabase()
	if db.ReadOnly() != ReadOnlyDatabaseInterface(db) {
		t.Fatal("the no-op database's ReadOnly is not itself")
	}
	ran := false
	err := db.ReadOnlyTransaction(context.Background(), func(tx ReadOnlyTxInterface) error {
		ran = true
		_, err := tx.GetShelfReadOnlyRepository().GetOne(context.Background(), types.NewUUID(), nil)
		return err
	})
	if !ran || !errors.Is(err, ErrNotFound) {
		t.Fatalf("the no-op read-only transaction ran %v and returned %v, want ErrNotFound", ran, err)
	}
	if _, err := db.ReadOnly().GetShelfReadOnlyRepository().GetOne(context.Background(), types.NewUUID(), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the no-op read-only repository = %v, want ErrNotFound", err)
	}
	var one int
	if err := db.ReadOnly().QueryRow(context.Background(), "SELECT 1").Scan(&one); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("the no-op read-only query row = %v, want pgx.ErrNoRows", err)
	}
}

`
