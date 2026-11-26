package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedReadReplicaOnPostgres generates the ORM module for
// fixture-optimistic-db, then builds, vets and tests it. Its generated
// tests stand a read replica up on the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (that test skips without the
// database) as a schema of its own whose pool reads with
// default_transaction_read_only on, as a standby refuses writes, and whose
// rows differ from the primary's, so each read shows the pool it ran on
// (D150). ConnectWithPools over both: the Database's repositories, raw
// reads and transactions run on the primary; ReadReplica's repositories,
// declared reads and raw reads run on the replica, and a write through it
// fails with SQLSTATE 25006; PingReadReplica pings the replica; Close
// closes both pools. ConnectWithPools with no replica, or with the primary
// twice, has none: ReadReplica is the Database itself, and reads on the
// primary. With no database, the no-op database has no replica and is its
// own.
func TestGeneratedReadReplicaOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	ormDir := generateORMModule(t, "fixture-optimistic-db")
	if err := os.WriteFile(filepath.Join(ormDir, "read_replica_test.go"), []byte(readReplicaORMTest), 0o644); err != nil {
		t.Fatalf("write read replica test: %v", err)
	}
	out := runORMModule(t, ormDir)
	if !strings.Contains(out, "--- PASS: TestNoOpReadReplica") {
		t.Fatalf("the generated ORM did not run TestNoOpReadReplica:\n%s", out)
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" && !strings.Contains(out, "--- PASS: TestReadReplicaOnPostgres") {
		t.Fatalf("the generated ORM did not run TestReadReplicaOnPostgres:\n%s", out)
	}
}

// replicaTestSetup is the start of a generated test that reads through a
// read replica: two schemas of their own on the Postgres the environment
// names, each with create.sql applied, primary over the first, and
// replica over the second, read only as a standby is. seed writes to the
// replica's schema, as the primary's replication would. %s names the
// schemas' prefix.
const replicaTestSetup = `
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to read through a replica on Postgres")
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
	stamp := time.Now().UnixNano()
	primarySchema := fmt.Sprintf("PREFIX_primary_%d", stamp)
	replicaSchema := fmt.Sprintf("PREFIX_replica_%d", stamp)
	// schemaPool is a pool over one schema; pgcrypto, which the tables
	// take, is in public. A read-only one begins every transaction READ
	// ONLY, so Postgres refuses its writes with SQLSTATE 25006.
	schemaPool := func(schema string, readOnly bool) *pgxpool.Pool {
		t.Helper()
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatalf("parse database URL: %v", err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
		if readOnly {
			cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
		}
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatalf("connect %s pool: %v", schema, err)
		}
		return pool
	}
	for _, schema := range []string{primarySchema, replicaSchema} {
		if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
			t.Fatalf("create schema: %v", err)
		}
		defer func(schema string) { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }(schema)
		pool := schemaPool(schema, false)
		_, err := pool.Exec(ctx, string(createSQL))
		pool.Close()
		if err != nil {
			t.Fatalf("apply create.sql to %s: %v", schema, err)
		}
	}
	seed := schemaPool(replicaSchema, false)
	defer seed.Close()
	primary := schemaPool(primarySchema, false)
	defer primary.Close()
	replica := schemaPool(replicaSchema, true)
	defer replica.Close()
`

var readReplicaORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-optimistic-db"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// shelfLabels are the labels of the shelves a database's repository finds,
// in order.
func shelfLabels(t *testing.T, ctx context.Context, db DatabaseInterface) []string {
	t.Helper()
	shelves, _, err := db.GetShelfRepository().FindMany(ctx, nil, &ShelfFindOptions{Fields: ShelfFields{Label: true}})
	if err != nil {
		t.Fatalf("find shelves: %v", err)
	}
	labels := make([]string, 0, len(shelves))
	for _, shelf := range shelves {
		labels = append(labels, shelf.Label)
	}
	sort.Strings(labels)
	return labels
}

