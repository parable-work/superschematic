package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/parable-work/superschematic/runtime/versiongraph/go/postgres"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/storage"
)

var fixtureDir = filepath.Join("..", "..", "testdata", "fixture")

func readDescriptor(t *testing.T) json.RawMessage {
	t.Helper()
	descriptor, err := os.ReadFile(filepath.Join(fixtureDir, "recipe.json"))
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

// TestNew: the adapter takes the fixture's descriptor and refuses one that
// leaves out what it builds statements from: a graph table, a kind's root
// column, or a role column the kind's columns do not declare.
func TestNew(t *testing.T) {
	kind := func(d map[string]any) map[string]any { return d["kinds"].([]any)[0].(map[string]any) }
	for _, c := range []struct {
		name   string
		edit   func(d map[string]any)
		refuse string
	}{
		{"the fixture's descriptor", func(map[string]any) {}, ""},
		{"an empty refTable", func(d map[string]any) { d["refTable"] = "" }, "refTable is empty"},
		{"an empty commitTable", func(d map[string]any) { d["commitTable"] = "" }, "commitTable is empty"},
		{"an empty patchTable", func(d map[string]any) { d["patchTable"] = "" }, "patchTable is empty"},
		{"an empty releaseTable", func(d map[string]any) { d["releaseTable"] = "" }, "releaseTable is empty"},
		{"an empty snapshotTable", func(d map[string]any) { d["snapshotTable"] = "" }, "snapshotTable is empty"},
		{"a kind without a root column", func(d map[string]any) { delete(kind(d), "root") }, "has no root"},
		{"a role column missing from the kind's columns", func(d map[string]any) {
			delete(kind(d)["columns"].(map[string]any), "_version")
		}, `version column "_version" is not in its columns`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var descriptor map[string]any
			if err := json.Unmarshal(readDescriptor(t), &descriptor); err != nil {
				t.Fatal(err)
			}
			c.edit(descriptor)
			_, err := postgres.New(mustJSON(t, descriptor), postgres.Options{})
			switch {
			case c.refuse == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.refuse != "" && (err == nil || !strings.Contains(err.Error(), c.refuse)):
				t.Fatalf("New = %v, want it refused with %q", err, c.refuse)
			}
		})
	}
}

// connect opens a connection whose search path is a schema of its own that
// holds the fixture's DDL, and returns a second connection to the same
// schema.
func connect(t *testing.T) (*pgx.Conn, *pgx.Conn) {
	t.Helper()
	dsn := os.Getenv("SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL to run the Postgres adapter against Postgres")
	}
	ctx := context.Background()
	createSQL, err := os.ReadFile(filepath.Join(fixtureDir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	// The fixture's DDL creates pgcrypto if it is missing. An extension's
	// name is unique in the database, so create it once in public, where
	// every schema's search path finds it, before test packages running in
	// parallel each try to create it in their own schema.
	if _, err := admin.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public"); err != nil && !isUniqueViolation(err) {
		t.Fatalf("create pgcrypto: %v", err)
	}
	schema := fmt.Sprintf("vg_adapter_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	open := func() *pgx.Conn {
		config, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		config.RuntimeParams["search_path"] = schema + ",public"
		conn, err := pgx.ConnectConfig(ctx, config)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		return conn
	}
	first := open()
	if _, err := first.Exec(ctx, string(createSQL)); err != nil {
		t.Fatalf("apply the fixture's DDL: %v", err)
	}
	return first, open()
}

