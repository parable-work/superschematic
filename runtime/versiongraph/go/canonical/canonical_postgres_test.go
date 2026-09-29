package canonical

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestVectorsRenderAsPostgresRendersThem checks every vector's postgres
// member against a real Postgres: each value's SQL, in a row, under the
// vector's session time zone (UTC when it names none), renders as the
// vector says, and each row's to_jsonb does. The rendering then goes
// through PostgresRow, so the rules run on what Postgres returned, not only
// on text written into a file. It needs
// SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL.
func TestVectorsRenderAsPostgresRendersThem(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL to check the canonical vectors against Postgres")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })

	render := func(t *testing.T, timeZone, query string) string {
		t.Helper()
		if timeZone == "" {
			timeZone = "UTC"
		}
		if _, err := conn.Exec(ctx, "SELECT set_config('TimeZone', $1, false)", timeZone); err != nil {
			t.Fatalf("set the time zone to %s: %v", timeZone, err)
		}
		var text string
		if err := conn.QueryRow(ctx, query).Scan(&text); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return text
	}

	values, rows := readVectors(t)
	for _, c := range values {
		t.Run(c.Class+"/"+c.Name, func(t *testing.T) {
			got := render(t, c.TimeZone, "SELECT to_jsonb(t)::text FROM (SELECT "+c.SQL+" AS v) AS t")
			var row map[string]json.RawMessage
			if err := json.Unmarshal([]byte(got), &row); err != nil {
				t.Fatal(err)
			}
			if string(row["v"]) != c.Postgres {
				t.Fatalf("Postgres renders %s as %s, the vector says %s", c.SQL, row["v"], c.Postgres)
			}
			canonical, err := PostgresRow(map[string]string{"v": c.Class}, json.RawMessage(got))
			if c.Error != "" {
				if err == nil {
					t.Fatalf("PostgresRow accepted %s, which the vector refuses (%s)", got, c.Error)
				}
				return
			}
			if err != nil {
				t.Fatalf("PostgresRow(%s): %v", got, err)
			}
			if want := `{"v":` + c.Canonical + `}`; string(canonical) != want {
				t.Fatalf("PostgresRow(%s)\n got: %s\nwant: %s", got, canonical, want)
			}
		})
	}
	for _, c := range rows {
		t.Run("row/"+c.Name, func(t *testing.T) {
			got := render(t, c.TimeZone, "SELECT to_jsonb(t)::text FROM (SELECT "+c.SQL+") AS t")
			if got != c.Postgres {
				t.Fatalf("Postgres renders the row as\n%s\nthe vector says\n%s", got, c.Postgres)
			}
		})
	}
}
