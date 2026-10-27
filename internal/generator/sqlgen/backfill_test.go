package sqlgen

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// quoteSQLite quotes an identifier as the SQLite dialect does.
func quoteSQLite(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// backfillSchema is enqueueSchema's task table with a trigger on its
// inserts and updates, whose backfill (D88) enqueues every task that is
// done, open and not urgent, on a queue of the onDuplicate given, keyed by
// title when it dedupes.
func backfillSchema(onDuplicate string) *ir.Schema {
	s := enqueueSchema("Task")
	def := s.Types["Task"].EnqueueOnChange[0]
	def.On, def.Backfill = []string{ir.EnqueueOnInsert, ir.EnqueueOnUpdate}, true
	if onDuplicate != "" {
		s.Types["TaskGone"].Queue = &ir.QueueDef{Key: "title", OnDuplicate: onDuplicate}
	}
	return s
}

// TestBackfillSQL: a backfill inserts the trigger's message for each row
// that meets its conditions, written as the trigger writes them, and ends
// with the queue's conflict. Under replace, Postgres takes one row of each
// key, the first by the table's key, which one statement's ON CONFLICT DO
// UPDATE needs; SQLite's upsert takes each row in turn, so the last wins;
// skip and append take every row (D88).
func TestBackfillSQL(t *testing.T) {
	where := "WHERE src.done = TRUE AND src.urgent IS NULL AND src.rank = 2.5 AND src.title = 'it''s' AND src.status = 'open'"
	for _, tc := range []struct {
		onDuplicate string
		postgres    []string
		sqlite      []string
	}{
		{"", []string{
			"INSERT INTO task_gone_queue (task_id, title)\nSELECT src.id, src.title\nFROM task AS src\n" + where,
		}, []string{
			`INSERT INTO "task_gone_queue" ("task_id", "title")` + "\nSELECT src.\"id\", src.\"title\"\nFROM \"task\" AS src\nWHERE src.\"done\" = 1",
		}},
		{ir.QueueOnDuplicateReplace, []string{
			"SELECT DISTINCT ON (src.title) src.id, src.title\nFROM task AS src\n" + where + "\nORDER BY src.title, src.id\nON CONFLICT (title) WHERE state = 'ready' DO UPDATE SET",
		}, []string{
			"SELECT src.\"id\", src.\"title\"\nFROM \"task\" AS src\nWHERE",
			"ON CONFLICT",
		}},
		{ir.QueueOnDuplicateSkip, []string{
			"SELECT src.id, src.title\nFROM task AS src\n" + where + "\nON CONFLICT (title) WHERE state = 'ready' DO NOTHING",
		}, []string{"DO NOTHING"}},
	} {
		out, err := Generate(backfillSchema(tc.onDuplicate), Options{SchemaName: "tasks-db"})
		if err != nil {
			t.Fatal(err)
		}
		e := out.EnqueueTriggers[0]
		if !e.Backfill {
			t.Fatalf("%q: the trigger does not backfill", tc.onDuplicate)
		}
		pg, lite := e.BackfillSQL(), e.SQLiteBackfillSQL(quoteSQLite)
		for _, want := range tc.postgres {
			if !strings.Contains(pg, want) {
				t.Errorf("%q: the Postgres backfill misses %q:\n%s", tc.onDuplicate, want, pg)
			}
		}
		for _, want := range tc.sqlite {
			if !strings.Contains(lite, want) {
				t.Errorf("%q: the SQLite backfill misses %q:\n%s", tc.onDuplicate, want, lite)
			}
		}
		if strings.Contains(lite, "DISTINCT ON") {
			t.Errorf("%q: SQLite has no DISTINCT ON:\n%s", tc.onDuplicate, lite)
		}
	}
}

// TestRoleSeedSQL: a seeded role's insert adds it, and gives a role of its
// name with no permissions the seed's; its update sets them; a seed with
// no permissions adds the role and changes none (D88).
func TestRoleSeedSQL(t *testing.T) {
	seed := RoleSeed{
		Table: "role", QuotedTable: "role", NameColumn: `"name"`, PermissionsColumn: "permissions", PermissionsType: "TEXT[]",
		Name: "member", Permissions: []string{"orders.read", "it's"},
	}
	for got, want := range map[string]string{
		seed.InsertSQL(): "INSERT INTO role (\"name\", permissions) VALUES ('member', ARRAY['orders.read', 'it''s']::TEXT[])\n" +
			"ON CONFLICT (\"name\") DO UPDATE SET permissions = EXCLUDED.permissions WHERE cardinality(role.permissions) = 0",
		seed.UpdateSQL(): `UPDATE role SET permissions = ARRAY['orders.read', 'it''s']::TEXT[] WHERE "name" = 'member'`,
		seed.SQLiteInsertSQL(quoteSQLite): "INSERT INTO \"role\" (\"name\", \"permissions\") VALUES ('member', '[\"orders.read\",\"it''s\"]')\n" +
			"ON CONFLICT (\"name\") DO UPDATE SET \"permissions\" = excluded.\"permissions\" WHERE json_array_length(\"role\".\"permissions\") = 0",
		seed.SQLiteUpdateSQL(quoteSQLite): `UPDATE "role" SET "permissions" = '["orders.read","it''s"]' WHERE "name" = 'member'`,
	} {
		if got != want {
			t.Errorf("got\n%s\nwant\n%s", got, want)
		}
	}
	empty := seed
	empty.Permissions = nil
	if got, want := empty.InsertSQL(), "INSERT INTO role (\"name\", permissions) VALUES ('member', '{}'::TEXT[])\nON CONFLICT (\"name\") DO NOTHING"; got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