func TestReadReplicaOnPostgres(t *testing.T) {` + strings.Replace(replicaTestSetup, "PREFIX", "read_replica_orm", 1) + `
	ctx = WithUserID(ctx, types.NewUUID())
	if _, err := seed.Exec(ctx, "INSERT INTO shelf (label, created_by, updated_by) VALUES ('Replica', gen_random_uuid(), gen_random_uuid())"); err != nil {
		t.Fatalf("seed the replica: %v", err)
	}

	db, err := ConnectWithPools(primary, replica)
	if err != nil {
		t.Fatalf("connect with pools: %v", err)
	}
	if !db.HasReadReplica() {
		t.Fatal("a Database over two pools has no read replica")
	}

	// The Database's own repositories and transactions write and read on
	// the primary.
	if _, err := db.Shelf.CreateOne(ctx, &types.Shelf{Label: "Primary"}); err != nil {
		t.Fatalf("create on the primary: %v", err)
	}
	err = db.Transaction(ctx, func(tx TxInterface) error {
		_, err := tx.GetShelfRepository().CreateOne(ctx, &types.Shelf{Label: "Committed"})
		return err
	})
	if err != nil {
		t.Fatalf("transaction on the primary: %v", err)
	}
	if got := fmt.Sprint(shelfLabels(t, ctx, db)); got != "[Committed Primary]" {
		t.Fatalf("the primary's shelves = %s, want [Committed Primary]", got)
	}
	var count int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM shelf").Scan(&count); err != nil || count != 2 {
		t.Fatalf("a raw read on the Database counted %d, %v; want the primary's 2", count, err)
	}

	// ReadReplica reads the replica's rows, through its repositories and
	// its raw reads.
	replicaDB := db.ReadReplica()
	if got := fmt.Sprint(shelfLabels(t, ctx, replicaDB)); got != "[Replica]" {
		t.Fatalf("the replica's shelves = %s, want [Replica]", got)
	}
	onReplica, ok := replicaDB.(*Database)
	if !ok {
		t.Fatalf("ReadReplica is %T, want *Database", replicaDB)
	}
	var label string
	if err := onReplica.QueryRow(ctx, "SELECT label FROM shelf").Scan(&label); err != nil || label != "Replica" {
		t.Fatalf("a raw read on the replica = %q, %v; want Replica", label, err)
	}
	// The replica has no replica of its own: it reads on itself.
	if onReplica.HasReadReplica() || onReplica.ReadReplica() != replicaDB {
		t.Fatal("the replica's Database reads through another replica")
	}

	// A write through the replica fails as the replica refuses it.
	var pgErr *pgconn.PgError
	if _, err := replicaDB.GetShelfRepository().CreateOne(ctx, &types.Shelf{Label: "Refused"}); !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Fatalf("a write through the replica = %v, want SQLSTATE 25006", err)
	}
	if got := fmt.Sprint(shelfLabels(t, ctx, db)); got != "[Committed Primary]" {
		t.Fatalf("after the refused write the primary's shelves = %s, want [Committed Primary]", got)
	}

	if err := db.PingReadReplica(ctx); err != nil {
		t.Fatalf("ping the replica: %v", err)
	}

	// Close closes the replica's pool with the primary's.
	db.Close()
	if err := replica.Ping(ctx); err == nil {
		t.Fatal("the replica's pool is open after Close")
	}
	if err := primary.Ping(ctx); err == nil {
		t.Fatal("the primary's pool is open after Close")
	}

	// With no replica, or the primary given twice, there is none: the
	// Database reads on the primary.
	for name, second := range map[string]func(*pgxpool.Pool) *pgxpool.Pool{
		"no replica":  func(*pgxpool.Pool) *pgxpool.Pool { return nil },
		"the primary": func(p *pgxpool.Pool) *pgxpool.Pool { return p },
	} {
		pool := schemaPool(primarySchema, false)
		alone, err := ConnectWithPools(pool, second(pool))
		if err != nil {
			t.Fatalf("%s: connect with pools: %v", name, err)
		}
		if alone.HasReadReplica() {
			t.Fatalf("%s: the Database has a read replica", name)
		}
		if alone.ReadReplica() != DatabaseInterface(alone) {
			t.Fatalf("%s: ReadReplica is not the Database itself", name)
		}
		if got := fmt.Sprint(shelfLabels(t, ctx, alone.ReadReplica())); got != "[Committed Primary]" {
			t.Fatalf("%s: ReadReplica's shelves = %s, want the primary's", name, got)
		}
		if err := alone.PingReadReplica(ctx); err != nil {
			t.Fatalf("%s: ping: %v", name, err)
		}
		alone.Close()
	}
}

func TestNoOpReadReplica(t *testing.T) {
	db := NewNoOpDatabase()
	if db.HasReadReplica() {
		t.Fatal("the no-op database has a read replica")
	}
	if db.ReadReplica() != DatabaseInterface(db) {
		t.Fatal("the no-op database's ReadReplica is not itself")
	}
}
`
