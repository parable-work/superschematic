package sqlite_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/versiongraph/go/engine"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/sqlite"
)

// splitmix64 fills each id's 16 bytes with two values of a splitmix64
// generator seeded with seed, high first, as the vector script's ids are
// drawn (typescript/test/sqlite-vectors.ts, seededUUIDs); the adapter sets
// the version and variant bits after, as the script does.
func splitmix64(seed uint64) func(b []byte) {
	state := seed
	next := func() uint64 {
		state += 0x9e3779b97f4a7c15
		z := state
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		return z ^ (z >> 31)
	}
	return func(b []byte) {
		binary.BigEndian.PutUint64(b[0:8], next())
		binary.BigEndian.PutUint64(b[8:16], next())
	}
}

// rawRows is each row's JSON text.
func rawRows(rows ...string) []json.RawMessage {
	out := make([]json.RawMessage, len(rows))
	for i, r := range rows {
		out[i] = json.RawMessage(r)
	}
	return out
}

// quoteIdentifier quotes an identifier as the layout does.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// dumpDatabase writes the database as SQL text, as the vector script's
// dumpDatabase does: the layout's statements, then one INSERT per row of
// each table, the tables in the layout's order and each table's rows by
// primary key (id, or history_id in a history table), one statement per
// line, each ending in a semicolon.
func dumpDatabase(t *testing.T, db *sql.DB) string {
	t.Helper()
	var lines []string
	for _, statement := range must(sqlite.Layout(nil))(t) {
		lines = append(lines, statement+";")
	}
	for _, local := range sqlite.Tables() {
		name := sqlite.DefaultTableName(local)
		var columns []string
		rows := must(db.Query("SELECT name FROM pragma_table_info(?1) ORDER BY cid", name))(t)
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				t.Fatal(err)
			}
			columns = append(columns, column)
		}
		_ = rows.Close()
		key := "id"
		if strings.HasSuffix(local, "_history") {
			key = "history_id"
		}
		rows = must(db.Query(`SELECT * FROM ` + quoteIdentifier(name) + ` ORDER BY ` + key))(t)
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			literals := make([]string, len(columns))
			for i, value := range values {
				switch v := value.(type) {
				case nil:
					literals[i] = "NULL"
				case string:
					literals[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'"
				case int64:
					literals[i] = fmt.Sprint(v)
				default:
					t.Fatalf("%s.%s holds %T; the layout holds text, integers and NULL only", name, columns[i], v)
				}
			}
			lines = append(lines, "INSERT INTO "+quoteIdentifier(name)+" ("+strings.Join(columns, ", ")+") VALUES ("+strings.Join(literals, ", ")+");")
		}
		_ = rows.Close()
	}
	return strings.Join(lines, "\n") + "\n"
}

