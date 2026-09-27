// Package orm checks, against a real Postgres, the JSONB columns sqlgen
// generates for arrays of arrays (T[][]) and the JSON codec ormgen generates
// for them. TestArraysOfArraysOnPostgres in the ormgen package copies this
// module, adds the utils.go ormgen generated for fixture-nested-arrays-db
// (package orm, standard library only) and runs it with:
//
//	PGARRAYS_DATABASE_URL  a Postgres URL whose role may create schemas
//	PGARRAYS_CREATE_SQL    the create.sql sqlgen generated for the fixture
//
// Values are written and read the way the generated repositories do: a
// required column always goes through marshalArrayOfArraysFieldValue, an
// optional one only when its list is not nil, and every column is scanned as
// bytes and decoded with unmarshalJSONListFieldValue, which refuses a null
// element another writer stored.
package orm

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// sqlNull stands for SQL NULL among the expected column texts.
const sqlNull = "<SQL NULL>"

type boardPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// board is the fixture's Board row with the Go types its generated types
// module gives each column (CellState is a string type).
type board struct {
	Labels [][]string
	States [][]string
	Walls  [][]boardPoint
	Scores [][]float64
}

func env(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s is not set; run through TestArraysOfArraysOnPostgres", name)
	}
	return v
}

func encode[T any](t *testing.T, value [][]T) any {
	t.Helper()
	encoded, err := marshalArrayOfArraysFieldValue(value)
	if err != nil {
		t.Fatalf("marshal %#v: %v", value, err)
	}
	return encoded
}

