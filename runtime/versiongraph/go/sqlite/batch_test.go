package sqlite_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/runtime/versiongraph/go/sqlite"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/storage"
)

// countingClient is a Client that keeps the statements its transactions
// execute.
type countingClient struct {
	inner sqlite.Client
	execs []string
}

func (c *countingClient) Transact(ctx context.Context, fn func(ctx context.Context, conn sqlite.Conn) error) error {
	return c.inner.Transact(ctx, func(ctx context.Context, conn sqlite.Conn) error {
		return fn(ctx, &countingConn{inner: conn, client: c})
	})
}

// count is how many statements executed since the last count began with
// prefix, and starts the next count.
func (c *countingClient) count(prefix string) int {
	n := 0
	for _, sql := range c.execs {
		if strings.HasPrefix(sql, prefix) {
			n++
		}
	}
	c.execs = nil
	return n
}

type countingConn struct {
	inner  sqlite.Conn
	client *countingClient
}

func (c *countingConn) Query(ctx context.Context, sql string, args []any, row func(scan func(dest ...any) error) error) error {
	return c.inner.Query(ctx, sql, args, row)
}

func (c *countingConn) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	c.client.execs = append(c.client.execs, sql)
	return c.inner.Exec(ctx, sql, args...)
}

// TestBatchedWrites: patches, a snapshot entry and released rows, more of
// each than one statement takes, are written 100 to a statement, in the
// order given, and read back as given, each column of the type one
// statement a row stored. A released key the upserts repeat takes its last
// row.
func TestBatchedWrites(t *testing.T) {
	db := openDB(t, "", "")
	client := &countingClient{inner: sqlite.DB(db)}
	s := setupOn(t, db, client, sqlite.Options{}, readDescriptor(t))
	const n = 250
	operations := []string{"ADD", "UPDATE", "DELETE"}
	var patches []storage.Patch
	var entries []storage.SnapshotEntry
	var upserts []json.RawMessage
	var deletes []string
	for i := range n {
		// Keys of digits alone, which stay text.
		key, id := strconv.Itoa(i+1), strconv.Itoa(1000+i)
		patches = append(patches, storage.Patch{Kind: "step", EntityKey: key, EntityID: id, EntityVersion: int64(i + 1), Operation: operations[i%3]})
		entries = append(entries, storage.SnapshotEntry{Kind: "step", EntityKey: key, EntityID: id, EntityVersion: int64(i + 1)})
		upserts = append(upserts, mustJSON(t, map[string]any{"entity_key": key, "heading": "Part " + key, "rank": key}))
		if i < 150 {
			deletes = append(deletes, key)
		}
	}
	upserts = append(upserts, mustJSON(t, map[string]any{"entity_key": "1", "heading": "Again", "rank": "1"}))
	ctx := context.Background()
	err := s.storage.Transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		_, commit, err := refAndCommit(ctx, tx, bread, "batch")
		if err != nil {
			return err
		}
		client.count("")
		if err := tx.InsertPatches(ctx, commit, patches); err != nil {
			return err
		}
		if got := client.count(`INSERT INTO "graph_patch"`); got != 3 {
			return fmt.Errorf("%d patch INSERTs, want 3", got)
		}
		if err := tx.InsertSnapshot(ctx, commit, entries); err != nil {
			return err
		}
		if got := client.count(`INSERT INTO "graph_snapshot_entry"`); got != 3 {
			return fmt.Errorf("%d snapshot INSERTs, want 3", got)
		}
		if err := tx.WriteReleased(ctx, "part", storage.ReleasedWrite{Root: bread, Commit: commit, Sequence: 7, Upsert: upserts}); err != nil {
			return err
		}
		if got := client.count(`INSERT INTO "graph_released"`); got != 3 {
			return fmt.Errorf("%d released INSERTs, want 3", got)
		}
		if err := tx.WriteReleased(ctx, "part", storage.ReleasedWrite{Root: bread, Commit: commit, Sequence: 8, Delete: deletes}); err != nil {
			return err
		}
		if got := client.count(`DELETE FROM "graph_released"`); got != 2 {
			return fmt.Errorf("%d released DELETEs, want 2", got)
		}
		gotPatches, err := tx.Patches(ctx, []string{commit})
		if err != nil {
			return err
		}
		gotEntries, err := tx.Snapshot(ctx, commit)
		if err != nil {
			return err
		}
		for i := range gotPatches {
			gotPatches[i].Commit = ""
		}
		sort.Slice(gotPatches, func(i, j int) bool { return gotPatches[i].EntityVersion < gotPatches[j].EntityVersion })
		sort.Slice(gotEntries, func(i, j int) bool { return gotEntries[i].EntityVersion < gotEntries[j].EntityVersion })
		if fmt.Sprint(gotPatches) != fmt.Sprint(patches) {
			return fmt.Errorf("patches read back as %v, want %v", gotPatches, patches)
		}
		if fmt.Sprint(gotEntries) != fmt.Sprint(entries) {
			return fmt.Errorf("snapshot read back as %v, want %v", gotEntries, entries)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"graph_patch", "graph_snapshot_entry"} {
		rows, err := db.Query(`SELECT entity_key, typeof(entity_key), typeof(entity_version) FROM "` + table + `" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		i := 0
		for rows.Next() {
			var key, keyType, versionType string
			if err := rows.Scan(&key, &keyType, &versionType); err != nil {
				t.Fatal(err)
			}
			if want := strconv.Itoa(i + 1); key != want || keyType != "text" || versionType != "integer" {
				t.Fatalf("%s row %d: key %q (%s), version %s; want key %q (text), version integer", table, i, key, keyType, versionType, want)
			}
			i++
		}
		_ = rows.Close()
		if i != n {
			t.Fatalf("%s holds %d rows, want %d", table, i, n)
		}
	}
	rows, err := db.Query(`SELECT entity_key, typeof(entity_key), sequence, typeof(sequence), data FROM "graph_released" ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	i := 150
	for rows.Next() {
		var key, keyType, sequenceType, data string
		var sequence int64
		if err := rows.Scan(&key, &keyType, &sequence, &sequenceType, &data); err != nil {
			t.Fatal(err)
		}
		want := strconv.Itoa(i + 1)
		wantData := `{"heading":"Part ` + want + `","parent_key":null,"rank":"` + want + `"}`
		if key != want || keyType != "text" || sequence != 7 || sequenceType != "integer" || data != wantData {
			t.Fatalf("released row %d: %q (%s), sequence %d (%s), %s; want %q (text), 7 (integer), %s", i, key, keyType, sequence, sequenceType, data, want, wantData)
		}
		i++
	}
	if i != n {
		t.Fatalf("%d released rows, want %d", i-150, n-150)
	}
}

// TestRepeatedReleasedKey: a key the upserts repeat keeps its first row's
// place and takes its last row, as one statement a row left it.
func TestRepeatedReleasedKey(t *testing.T) {
	s := newSetup(t, sqlite.Options{}, "")
	upserts := []json.RawMessage{
		mustJSON(t, map[string]any{"entity_key": "1", "heading": "First", "rank": "a"}),
		mustJSON(t, map[string]any{"entity_key": "2", "heading": "Second", "rank": "b"}),
		mustJSON(t, map[string]any{"entity_key": "1", "heading": "Again", "rank": "c"}),
	}
	must(in(s.storage, func(ctx context.Context, tx storage.Tx) (struct{}, error) {
		_, commit, err := refAndCommit(ctx, tx, bread, "batch")
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, tx.WriteReleased(ctx, "part", storage.ReleasedWrite{Root: bread, Commit: commit, Sequence: 1, Upsert: upserts})
	}))(t)
	var got []string
	rows, err := s.db.Query(`SELECT entity_key || ' ' || data FROM "graph_released" ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	want := []string{
		`1 {"heading":"Again","parent_key":null,"rank":"c"}`,
		`2 {"heading":"Second","parent_key":null,"rank":"b"}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("released rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