// TestWriteParity: the vector script (typescript/test/sqlite-vectors.ts,
// writeDatabase), run through this adapter and the Go engine with the
// script's clock and its seeded ids, writes typescript.sql byte for byte,
// so a file this adapter writes is the file the TypeScript one writes.
func TestWriteParity(t *testing.T) {
	ctx := context.Background()
	descriptor := readDescriptor(t)
	// The menu graph's first writes run over the fixture's descriptor less
	// utensil.name.
	var d map[string]any
	decoder := json.NewDecoder(bytes.NewReader(descriptor))
	decoder.UseNumber()
	if err := decoder.Decode(&d); err != nil {
		t.Fatal(err)
	}
	delete(kindOf(t, d, "utensil")["columns"].(map[string]any), "name")
	beforeGain := mustJSON(t, d)

	restore := sqlite.SetIDSource(splitmix64(0x5eedd32a))
	defer restore()
	// The script's clock reads 2026-10-05T09:00:00Z first and moves
	// 1.250005 seconds at each read.
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC).UnixMicro()
	clock := func() int64 {
		read := now
		now += 1_250_005
		return read
	}
	db := openDB(t, "", "")
	client := sqlite.DB(db)
	opts := engine.Options{SchemaEpoch: 1, SnapshotEvery: 3}
	const ann = "Ann"

	recipe := must(sqlite.New(descriptor, sqlite.Options{Graph: "recipe", Clock: clock}))(t)
	if err := recipe.CreateTables(ctx, client); err != nil {
		t.Fatal(err)
	}
	g := must(engine.New(descriptor, must(recipe.Storage(ctx, client))(t), opts))(t)
	merged := func(result *engine.MergeResult, err error) *engine.MergeResult {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Conflicts) != 0 || result.Commit == nil {
			t.Fatalf("a merge of the script left conflicts %v or no commit", result.Conflicts)
		}
		return result
	}

	main := must(g.CreatePrimary(ctx, cook, bread, "main"))(t)
	first := must(g.Branch(ctx, cook, main.ID, "first"))(t)
	firstTasting := `{"entity_key":"First","taster":"Ann","salty":true,"score":4.5,"servings":9007199254740993,` +
		`"tasted_on":"2026-09-01","tasted_at":"2026-09-01T10:00:00.12Z","served_at":"18:30:00","rested":"1h30m0s",` +
		`"verdict":"again","remarks":{"crust":[1,2.50],"crumb":"open"},"tags":["sour","a \"quoted\" tag","crème brûlée 🍞"],` +
		`"helpers":["Bob","Cy"],"bites":[[1,2],[3]]}`
	secondTasting := `{"entity_key":"00000000-0000-0000-0000-000000000002","taster":"00000000-0000-0000-0000-00000000000a","salty":false,` +
		`"score":1e21,"servings":-3,"tasted_on":"2026-02-28","tasted_at":"2026-09-01T12:30:00+02:30","served_at":"2:30 pm",` +
		`"rested":"-1m30.5s","verdict":"never","remarks":null,"tags":[],"helpers":["00000000-0000-0000-0000-00000000003d"],"bites":[]}`
	first = must(g.Save(ctx, cook, first.ID, first.Version, engine.Edits{
		"cover": {Upsert: rawRows(`{"entity_key":"Cover","photo_url":"https://example.com/bread.jpg"}`)},
		"ingredient": {Upsert: rawRows(
			`{"entity_key":"Flour","step_key":"Knead","quantity":"500 g","substitutes":[{"name":"spelt","ratio":1}]}`,
			`{"entity_key":"Salt","step_key":"Knead","quantity":"10 g","substitutes":null}`,
		)},
		"note": {Upsert: rawRows(
			`{"entity_key":"Note","body":"Proof overnight\tif there's time"}`,
			`{"entity_key":"Reply","body":"Agreed","reply_to":"Note"}`,
		)},
		"step": {Upsert: rawRows(
			`{"entity_key":"Knead","position":1,"instruction":"Knead for ten minutes","timings":{"knead":"10m"},"scratch":"floury"}`,
			`{"entity_key":"Bake","position":2,"instruction":"Bake at 230 C","timings":{"bake":"35m","preheat":"30m"}}`,
		)},
		"tasting": {Upsert: rawRows(firstTasting, secondTasting)},
		"utensil": {Upsert: rawRows(`{"name":"Bowl"}`)},
	}))(t).Ref
	first = must(g.Commit(ctx, cook, first.ID, first.Version, engine.CommitOptions{Message: "first draft"}))(t).Ref
	m1 := merged(g.Merge(ctx, cook, first.ID, main.ID, main.Version, nil, engine.CommitOptions{Message: "first", Tag: true}))
	main = m1.Ref
	release := must(g.Release(ctx, cook, bread, m1.Commit.ID, 0))(t)
	must(g.Seal(ctx, cook, first.ID, first.Version))(t)

	second := must(g.Branch(ctx, ann, main.ID, "second"))(t)
	second = must(g.Save(ctx, ann, second.ID, second.Version, engine.Edits{
		"ingredient": {Delete: []string{"Salt"}},
		"step": {Upsert: rawRows(
			`{"entity_key":"Knead","position":1,"instruction":"Knead for twelve minutes","timings":{"knead":"12m","rest":"5m"},"scratch":"sticky"}`,
			`{"entity_key":"Proof","position":3,"instruction":"Proof for an hour","timings":{"proof":"1h"}}`,
		)},
	}))(t).Ref
	second = must(g.Commit(ctx, ann, second.ID, second.Version, engine.CommitOptions{Message: "second draft"}))(t).Ref
	// A partial row keeps the columns it lacks; the unset removes the change
	// set's own Proof row, as Cook, which the DELETE image names.
	second = must(g.Save(ctx, cook, second.ID, second.Version, engine.Edits{
		"step": {Upsert: rawRows(`{"entity_key":"Knead","instruction":"Knead until smooth"}`), Unset: []string{"Proof"}},
	}))(t).Ref
	second = must(g.Commit(ctx, cook, second.ID, second.Version, engine.CommitOptions{}))(t).Ref
	m2 := merged(g.Merge(ctx, ann, second.ID, main.ID, main.Version, nil, engine.CommitOptions{Message: "second", Tag: true}))
	main = m2.Ref
	must(g.Release(ctx, ann, bread, m2.Commit.ID, release.Version))(t)
	// Work the change set does not commit: a partial row on insert stores
	// null for each column it lacks.
	must(g.Save(ctx, ann, second.ID, second.Version, engine.Edits{"tasting": {Upsert: rawRows(`{"entity_key":"First","score":5}`)}}))(t)

	scrap := must(g.Branch(ctx, cook, main.ID, "scrap"))(t)
	scrap = must(g.Save(ctx, cook, scrap.ID, scrap.Version, engine.Edits{
		"cover":   {Delete: []string{"Cover"}},
		"utensil": {Upsert: rawRows(`{"entity_key":"Whisk","name":"Whisk"}`)},
	}))(t).Ref
	if err := g.Discard(ctx, cook, scrap.ID, scrap.Version); err != nil {
		t.Fatal(err)
	}

	// Before the gain: rows, images and commits without the utensil's name.
	menu := must(sqlite.New(beforeGain, sqlite.Options{Graph: "menu", Clock: clock}))(t)
	h := must(engine.New(beforeGain, must(menu.Storage(ctx, client))(t), opts))(t)
	lunch := must(h.CreatePrimary(ctx, cook, bread, "main"))(t)
	today := must(h.Branch(ctx, cook, lunch.ID, "today"))(t)
	today = must(h.Save(ctx, cook, today.ID, today.Version, engine.Edits{
		"step":    {Upsert: rawRows(`{"entity_key":"Knead","position":1,"instruction":"Slice","timings":{}}`)},
		"utensil": {Upsert: rawRows(`{"entity_key":"Knife"}`)},
	}))(t).Ref
	today = must(h.Commit(ctx, cook, today.ID, today.Version, engine.CommitOptions{Message: "today"}))(t).Ref
	served := merged(h.Merge(ctx, cook, today.ID, lunch.ID, lunch.Version, nil, engine.CommitOptions{Message: "lunch", Tag: true}))
	lunch = served.Ref
	must(h.Release(ctx, cook, bread, served.Commit.ID, 0))(t)

	// After it: the fixture's descriptor, which declares the name.
	gainedMenu := must(sqlite.New(descriptor, sqlite.Options{Graph: "menu", Clock: clock}))(t)
	i := must(engine.New(descriptor, must(gainedMenu.Storage(ctx, client))(t), opts))(t)
	dinner := must(i.Branch(ctx, ann, lunch.ID, "dinner"))(t)
	dinner = must(i.Save(ctx, ann, dinner.ID, dinner.Version, engine.Edits{
		"utensil": {Upsert: rawRows(`{"entity_key":"Knife","name":"Bread knife"}`, `{"entity_key":"Board","name":"Bread board"}`)},
	}))(t).Ref
	dinner = must(i.Commit(ctx, ann, dinner.ID, dinner.Version, engine.CommitOptions{Message: "dinner"}))(t).Ref
	merged(i.Merge(ctx, ann, dinner.ID, lunch.ID, lunch.Version, nil, engine.CommitOptions{Message: "supper", Tag: true}))

	got := dumpDatabase(t, db)
	want := string(must(os.ReadFile(filepath.Join(vectorsDir, "typescript.sql")))(t))
	if got == want {
		return
	}
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
	differ := 0
	for n := 0; n < len(gotLines) || n < len(wantLines); n++ {
		var a, b string
		if n < len(gotLines) {
			a = gotLines[n]
		}
		if n < len(wantLines) {
			b = wantLines[n]
		}
		if a != b {
			differ++
			if differ <= 3 {
				t.Errorf("line %d is\n%s\nwhere typescript.sql has\n%s", n+1, a, b)
			}
		}
	}
	t.Fatalf("%d of the dump's %d lines differ from typescript.sql's %d", differ, len(gotLines), len(wantLines))
}