func adapter(t *testing.T) *postgres.Adapter {
	t.Helper()
	a, err := postgres.New(readDescriptor(t), postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestPruneKeepsPinnedImages: a prune past every image's retention deletes
// the superseded image no commit pins and keeps the pinned one, and a kind
// declared without retentionDays has no prune function and prunes nothing.
func TestPruneKeepsPinnedImages(t *testing.T) {
	conn, _ := connect(t)
	ctx := context.Background()
	s := adapter(t).Storage(postgres.Pgx(conn))
	if _, err := conn.Exec(ctx, "INSERT INTO recipe (id, title, created_by) VALUES ('00000000-0000-0000-0000-000000000001', 'Bread', '00000000-0000-0000-0000-000000000002')"); err != nil {
		t.Fatal(err)
	}
	const actor = "2"
	var ref storage.Ref
	var pinned json.RawMessage
	err := s.Transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		var err error
		if ref, err = tx.CreateRef(ctx, storage.NewRef{Root: "1", Name: "main", Actor: actor}); err != nil {
			return err
		}
		for _, instruction := range []string{"Mix", "Mix well", "Mix gently"} {
			row := json.RawMessage(`{"entity_key": "Mix", "position": 1, "instruction": "` + instruction + `", "timings": {}}`)
			stored, err := tx.UpsertRow(ctx, "step", storage.RowWrite{Ref: ref.ID, Root: ref.Root, Row: row, Actor: actor})
			if err != nil {
				return err
			}
			if instruction == "Mix well" {
				pinned = stored
			}
		}
		if _, err := tx.UpsertRow(ctx, "cover", storage.RowWrite{Ref: ref.ID, Root: ref.Root, Row: json.RawMessage(`{"photo_url": "a.jpg"}`), Actor: actor}); err != nil {
			return err
		}
		var row struct {
			ID      string `json:"id"`
			Version int64  `json:"_version"`
		}
		if err := json.Unmarshal(pinned, &row); err != nil {
			return err
		}
		commit, err := tx.InsertCommit(ctx, storage.NewCommit{Root: ref.Root, Ref: ref.ID, ContentHash: "h", Actor: actor})
		if err != nil {
			return err
		}
		return tx.InsertPatches(ctx, commit.ID, []storage.Patch{{Kind: "step", EntityKey: "Mix", EntityID: row.ID, EntityVersion: row.Version, Operation: "ADD"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"step_history", "cover_history"} {
		if _, err := conn.Exec(ctx, "UPDATE "+table+" SET recorded_at = now() - interval '400 days'"); err != nil {
			t.Fatal(err)
		}
	}
	var stepsPruned, coversPruned int64
	err = s.Transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		var err error
		if stepsPruned, err = tx.Prune(ctx, "step", 365, 0); err != nil {
			return err
		}
		coversPruned, err = tx.Prune(ctx, "cover", 365, 0)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if stepsPruned != 1 || coversPruned != 0 {
		t.Fatalf("pruned %d step and %d cover images, want the unpinned superseded step image alone", stepsPruned, coversPruned)
	}
	var instructions []string
	rows, err := conn.Query(ctx, "SELECT data->>'instruction' FROM step_history ORDER BY _version")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var instruction string
		if err := rows.Scan(&instruction); err != nil {
			t.Fatal(err)
		}
		instructions = append(instructions, instruction)
	}
	rows.Close()
	if strings.Join(instructions, ",") != "Mix well,Mix gently" {
		t.Fatalf("history keeps %q, want the pinned image and the latest", instructions)
	}
}

// TestSweepLockIsHeldByOneTransaction: while one transaction holds the
// graph's sweep lock, another does not get it and does not wait; once the
// first ends, the lock is free.
func TestSweepLockIsHeldByOneTransaction(t *testing.T) {
	first, second := connect(t)
	ctx := context.Background()
	a := adapter(t)
	holder, other := a.Storage(postgres.Pgx(first)), a.Storage(postgres.Pgx(second))
	take := func(s storage.Storage) bool {
		var locked bool
		if err := s.Transact(ctx, func(ctx context.Context, tx storage.Tx) error {
			var err error
			locked, err = tx.SweepLock(ctx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return locked
	}
	err := holder.Transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		locked, err := tx.SweepLock(ctx)
		if err != nil {
			return err
		}
		if !locked {
			t.Error("the first transaction did not get a free sweep lock")
		}
		if take(other) {
			t.Error("a second transaction got the sweep lock the first holds")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !take(other) {
		t.Fatal("the sweep lock stayed held after its transaction ended")
	}
}

// TestPgxOverAnOpenTransaction: bound to a transaction the caller holds,
// the adapter's transaction is a savepoint inside it, so what it writes
// rolls back with the caller's transaction.
func TestPgxOverAnOpenTransaction(t *testing.T) {
	conn, _ := connect(t)
	ctx := context.Background()
	if _, err := conn.Exec(ctx, "INSERT INTO recipe (id, title, created_by) VALUES ('00000000-0000-0000-0000-000000000001', 'Bread', '00000000-0000-0000-0000-000000000002')"); err != nil {
		t.Fatal(err)
	}
	outer, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s := adapter(t).Storage(postgres.Pgx(outer))
	if err := s.Transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		_, err := tx.CreateRef(ctx, storage.NewRef{Root: "1", Name: "main", Actor: "2"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var inside int
	if err := outer.QueryRow(ctx, "SELECT count(*) FROM recipe_ref").Scan(&inside); err != nil || inside != 1 {
		t.Fatalf("the caller's transaction sees %d refs (%v), want the one the adapter wrote", inside, err)
	}
	if err := outer.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM recipe_ref").Scan(&after); err != nil || after != 0 {
		t.Fatalf("%d refs after the caller rolled back (%v), want none", after, err)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// isUniqueViolation reports whether err is Postgres's unique_violation: a
// concurrent CREATE EXTENSION that another session won.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
