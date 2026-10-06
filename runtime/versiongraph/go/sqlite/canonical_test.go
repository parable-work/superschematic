package sqlite_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/runtime/versiongraph/go/canonical"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/sqlite"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/storage"
)

// valueCase and rowCase are the canonical vectors' cases
// (runtime/versiongraph/testdata/canonical): a value of a class, or a row
// of columns, as Postgres renders it and in its canonical form, which is
// absent for one the rules refuse.
type valueCase struct {
	Name      string  `json:"name"`
	Class     string  `json:"class"`
	Postgres  string  `json:"postgres"`
	Canonical *string `json:"canonical"`
}

type rowCase struct {
	Name      string            `json:"name"`
	Columns   map[string]string `json:"columns"`
	Postgres  string            `json:"postgres"`
	Canonical *string           `json:"canonical"`
}

// TestCanonicalVectors: every canonical vector reads back as the canonical
// value written, live and from history, its Postgres form is stored
// canonical, and so is each input form the rules read for a list class.
func TestCanonicalVectors(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "canonical", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	var values []valueCase
	var rows []rowCase
	for _, file := range files {
		text, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Cases []valueCase `json:"cases"`
			Rows  []rowCase   `json:"rows"`
		}
		if err := json.Unmarshal(text, &doc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		values = append(values, doc.Cases...)
		rows = append(rows, doc.Rows...)
	}
	if len(values) == 0 || len(rows) == 0 {
		t.Fatal("no canonical vectors")
	}
	// Input forms the canonical rules read, for each list class, beside the
	// vectors' Postgres forms: each is stored canonical, as the rules give
	// it, or refused.
	inputs := [][2]string{
		{"string[]", `["a", 1]`},
		{"string[][]", `[["a"], [2]]`},
		{"integer[]", "[-0, 12]"},
		{"integer[][]", "[[-0, 7], []]"},
		{"integer[][]", "[[1.5]]"},
		{"number[]", "[1e2, -0]"},
		{"number[][]", "[[1.50e1]]"},
		{"boolean[]", `["true"]`},
		{"boolean[][]", `[[true], ["true"]]`},
		{"uuid[][]", `[["5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90"]]`},
		{"dateTime[]", `["2026-09-01T12:30:00+02:30"]`},
		{"dateTime[][]", `[["2026-09-01T12:30:00.10+02:30"]]`},
		{"date[]", `["2026-02-30"]`},
		{"date[]", `["2026-09-01", "nope"]`},
		{"date[][]", `[["2026-02-29"]]`},
		{"time[]", `["2:30 pm"]`},
		{"duration[]", `["1 day 02:00:00"]`},
		{"enum[]", "[1]"},
		{"enum[][]", `[["again"], [1]]`},
		{"json[]", `[{"b": 1.50, "a": [2.0]}]`},
		{"json[][]", `[[{"b": 1, "a": 0}]]`},
	}
	// A kind per case, whose role columns are named apart from its own.
	kindOf := func(name string, columns map[string]string) map[string]any {
		all := map[string]string{"vg_key": "uuid", "vg_id": "uuid", "vg_ref": "uuid", "vg_root": "uuid", "vg_tombstone": "boolean", "vg_version": "integer"}
		for column, class := range columns {
			all[column] = class
		}
		return map[string]any{
			"kind": name, "key": "vg_key", "id": "vg_id", "ref": "vg_ref", "root": "vg_root", "tombstone": "vg_tombstone", "version": "vg_version",
			"history": map[string]any{"exclude": []string{}}, "columns": all,
		}
	}
	var kinds []map[string]any
	for i, c := range values {
		kinds = append(kinds, kindOf(fmt.Sprintf("value%d", i), map[string]string{"v": c.Class}), kindOf(fmt.Sprintf("postgres%d", i), map[string]string{"v": c.Class}))
	}
	for i, c := range rows {
		kinds = append(kinds, kindOf(fmt.Sprintf("row%d", i), c.Columns), kindOf(fmt.Sprintf("postgresRow%d", i), c.Columns))
	}
	for i, input := range inputs {
		kinds = append(kinds, kindOf(fmt.Sprintf("input%d", i), map[string]string{"v": input[0]}))
	}
	ctx := context.Background()
	db := openDB(t, "", "")
	client := sqlite.DB(db)
	adapter := must(sqlite.New(mustJSON(t, map[string]any{"version": 3, "kinds": kinds}), sqlite.Options{Graph: "vectors"}))(t)
	if err := adapter.CreateTables(ctx, client); err != nil {
		t.Fatal(err)
	}
	s := must(adapter.Storage(ctx, client))(t)
	ref := must(in(s, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) {
		return tx.CreateRef(ctx, storage.NewRef{Root: bread, Name: "main", Actor: cook})
	}))(t)
	write := func(kind, row string) (json.RawMessage, error) {
		return in(s, func(ctx context.Context, tx storage.Tx) (json.RawMessage, error) {
			return tx.UpsertRow(ctx, kind, storage.RowWrite{Ref: ref.ID, Root: bread, Row: json.RawMessage(row), Actor: cook})
		})
	}
	stored := func(kind string) (id, data string, ok bool) {
		err := db.QueryRow(`SELECT id, data FROM "graph_member" WHERE kind = ?1`, kind).Scan(&id, &data)
		return id, data, err == nil
	}
	refusedCanonically := func(err error) bool {
		var canonicalErr *canonical.Error
		return errors.As(err, &canonicalErr)
	}
	for i, c := range values {
		kind := fmt.Sprintf("value%d", i)
		what := c.Class + "/" + c.Name
		if c.Canonical == nil {
			// A value its class refuses is not stored.
			if _, err := write(kind, `{"v":`+c.Postgres+`}`); !refusedCanonically(err) {
				t.Fatalf("%s: writing %s = %v, want the canonical rules' refusal", what, c.Postgres, err)
			}
			if _, _, ok := stored(kind); ok {
				t.Fatalf("%s: a refused value was stored", what)
			}
			continue
		}
		want := `{"v":` + *c.Canonical + `}`
		written := must(write(kind, want))(t)
		id, data, _ := stored(kind)
		if data != want {
			t.Fatalf("%s: stored as %s, want %s", what, data, want)
		}
		must(write(fmt.Sprintf("postgres%d", i), `{"v":`+c.Postgres+`}`))(t)
		if _, data, _ := stored(fmt.Sprintf("postgres%d", i)); data != want {
			t.Fatalf("%s: its Postgres form %s is stored as %s, want %s", what, c.Postgres, data, want)
		}
		read := must(in(s, func(ctx context.Context, tx storage.Tx) ([]json.RawMessage, error) { return tx.Rows(ctx, kind, ref.ID) }))(t)
		images := must(in(s, func(ctx context.Context, tx storage.Tx) ([]json.RawMessage, error) {
			return tx.Images(ctx, kind, []storage.Pin{{ID: id, Version: 1}})
		}))(t)
		if len(read) != 1 || len(images) != 1 {
			t.Fatalf("%s: %d rows and %d images", what, len(read), len(images))
		}
		for _, text := range []json.RawMessage{written, read[0], images[0]} {
			if !strings.HasPrefix(string(text), `{"v":`+*c.Canonical+`,"vg_id":`) {
				t.Fatalf("%s: reads back as %s, want %s first", what, text, *c.Canonical)
			}
		}
	}
	for i, c := range rows {
		kind := fmt.Sprintf("row%d", i)
		if c.Canonical == nil {
			if _, err := write(kind, c.Postgres); err == nil {
				t.Fatalf("%s: a row the rules refuse was written", c.Name)
			}
			if _, _, ok := stored(kind); ok {
				t.Fatalf("%s: a refused row was stored", c.Name)
			}
			continue
		}
		must(write(kind, *c.Canonical))(t)
		must(write(fmt.Sprintf("postgresRow%d", i), c.Postgres))(t)
		// Every declared column, the ones the row lacks as null.
		var canonicalRow map[string]json.RawMessage
		if err := json.Unmarshal([]byte(*c.Canonical), &canonicalRow); err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(c.Columns))
		for column := range c.Columns {
			names = append(names, column)
		}
		sort.Strings(names)
		var members []string
		for _, column := range names {
			value := "null"
			if v, ok := canonicalRow[column]; ok {
				var compact bytes.Buffer
				if err := json.Compact(&compact, v); err != nil {
					t.Fatal(err)
				}
				value = compact.String()
			}
			members = append(members, string(mustJSON(t, column))+":"+value)
		}
		want := "{" + strings.Join(members, ",") + "}"
		if _, data, _ := stored(kind); data != want {
			t.Fatalf("%s: stored as %s, want %s", c.Name, data, want)
		}
		if _, data, _ := stored(fmt.Sprintf("postgresRow%d", i)); data != want {
			t.Fatalf("%s: its Postgres form is stored as %s, want %s", c.Name, data, want)
		}
	}
	for i, input := range inputs {
		kind := fmt.Sprintf("input%d", i)
		class, text := input[0], input[1]
		want, err := canonical.Postgres(class, json.RawMessage(text))
		if err != nil {
			if _, err := write(kind, `{"v":`+text+`}`); !refusedCanonically(err) {
				t.Fatalf("%s %s = %v, want it refused", class, text, err)
			}
			if _, _, ok := stored(kind); ok {
				t.Fatalf("%s %s: a refused value was stored", class, text)
			}
			continue
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(text)); err != nil {
			t.Fatal(err)
		}
		if compact.String() == string(want) {
			t.Fatalf("%s %s is already canonical", class, text)
		}
		must(write(kind, `{"v":`+text+`}`))(t)
		if _, data, _ := stored(kind); data != `{"v":`+string(want)+`}` {
			t.Fatalf("%s %s is stored as %s, want %s", class, text, data, want)
		}
	}
}