func decode[T any](t *testing.T, raw []byte, field string, target *[][]T) {
	t.Helper()
	if len(raw) == 0 {
		return
	}
	if err := unmarshalJSONListFieldValue(raw, target, field, 2); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

func TestArraysOfArraysOnPostgres(t *testing.T) {
	dsn := env(t, "PGARRAYS_DATABASE_URL")
	createSQL, err := os.ReadFile(env(t, "PGARRAYS_CREATE_SQL"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Cleanups run last-registered first: the schema is dropped before the
	// connection closes.
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	schema := fmt.Sprintf("arrays_of_arrays_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})
	if _, err := conn.Exec(ctx, "SET search_path TO "+schema+", public"); err != nil {
		t.Fatalf("set search_path: %v", err)
	}
	if _, err := conn.PgConn().Exec(ctx, string(createSQL)).ReadAll(); err != nil {
		t.Fatalf("apply create.sql: %v", err)
	}

	rows, err := conn.Query(ctx, `SELECT column_name, data_type FROM information_schema.columns
WHERE table_schema = $1 AND table_name = 'board' ORDER BY ordinal_position`, schema)
	if err != nil {
		t.Fatal(err)
	}
	var columns []string
	for rows.Next() {
		var name, dataType string
		if err := rows.Scan(&name, &dataType); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name+" "+dataType)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(columns, ", "), "id uuid, labels jsonb, states jsonb, walls jsonb, scores jsonb"; got != want {
		t.Fatalf("board columns = %s, want %s", got, want)
	}

	// A native array must be rectangular, which is why T[][] is JSONB.
	if _, err := conn.Exec(ctx, "SELECT ARRAY[ARRAY['a', 'b'], ARRAY['c']]"); err == nil {
		t.Fatal("Postgres accepted a ragged native array")
	}

	for _, tc := range []struct {
		name string
		in   board
		want board
		// text is each column read back as ::text, in column order.
		text [4]string
	}{
		{
			name: "ragged lists",
			in: board{
				Labels: [][]string{{"a", "b", "c"}, {"d"}, {}},
				States: [][]string{{"filled"}, {}, {"empty", "filled", "empty"}},
				Walls:  [][]boardPoint{{{X: 1, Y: 2}, {X: 3, Y: 4}, {X: 5, Y: 6}}, {{X: 0, Y: 0}}},
				Scores: [][]float64{{1.5}, {2, 3, 4}, {}},
			},
			want: board{
				Labels: [][]string{{"a", "b", "c"}, {"d"}, {}},
				States: [][]string{{"filled"}, {}, {"empty", "filled", "empty"}},
				Walls:  [][]boardPoint{{{X: 1, Y: 2}, {X: 3, Y: 4}, {X: 5, Y: 6}}, {{X: 0, Y: 0}}},
				Scores: [][]float64{{1.5}, {2, 3, 4}, {}},
			},
			text: [4]string{
				`[["a", "b", "c"], ["d"], []]`,
				`[["filled"], [], ["empty", "filled", "empty"]]`,
				`[[{"x": 1, "y": 2}, {"x": 3, "y": 4}, {"x": 5, "y": 6}], [{"x": 0, "y": 0}]]`,
				`[[1.5], [2, 3, 4], []]`,
			},
		},
		{
			name: "empty outer and empty inner lists",
			in:   board{Labels: [][]string{}, States: [][]string{{}}, Walls: [][]boardPoint{{}, {}}, Scores: [][]float64{}},
			want: board{Labels: [][]string{}, States: [][]string{{}}, Walls: [][]boardPoint{{}, {}}, Scores: [][]float64{}},
			text: [4]string{`[]`, `[[]]`, `[[], []]`, `[]`},
		},
		{
			name: "nil inner lists are stored as empty lists and a nil optional list as SQL NULL",
			in:   board{Labels: [][]string{nil, {"x"}}, States: [][]string{nil}, Walls: [][]boardPoint{{{X: 1, Y: 1}}, nil}},
			want: board{Labels: [][]string{{}, {"x"}}, States: [][]string{{}}, Walls: [][]boardPoint{{{X: 1, Y: 1}}, {}}},
			text: [4]string{`[[], ["x"]]`, `[[]]`, `[[{"x": 1, "y": 1}], []]`, sqlNull},
		},
		{
			name: "a nil required list is JSON null, as in every JSON column",
			in:   board{States: [][]string{{"empty"}}, Walls: [][]boardPoint{}},
			want: board{States: [][]string{{"empty"}}, Walls: [][]boardPoint{}},
			text: [4]string{`null`, `[["empty"]]`, `[]`, sqlNull},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			names := []string{"labels", "states", "walls"}
			args := []any{encode(t, tc.in.Labels), encode(t, tc.in.States), encode(t, tc.in.Walls)}
			if tc.in.Scores != nil {
				names = append(names, "scores")
				args = append(args, encode(t, tc.in.Scores))
			}
			placeholders := make([]string, len(args))
			for i := range args {
				placeholders[i] = fmt.Sprintf("$%d", i+1)
			}
			var id string
			insert := fmt.Sprintf("INSERT INTO board (%s) VALUES (%s) RETURNING id::text",
				strings.Join(names, ", "), strings.Join(placeholders, ", "))
			if err := conn.QueryRow(ctx, insert, args...).Scan(&id); err != nil {
				t.Fatalf("insert: %v", err)
			}

			var raw [4][]byte
			var text [4]*string
			if err := conn.QueryRow(ctx, `SELECT labels, states, walls, scores,
labels::text, states::text, walls::text, scores::text FROM board WHERE id = $1`, id).Scan(
				&raw[0], &raw[1], &raw[2], &raw[3], &text[0], &text[1], &text[2], &text[3],
			); err != nil {
				t.Fatalf("select: %v", err)
			}
			for i, want := range tc.text {
				got := sqlNull
				if text[i] != nil {
					got = *text[i]
				}
				if got != want {
					t.Errorf("column %d reads back as %s, want %s", i, got, want)
				}
			}

			var got board
			decode(t, raw[0], "labels", &got.Labels)
			decode(t, raw[1], "states", &got.States)
			decode(t, raw[2], "walls", &got.Walls)
			decode(t, raw[3], "scores", &got.Scores)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("decoded %#v, want %#v", got, tc.want)
			}
		})
	}

	// The codec writes no null element, but another writer can store one. It
	// fails the read at its path, as a generated type's UnmarshalJSON fails
	// it; encoding/json alone would read ["a", ""]. A stored null inner list
	// reads as a nil list.
	t.Run("a stored null element fails the read", func(t *testing.T) {
		var id string
		if err := conn.QueryRow(ctx, `INSERT INTO board (labels, states, walls, scores)
VALUES ($1::jsonb, $2::jsonb, $3::jsonb, $4::jsonb) RETURNING id::text`,
			`[["a", null]]`, `[["filled"], null]`, `[[{"x": 1, "y": 2}, null]]`, `[[null]]`).Scan(&id); err != nil {
			t.Fatalf("insert: %v", err)
		}
		var raw [4][]byte
		if err := conn.QueryRow(ctx, `SELECT labels, states, walls, scores FROM board WHERE id = $1`, id).Scan(
			&raw[0], &raw[1], &raw[2], &raw[3],
		); err != nil {
			t.Fatalf("select: %v", err)
		}
		var labels [][]string
		var states [][]string
		var walls [][]boardPoint
		var scores [][]float64
		for _, tc := range []struct {
			field  string
			raw    []byte
			target any
			want   string
		}{
			{"labels", raw[0], &labels, "labels[0][1]: null element"},
			{"walls", raw[2], &walls, "walls[0][1]: null element"},
			{"scores", raw[3], &scores, "scores[0][0]: null element"},
		} {
			if err := unmarshalJSONListFieldValue(tc.raw, tc.target, tc.field, 2); err == nil || err.Error() != tc.want {
				t.Errorf("decode %s = %s: err = %v, want %q", tc.field, tc.raw, err, tc.want)
			}
		}
		decode(t, raw[1], "states", &states)
		if want := [][]string{{"filled"}, nil}; !reflect.DeepEqual(states, want) {
			t.Errorf("states with a null inner list = %#v, want %#v", states, want)
		}
	})
}
