package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/parable-work/superschematic/runtime/versiongraph/go/canonical"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/engine"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/sqlite"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/storage"
)

var fixtureDir = filepath.Join("..", "..", "testdata", "fixture")

// The graph the tests keep the fixture's Recipe graph under, the default
// actor, and the root most tests write.
const (
	graph = "recipe"
	cook  = "Cook"
	bread = "Bread"
)

// readDescriptor is the scenarios' graph descriptor, fixture-version-graph-db's
// Recipe graph.
func readDescriptor(t *testing.T) json.RawMessage {
	t.Helper()
	descriptor, err := os.ReadFile(filepath.Join(fixtureDir, "recipe.json"))
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

// descriptorDoc is the fixture's descriptor, decoded to edit.
func descriptorDoc(t *testing.T) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(readDescriptor(t), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// kindOf is the kind of a decoded descriptor named name.
func kindOf(t *testing.T, d map[string]any, name string) map[string]any {
	t.Helper()
	for _, k := range d["kinds"].([]any) {
		if k := k.(map[string]any); k["kind"] == name {
			return k
		}
	}
	t.Fatalf("no kind %q", name)
	return nil
}

// columns are each kind's columns' value classes, as the fixture declares
// them.
func columns(t *testing.T, kind string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for column, class := range kindOf(t, descriptorDoc(t), kind)["columns"].(map[string]any) {
		out[column] = class.(string)
	}
	return out
}

// openDB opens a SQLite file through modernc.org/sqlite: path, or a file of
// its own when path is "". The DSN's query (busy_timeout(5000) when "")
// sets the connections' pragmas.
func openDB(t *testing.T, path, query string) *sql.DB {
	t.Helper()
	if path == "" {
		path = filepath.Join(t.TempDir(), "graph.sqlite")
	}
	if query == "" {
		query = "_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", path+"?"+query)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// setup is a database with the layout, and the adapter's storage and an
// engine over it.
type setup struct {
	db      *sql.DB
	client  sqlite.Client
	adapter *sqlite.Adapter
	storage storage.Storage
	engine  *engine.Engine
}

// newSetup opens a database (path, or a file of its own), creates the
// layout through the database/sql pool's binding, and builds the adapter
// with opts (graph recipe unless they name one) and an engine at the
// fixture's schema epoch and snapshot interval.
func newSetup(t *testing.T, opts sqlite.Options, path string) *setup {
	t.Helper()
	db := openDB(t, path, "")
	return setupOn(t, db, sqlite.DB(db), opts, readDescriptor(t))
}

func setupOn(t *testing.T, db *sql.DB, client sqlite.Client, opts sqlite.Options, descriptor json.RawMessage) *setup {
	t.Helper()
	ctx := context.Background()
	if opts.Graph == "" {
		opts.Graph = graph
	}
	adapter, err := sqlite.New(descriptor, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.CreateTables(ctx, client); err != nil {
		t.Fatal(err)
	}
	s, err := adapter.Storage(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(descriptor, s, engine.Options{SchemaEpoch: 1, SnapshotEvery: 3})
	if err != nil {
		t.Fatal(err)
	}
	return &setup{db: db, client: client, adapter: adapter, storage: s, engine: eng}
}

// in runs fn in one transaction of s and returns its value.
func in[T any](s storage.Storage, fn func(ctx context.Context, tx storage.Tx) (T, error)) (T, error) {
	var out T
	err := s.Transact(context.Background(), func(ctx context.Context, tx storage.Tx) error {
		var err error
		out, err = fn(ctx, tx)
		return err
	})
	return out, err
}

// must returns v, failing the test on err: must(f())(t).
func must[T any](v T, err error) func(t *testing.T) T {
	return func(t *testing.T) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

// member is a JSON object's member, as its JSON text, and whether it has
// it.
func member(t *testing.T, row any, name string) (string, bool) {
	t.Helper()
	var text []byte
	switch r := row.(type) {
	case string:
		text = []byte(r)
	case json.RawMessage:
		text = r
	default:
		t.Fatalf("member of %T", row)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(text, &members); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	value, ok := members[name]
	return string(value), ok
}

// memberText is member, failing the test when the object lacks it.
func memberText(t *testing.T, row any, name string) string {
	t.Helper()
	value, ok := member(t, row, name)
	if !ok {
		t.Fatalf("%v has no %s", row, name)
	}
	return value
}

// stepRow is a step row of the fixture, as an upsert's JSON; a nil key is
// none.
func stepRow(t *testing.T, key any, instruction string, extra ...map[string]any) json.RawMessage {
	t.Helper()
	row := map[string]any{"entity_key": key, "position": 1, "instruction": instruction, "timings": map[string]any{}}
	for _, e := range extra {
		for k, v := range e {
			row[k] = v
		}
	}
	return mustJSON(t, row)
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// image is one history image as the layout holds it.
type image struct {
	version    int64
	operation  string
	data       string
	recordedAt int64
}

// historyOf reads a history table's images of an id, by version.
func historyOf(t *testing.T, db *sql.DB, table, id string) []image {
	t.Helper()
	rows, err := db.Query(`SELECT _version, operation, data, recorded_at FROM `+table+` WHERE id = ?1 ORDER BY _version`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []image
	for rows.Next() {
		var i image
		if err := rows.Scan(&i.version, &i.operation, &i.data, &i.recordedAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// queryInts reads a statement's one integer column.
func queryInts(t *testing.T, q interface {
	Query(string, ...any) (*sql.Rows, error)
}, statement string, args ...any) []int64 {
	t.Helper()
	rows, err := q.Query(statement, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// count reads a statement's one count.
func count(t *testing.T, db *sql.DB, statement string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(statement, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// assertCanonicalRow fails unless a row the adapter returned is a
// canonical row of its kind: the canonical rules leave it as it is, its
// members sorted.
func assertCanonicalRow(t *testing.T, kind string, row any) {
	t.Helper()
	text := fmt.Sprint(row)
	if r, ok := row.(json.RawMessage); ok {
		text = string(r)
	}
	out, err := canonical.Row(columns(t, kind), json.RawMessage(text))
	if err != nil {
		t.Fatalf("a %s row: %v", kind, err)
	}
	if string(out) != text {
		t.Fatalf("a %s row is not canonical:\n%s\nis canonically\n%s", kind, text, out)
	}
}

var v4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// assertCanonicalID fails unless id is a version-4 UUID in its canonical
// form, base62.
func assertCanonicalID(t *testing.T, id, what string) {
	t.Helper()
	out, err := canonical.Postgres(canonical.UUID, mustJSON(t, id))
	if err != nil || string(out) != string(mustJSON(t, id)) {
		t.Fatalf("%s %q is not in its canonical form (%s, %v)", what, id, out, err)
	}
	n := new(big.Int)
	for _, c := range []byte(id) {
		n.Mul(n, big.NewInt(62))
		n.Add(n, big.NewInt(int64(strings.IndexByte(base62Alphabet, c))))
	}
	hex := fmt.Sprintf("%032x", n)
	hyphenated := hex[0:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:32]
	if !v4.MatchString(hyphenated) {
		t.Fatalf("%s %q (%s) is not a version-4 UUID", what, id, hyphenated)
	}
}

// refAndCommit writes a ref and a tagged commit on it through the adapter,
// for the tests that need a commit.
func refAndCommit(ctx context.Context, tx storage.Tx, root, name string) (storage.Ref, string, error) {
	ref, err := tx.CreateRef(ctx, storage.NewRef{Root: root, Name: name, Actor: cook})
	if err != nil {
		return storage.Ref{}, "", err
	}
	sequence, err := tx.NextSequence(ctx, root)
	if err != nil {
		return storage.Ref{}, "", err
	}
	commit, err := tx.InsertCommit(ctx, storage.NewCommit{
		Root: root, Ref: ref.ID, SchemaEpoch: 1, ContentHash: strings.Repeat("0", 64), Sequence: &sequence, Actor: cook,
	})
	if err != nil {
		return storage.Ref{}, "", err
	}
	return ref, commit.ID, nil
}

// errorContains fails unless err's message holds want.
func errorContains(t *testing.T, err error, want, what string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%s: %v, want an error with %q", what, err, want)
	}
}
