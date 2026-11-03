package tsreader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// queryConstantFiles are a DB service whose report's SQL is built from
// string constants (D95): a nested template of constants in a file the
// schema file imports, a quoted constant there too, and one of the schema
// file's own.
func queryConstantFiles(sql string, extra map[string]string) map[string]string {
	files := map[string]string{
		"src/filters.ts": "const RANGE = `li.\"date\" BETWEEN :start AND :end`;\n" +
			"export const FILTER = `${RANGE}\n  AND li.amount > 0`;\n" +
			"export const LIVE = 'li.removed_at IS NULL';\n",
		"src/reports.schema.ts": `import { query } from "@superschematic/db";
import { FILTER, LIVE } from "./filters";

const FROM = "FROM line_item li";

export abstract class TotalsParams {
  start: string;
  end: string;
}

export abstract class TotalsRow {
  total: number;
}

@query<TotalsParams, TotalsRow>({
  sql: ` + sql + `,
})
export abstract class Totals {}
`,
	}
	for name, text := range extra {
		files[name] = text
	}
	return files
}

// TestQuerySQLInterpolatesStringConstants: a template literal's ${...} is
// a string constant, read through its const's initializer, in the file or
// one it imports, a template of constants among them; the SQL is the text
// with each in place, which the lock hashes (D95). A const naming such a
// template is SQL too.
func TestQuerySQLInterpolatesStringConstants(t *testing.T) {
	want := "SELECT SUM(li.amount) AS total FROM line_item li WHERE li.\"date\" BETWEEN :start AND :end\n  AND li.amount > 0 AND li.removed_at IS NULL"
	for name, sql := range map[string]string{
		"a template":     "`SELECT SUM(li.amount) AS total ${FROM} WHERE ${FILTER} AND ${LIVE}`",
		"a const of one": "TOTALS",
	} {
		t.Run(name, func(t *testing.T) {
			dir := decoratorTestService(t, "DB", queryConstantFiles(sql, map[string]string{
				"src/totals.ts": "import { FILTER, LIVE } from \"./filters\";\nconst FROM = \"FROM line_item li\";\n" +
					"export const TOTALS = `SELECT SUM(li.amount) AS total ${FROM} WHERE ${FILTER} AND ${LIVE}`;\n",
			}))
			if name == "a const of one" {
				writeServiceFile(t, dir, "src/reports.schema.ts", func(text string) string {
					return strings.Replace(text, "import { FILTER, LIVE } from \"./filters\";", "import { TOTALS } from \"./totals\";", 1)
				})
			}
			schema, _, err := LoadService(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := schema.Types["Totals"].Query.SQL; got != want {
				t.Fatalf("sql = %q\nwant %q", got, want)
			}
		})
	}
}

// TestQuerySQLRefusesWhatIsNotAConstant: a ${...} that is not a string
// constant fails the load where it is written, saying what one may be: a
// let, a call, a number; and strings joined with + say to write a
// template literal.
func TestQuerySQLRefusesWhatIsNotAConstant(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sql   string
		extra string
		want  string
	}{
		{
			name:  "a let",
			sql:   "`SELECT 1 AS total ${WHERE}`",
			extra: "let WHERE = \"WHERE true\";\n",
			want:  `reports.schema.ts:17:29: ${WHERE} does not evaluate statically ("WHERE" is declared with let; the static walk reads a const's initializer, which no assignment changes: declare it const); a template literal's ${...} names a string constant: a const declared with a string, or with a template literal of such constants, in this file or one it imports`,
		},
		{
			name:  "a call",
			sql:   "`SELECT 1 AS total ${where()}`",
			extra: "function where(): string { return \"WHERE true\"; }\n",
			want:  `reports.schema.ts:17:29: ${where()} does not evaluate statically (function calls are not statically evaluable (only service({...}) sentinels are)); a template literal's ${...} names a string constant`,
		},
		{
			name:  "a number",
			sql:   "`SELECT 1 AS total LIMIT ${LIMIT}`",
			extra: "const LIMIT = 10;\n",
			want:  `reports.schema.ts:17:35: ${LIMIT} is a number, not a string; a template literal's ${...} names a string constant`,
		},
		{
			name: "strings joined with +",
			sql:  "\"SELECT 1 AS total \" + FROM",
			want: "reports.schema.ts:16:8: argument must be a literal; join strings with a template literal whose ${...} are string constants, as `${FILTER} AND x`, not with an operator",
		},
		{
			name:  "a constant whose own ${...} is not one, located where it is",
			sql:   "`SELECT 1 AS total ${BROKEN}`",
			extra: "import { BROKEN } from \"./broken\";\n",
			want:  `src/broken.ts:2:32: ${COUNT} is a number, not a string`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := decoratorTestService(t, "DB", queryConstantFiles(tc.sql, map[string]string{
				"src/broken.ts": "const COUNT = 3;\nexport const BROKEN = `LIMIT ${COUNT}`;\n",
			}))
			if tc.extra != "" {
				writeServiceFile(t, dir, "src/reports.schema.ts", func(text string) string {
					return strings.Replace(text, "const FROM", tc.extra+"const FROM", 1)
				})
			}
			_, _, err := LoadService(dir)
			if err == nil {
				t.Fatal("the service loaded")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v\nwant it to contain %q", err, tc.want)
			}
		})
	}
}

// writeServiceFile rewrites the file rel of the service at dir through
// edit.
func writeServiceFile(t *testing.T, dir, rel string, edit func(string) string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(edit(string(data))), 0o644); err != nil {
		t.Fatal(err)
	}
}
