package migrate_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/internal/testdb"
	"github.com/parable-work/superschematic/runtime/migrate/go/postgres"
)

// describeTestdata holds the describe document and result the compiler's
// queryverify package reads and writes too, which holds the two JSON forms
// to one another (D86).
const describeTestdata = "../testdata/describe"

// TestDescribeDocumentForm: the runner reads the describe document the
// compiler writes, refusing a key it does not know, and writes the result
// the compiler reads.
func TestDescribeDocumentForm(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(describeTestdata, "document.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := migrate.ReadDescribeDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Statements) != 2 || doc.Statements[0].ID != "totals/0" || !strings.Contains(doc.DDL, "CREATE TABLE line") {
		t.Fatalf("document = %+v", doc)
	}
	if _, err := migrate.ReadDescribeDocument([]byte(`{"version": 1, "ddl": "", "statements": [], "extra": true}`)); err == nil {
		t.Fatal("a document with an unknown key reads")
	}
	if _, err := migrate.ReadDescribeDocument([]byte(`{"version": 2, "ddl": "", "statements": []}`)); err == nil || !strings.Contains(err.Error(), "describe document version 2") {
		t.Fatalf("another version: %v", err)
	}

	result := migrate.DescribeResult{Version: migrate.DescribeVersion, Statements: []migrate.StatementDescription{
		{ID: "totals/0",
			Params: []migrate.DescribedType{{Type: "text", Name: "text"}, {Type: "uuid[]", Name: "_uuid", Element: "uuid"}},
			Columns: []migrate.DescribedColumn{
				{Name: "name", DescribedType: migrate.DescribedType{Type: "text", Name: "text"}, Table: "line", Column: "name", NotNull: true},
				{Name: "total", DescribedType: migrate.DescribedType{Type: "numeric", Name: "numeric"}},
			}},
		{ID: "totals/1", Error: `column "amout" does not exist (at character 12)`},
	}}
	got, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(describeTestdata, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != strings.TrimSpace(string(want)) {
		t.Fatalf("the result's form:\n%s\nwant:\n%s", got, want)
	}
}

// TestDescribeOnPostgres: Describe applies the DDL to a database of its
// own, prepares each statement there, and describes its parameters and its
// result columns, a column of a table with its table, its column and
// whether it is NOT NULL; a statement Postgres refuses is described by its
// error; the database is dropped when it is done.
func TestDescribeOnPostgres(t *testing.T) {
	url := testdb.NewPostgres(t)
	doc := &migrate.DescribeDocument{
		Version: migrate.DescribeVersion,
		DDL:     "CREATE TABLE line (id UUID NOT NULL PRIMARY KEY, name TEXT NOT NULL, amount BIGINT, kept NUMERIC(12,2));",
		Statements: []migrate.DescribeStatement{
			{ID: "q/0", SQL: "SELECT id, name, amount, kept, SUM(amount) AS total FROM line WHERE name = $1 AND id = ANY($2::uuid[]) GROUP BY id"},
			{ID: "q/1", SQL: "SELECT amout FROM line"},
		},
	}
	before := testdb.Strings(t, url, "SELECT datname FROM pg_database WHERE datname LIKE 'superschematic_describe_%'")
	result, err := postgres.Describe(context.Background(), url, doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Statements) != 2 {
		t.Fatalf("result = %+v", result)
	}
	q := result.Statements[0]
	if q.Error != "" || !reflect.DeepEqual(q.Params, []migrate.DescribedType{{Type: "text", Name: "text"}, {Type: "uuid[]", Name: "_uuid", Element: "uuid"}}) {
		t.Fatalf("parameters = %+v (%s)", q.Params, q.Error)
	}
	want := []migrate.DescribedColumn{
		{Name: "id", DescribedType: migrate.DescribedType{Type: "uuid", Name: "uuid"}, Table: "line", Column: "id", NotNull: true},
		{Name: "name", DescribedType: migrate.DescribedType{Type: "text", Name: "text"}, Table: "line", Column: "name", NotNull: true},
		{Name: "amount", DescribedType: migrate.DescribedType{Type: "bigint", Name: "int8"}, Table: "line", Column: "amount"},
		{Name: "kept", DescribedType: migrate.DescribedType{Type: "numeric(12,2)", Name: "numeric"}, Table: "line", Column: "kept"},
		{Name: "total", DescribedType: migrate.DescribedType{Type: "numeric", Name: "numeric"}},
	}
	if !reflect.DeepEqual(q.Columns, want) {
		t.Fatalf("columns = %+v, want %+v", q.Columns, want)
	}
	if bad := result.Statements[1]; !strings.Contains(bad.Error, `column "amout" does not exist`) || bad.Columns != nil {
		t.Fatalf("a refused statement = %+v", bad)
	}
	if after := testdb.Strings(t, url, "SELECT datname FROM pg_database WHERE datname LIKE 'superschematic_describe_%'"); len(after) > len(before) {
		t.Fatalf("Describe left its database: %v", after)
	}
}
