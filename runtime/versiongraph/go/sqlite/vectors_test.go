package sqlite_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	versiongraph "github.com/parable-work/superschematic/runtime/versiongraph/go"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/engine"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/sqlite"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/storage"
)

// The shared SQLite vectors (runtime/versiongraph/testdata/sqlite), which
// the TypeScript adapter wrote: its layout's statements, a database it
// wrote as SQL text, and what that database reads back as.
var vectorsDir = filepath.Join("..", "..", "testdata", "sqlite")

// TestLayoutVector: the layout's statements under the default names are
// layout.json's, string for string.
func TestLayoutVector(t *testing.T) {
	var file struct {
		Statements []string `json:"statements"`
	}
	text, err := os.ReadFile(filepath.Join(vectorsDir, "layout.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(text, &file); err != nil {
		t.Fatal(err)
	}
	statements := must(sqlite.Layout(nil))(t)
	if len(statements) != len(file.Statements) {
		t.Fatalf("the layout is %d statements, layout.json %d", len(statements), len(file.Statements))
	}
	for i := range statements {
		if statements[i] != file.Statements[i] {
			t.Fatalf("statement %d is\n%s\nwhere layout.json has\n%s", i, statements[i], file.Statements[i])
		}
	}
}

// vectorFile is typescript.json, with every read it lists as the JSON it
// holds.
type vectorFile struct {
	Graphs []struct {
		Graph string `json:"graph"`
		Refs  []struct {
			ID      string                       `json:"id"`
			ReadRef json.RawMessage              `json:"readRef"`
			Rows    map[string][]json.RawMessage `json:"rows"`
			Compose json.RawMessage              `json:"compose"`
			History json.RawMessage              `json:"history"`
		} `json:"refs"`
		Commits []struct {
			ID          string          `json:"id"`
			ReadCommit  json.RawMessage `json:"readCommit"`
			Materialize json.RawMessage `json:"materialize"`
			Patches     json.RawMessage `json:"patches"`
			Snapshot    json.RawMessage `json:"snapshot"`
		} `json:"commits"`
		Roots []struct {
			Root     string          `json:"root"`
			Released json.RawMessage `json:"released"`
		} `json:"roots"`
		Images []struct {
			Kind    string          `json:"kind"`
			ID      string          `json:"id"`
			Version int64           `json:"version"`
			Image   json.RawMessage `json:"image"`
		} `json:"images"`
	} `json:"graphs"`
}

// loadTypeScriptDatabase loads typescript.sql into a file of its own, one
// statement per line, with foreign keys off, since a ref names its head
// commit and the commit names its ref; then it turns them on and checks
// that no row breaks one. It returns the file's pool.
func loadTypeScriptDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	text, err := os.ReadFile(filepath.Join(vectorsDir, "typescript.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db := openDB(t, "", "")
	conn := must(db.Conn(ctx))(t)
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}
	statements := 0
	for _, line := range strings.Split(string(text), "\n") {
		if line == "" {
			continue
		}
		if _, err := conn.ExecContext(ctx, line); err != nil {
			t.Fatalf("load %s: %v", line, err)
		}
		statements++
	}
	if statements == 0 {
		t.Fatal("typescript.sql holds no statements")
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	broken := rows.Next()
	_ = rows.Close()
	if broken {
		t.Fatal("a row of typescript.sql breaks a foreign key")
	}
	return db
}

// readVectors reads typescript.json.
func readVectors(t *testing.T) vectorFile {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(vectorsDir, "typescript.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file vectorFile
	if err := json.Unmarshal(text, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Graphs) < 2 {
		t.Fatalf("typescript.json holds %d graphs, want two", len(file.Graphs))
	}
	return file
}

// sameValue compares two JSON texts as values, every number by its text,
// so a number a double does not hold compares exactly.
func sameValue(t *testing.T, a, b []byte) bool {
	t.Helper()
	decode := func(text []byte) any {
		d := json.NewDecoder(bytes.NewReader(text))
		d.UseNumber()
		var v any
		if err := d.Decode(&v); err != nil {
			t.Fatalf("decode %s: %v", text, err)
		}
		return v
	}
	return reflect.DeepEqual(decode(a), decode(b))
}

// expectValue fails unless got, written as JSON, is want as a value.
func expectValue(t *testing.T, what string, want json.RawMessage, got any) {
	t.Helper()
	text := mustJSON(t, got)
	if !sameValue(t, want, text) {
		t.Errorf("%s reads as\n%s\nwhere typescript.json has\n%s", what, text, want)
	}
}

// rowsCompared counts the rows expectRows has compared, so a test can
// tell it compared some.
var rowsCompared int

// expectRows fails unless got are want's rows, byte for byte.
func expectRows(t *testing.T, what string, want, got []json.RawMessage) {
	t.Helper()
	rowsCompared += len(want)
	if len(got) != len(want) {
		t.Errorf("%s: %d rows, typescript.json %d:\n%s", what, len(got), len(want), mustJSON(t, got))
		return
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("%s[%d] reads as\n%s\nwhere typescript.json has\n%s", what, i, got[i], want[i])
		}
	}
}

// optionalID is an id as typescript.json writes it: null for none.
func optionalID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func refValue(r storage.Ref) map[string]any {
	return map[string]any{
		"id": r.ID, "root": r.Root, "parent": optionalID(r.Parent), "base": optionalID(r.Base), "head": optionalID(r.Head),
		"name": r.Name, "sealed": r.Sealed, "discarded": r.Discarded, "version": r.Version,
	}
}

func commitValue(c storage.Commit) map[string]any {
	var sequence any
	if c.Sequence != nil {
		sequence = *c.Sequence
	}
	return map[string]any{
		"id": c.ID, "root": c.Root, "ref": c.Ref, "parent": optionalID(c.Parent), "message": c.Message, "schemaEpoch": c.SchemaEpoch,
		"contentHash": c.ContentHash, "sequence": sequence, "createdAt": c.CreatedAt, "createdBy": c.CreatedBy, "snapshot": c.Snapshot,
	}
}

// errorValue is a read that failed, as typescript.json writes it.
func errorValue(t *testing.T, err error) map[string]any {
	t.Helper()
	code := engine.ErrorCode(err)
	if code == "" {
		t.Fatalf("a read failed with no engine error code: %v", err)
	}
	return map[string]any{"error": code}
}

// expectTree fails unless a tree result is want's: its kinds and their
// rows, byte for byte, its content hash and its findings.
func expectTree(t *testing.T, what string, want json.RawMessage, got *engine.TreeResult) {
	t.Helper()
	var w struct {
		Tree        map[string][]json.RawMessage `json:"tree"`
		ContentHash string                       `json:"contentHash"`
		Findings    json.RawMessage              `json:"findings"`
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	var kinds []string
	for kind, rows := range got.Tree {
		if len(rows) > 0 {
			kinds = append(kinds, kind)
		}
	}
	sort.Strings(kinds)
	var wantKinds []string
	for kind := range w.Tree {
		wantKinds = append(wantKinds, kind)
	}
	sort.Strings(wantKinds)
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Errorf("%s holds kinds %q, typescript.json %q", what, kinds, wantKinds)
	}
	for _, kind := range wantKinds {
		expectRows(t, what+" "+kind, w.Tree[kind], got.Tree[kind])
	}
	if got.ContentHash != w.ContentHash {
		t.Errorf("%s hashes %s, typescript.json %s", what, got.ContentHash, w.ContentHash)
	}
	findings := got.Findings
	if findings == nil {
		findings = []versiongraph.Finding{}
	}
	expectValue(t, what+"'s findings", w.Findings, findings)
}

// TestTypeScriptDatabase: the database the TypeScript adapter wrote
// (typescript.sql), loaded into a fresh file, reads through this adapter
// and the engine, opened with each graph's name, as typescript.json says;
// and through each graph's adapter nothing of another graph reads.
func TestTypeScriptDatabase(t *testing.T) {
	ctx := context.Background()
	db := loadTypeScriptDatabase(t)
	file := readVectors(t)
	rowsCompared = 0
	defer func() {
		t.Logf("compared %d rows with typescript.json's, byte for byte", rowsCompared)
		if rowsCompared < 100 {
			t.Errorf("compared %d rows with typescript.json's", rowsCompared)
		}
	}()
	descriptor := readDescriptor(t)
	kindNames := map[string]string{}
	for _, k := range descriptorDoc(t)["kinds"].([]any) {
		k := k.(map[string]any)
		kindNames[k["kind"].(string)] = k["key"].(string)
	}
	type opened struct {
		storage storage.Storage
		engine  *engine.Engine
	}
	open := func(graph string) opened {
		adapter := must(sqlite.New(descriptor, sqlite.Options{Graph: graph}))(t)
		s := must(adapter.Storage(ctx, sqlite.DB(db)))(t)
		return opened{s, must(engine.New(descriptor, s, engine.Options{SchemaEpoch: 1, SnapshotEvery: 3}))(t)}
	}
	read := func(o opened, fn func(ctx context.Context, tx storage.Tx) error) {
		t.Helper()
		if err := o.storage.Transact(ctx, fn); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range file.Graphs {
		if len(g.Refs) == 0 || len(g.Commits) == 0 || len(g.Roots) == 0 || len(g.Images) == 0 {
			t.Fatalf("typescript.json lists %d refs, %d commits, %d roots and %d images of %s", len(g.Refs), len(g.Commits), len(g.Roots), len(g.Images), g.Graph)
		}
		o := open(g.Graph)
		for _, ref := range g.Refs {
			what := g.Graph + " ref " + ref.ID
			read(o, func(ctx context.Context, tx storage.Tx) error {
				r, err := tx.ReadRef(ctx, ref.ID)
				if err != nil {
					return err
				}
				expectValue(t, what+": readRef", ref.ReadRef, refValue(r))
				if len(ref.Rows) != len(kindNames) {
					t.Errorf("%s lists rows of %d kinds, the descriptor declares %d", what, len(ref.Rows), len(kindNames))
				}
				for kind, want := range ref.Rows {
					rows, err := tx.Rows(ctx, kind, ref.ID)
					if err != nil {
						return err
					}
					key := kindNames[kind]
					keys := make([]string, len(rows))
					for i, row := range rows {
						var members map[string]json.RawMessage
						if err := json.Unmarshal(row, &members); err != nil {
							return err
						}
						if err := json.Unmarshal(members[key], &keys[i]); err != nil {
							return err
						}
					}
					sort.Sort(byKey{keys, rows})
					expectRows(t, what+": rows "+kind, want, rows)
				}
				return nil
			})
			if composed, err := o.engine.Compose(ctx, ref.ID); err != nil {
				expectValue(t, what+": compose", ref.Compose, errorValue(t, err))
			} else {
				expectTree(t, what+": compose", ref.Compose, composed)
			}
			if history, err := o.engine.History(ctx, ref.ID); err != nil {
				expectValue(t, what+": history", ref.History, errorValue(t, err))
			} else {
				values := []any{}
				for _, c := range history {
					values = append(values, commitValue(c))
				}
				expectValue(t, what+": history", ref.History, values)
			}
		}
		for _, commit := range g.Commits {
			what := g.Graph + " commit " + commit.ID
			read(o, func(ctx context.Context, tx storage.Tx) error {
				c, err := tx.ReadCommit(ctx, commit.ID)
				if err != nil {
					return err
				}
				expectValue(t, what+": readCommit", commit.ReadCommit, commitValue(c))
				patches, err := tx.Patches(ctx, []string{commit.ID})
				if err != nil {
					return err
				}
				sort.Slice(patches, func(i, j int) bool {
					if patches[i].Kind != patches[j].Kind {
						return patches[i].Kind < patches[j].Kind
					}
					return patches[i].EntityKey < patches[j].EntityKey
				})
				values := []any{}
				for _, p := range patches {
					values = append(values, map[string]any{
						"commit": p.Commit, "kind": p.Kind, "entityKey": p.EntityKey, "entityId": p.EntityID,
						"entityVersion": p.EntityVersion, "operation": p.Operation,
					})
				}
				expectValue(t, what+": patches", commit.Patches, values)
				entries, err := tx.Snapshot(ctx, commit.ID)
				if err != nil {
					return err
				}
				sort.Slice(entries, func(i, j int) bool {
					if entries[i].Kind != entries[j].Kind {
						return entries[i].Kind < entries[j].Kind
					}
					return entries[i].EntityKey < entries[j].EntityKey
				})
				values = []any{}
				for _, e := range entries {
					values = append(values, map[string]any{"kind": e.Kind, "entityKey": e.EntityKey, "entityId": e.EntityID, "entityVersion": e.EntityVersion})
				}
				expectValue(t, what+": snapshot", commit.Snapshot, values)
				return nil
			})
			tree := must(o.engine.Materialize(ctx, commit.ID))(t)
			expectTree(t, what+": materialize", commit.Materialize, tree)
		}
		for _, root := range g.Roots {
			what := g.Graph + " root " + root.Root
			released := must(o.engine.Released(ctx, root.Root))(t)
			var want struct {
				Release json.RawMessage `json:"release"`
			}
			if err := json.Unmarshal(root.Released, &want); err != nil {
				t.Fatal(err)
			}
			r := released.Release
			expectValue(t, what+": released release", want.Release, map[string]any{"id": r.ID, "root": r.Root, "commit": r.Commit, "version": r.Version})
			expectTree(t, what+": released", root.Released, &released.TreeResult)
		}
		read(o, func(ctx context.Context, tx storage.Tx) error {
			for _, image := range g.Images {
				images, err := tx.Images(ctx, image.Kind, []storage.Pin{{ID: image.ID, Version: image.Version}})
				if err != nil {
					return err
				}
				expectRows(t, g.Graph+" image "+image.Kind+" "+image.ID, []json.RawMessage{image.Image}, images)
			}
			var held int64
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM "graph_member_history" WHERE graph = ?1`, g.Graph).Scan(&held); err != nil {
				return err
			}
			if held != int64(len(g.Images)) {
				t.Errorf("%s holds %d images, typescript.json lists %d", g.Graph, held, len(g.Images))
			}
			return nil
		})
		// Through this graph's adapter, nothing of another graph reads.
		for _, other := range file.Graphs {
			if other.Graph == g.Graph {
				continue
			}
			read(o, func(ctx context.Context, tx storage.Tx) error {
				for _, ref := range other.Refs {
					if _, err := tx.ReadRef(ctx, ref.ID); !errors.Is(err, storage.ErrNotFound) {
						t.Errorf("%s reads %s's ref %s: %v", g.Graph, other.Graph, ref.ID, err)
					}
					for kind := range kindNames {
						if rows, err := tx.Rows(ctx, kind, ref.ID); err != nil || len(rows) != 0 {
							t.Errorf("%s reads %s's %s rows: %s %v", g.Graph, other.Graph, kind, rows, err)
						}
					}
				}
				for _, commit := range other.Commits {
					if _, err := tx.ReadCommit(ctx, commit.ID); !errors.Is(err, storage.ErrNotFound) {
						t.Errorf("%s reads %s's commit %s: %v", g.Graph, other.Graph, commit.ID, err)
					}
					if patches, err := tx.Patches(ctx, []string{commit.ID}); err != nil || len(patches) != 0 {
						t.Errorf("%s reads %s's patches: %v %v", g.Graph, other.Graph, patches, err)
					}
					if entries, err := tx.Snapshot(ctx, commit.ID); err != nil || len(entries) != 0 {
						t.Errorf("%s reads %s's snapshot: %v %v", g.Graph, other.Graph, entries, err)
					}
				}
				for _, image := range other.Images {
					if images, err := tx.Images(ctx, image.Kind, []storage.Pin{{ID: image.ID, Version: image.Version}}); err != nil || len(images) != 0 {
						t.Errorf("%s reads %s's image: %s %v", g.Graph, other.Graph, images, err)
					}
				}
				return nil
			})
			for _, ref := range other.Refs {
				if _, err := o.engine.Compose(ctx, ref.ID); engine.ErrorCode(err) != "not_found" {
					t.Errorf("%s composes %s's ref %s: %v", g.Graph, other.Graph, ref.ID, err)
				}
			}
			for _, commit := range other.Commits {
				if _, err := o.engine.Materialize(ctx, commit.ID); engine.ErrorCode(err) != "not_found" {
					t.Errorf("%s materializes %s's commit %s: %v", g.Graph, other.Graph, commit.ID, err)
				}
			}
		}
	}
}

// byKey sorts rows by their entity keys, byte order.
type byKey struct {
	keys []string
	rows []json.RawMessage
}

func (b byKey) Len() int           { return len(b.keys) }
func (b byKey) Less(i, j int) bool { return b.keys[i] < b.keys[j] }
func (b byKey) Swap(i, j int) {
	b.keys[i], b.keys[j] = b.keys[j], b.keys[i]
	b.rows[i], b.rows[j] = b.rows[j], b.rows[i]
}
