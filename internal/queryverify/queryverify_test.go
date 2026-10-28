package queryverify

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "write fixture-queries-db's queries.lock")

const fixtureDir = "../loader/tsreader/testdata/services/fixture-queries-db"

// describeTestdata holds the describe document and result the migration
// runner's tests read and write too (runtime/migrate/go's describe_test.go).
const describeTestdata = "../../runtime/migrate/testdata/describe"

func fixture(t *testing.T) (*ir.Schema, map[string]sqlutil.QueryModel) {
	t.Helper()
	schema, err := loader.LoadService(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	queries, err := sqlutil.QueriesOf(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]sqlutil.QueryModel{}
	for _, q := range queries {
		byName[q.Name] = q
	}
	return schema, byName
}

// TestDescribeDocumentMatchesTheRunner: the describe document the compiler
// writes and the result it reads are the JSON forms the migration runner
// reads and writes (D86).
func TestDescribeDocumentMatchesTheRunner(t *testing.T) {
	doc := Document{
		Version: DescribeVersion,
		DDL:     "CREATE TABLE line (id UUID NOT NULL PRIMARY KEY, name TEXT NOT NULL, amount BIGINT);",
		Statements: []Statement{
			{ID: "totals/0", SQL: "SELECT name, SUM(amount) AS total FROM line WHERE name = $1 AND id = ANY($2) GROUP BY name"},
			{ID: "totals/1", SQL: "SELECT amout FROM line"},
		},
	}
	got, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(describeTestdata, "document.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != strings.TrimSpace(string(want)) {
		t.Fatalf("the document's form:\n%s\nwant:\n%s", got, want)
	}
	data, err := os.ReadFile(filepath.Join(describeTestdata, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result Result
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	columns := result.Statements[0].Columns
	if len(columns) != 2 || columns[0].Name != "name" || columns[0].Type.Name != "text" || columns[0].Table != "line" || !columns[0].NotNull ||
		result.Statements[0].Params[1].Element != "uuid" || !strings.Contains(result.Statements[1].Error, "amout") {
		t.Fatalf("result = %+v", result)
	}
}

// TestCompatible: a declared SQL type and a described one cross alike when
// they are of one family, both lists or neither.
func TestCompatible(t *testing.T) {
	for _, tc := range []struct {
		declared  string
		described Type
		want      bool
	}{
		{"UUID", Type{Name: "uuid"}, true},
		{"UUID[]", Type{Name: "_uuid", Element: "uuid"}, true},
		{"UUID", Type{Name: "_uuid", Element: "uuid"}, false},
		{"UUID[]", Type{Name: "uuid"}, false},
		{"TIMESTAMPTZ", Type{Name: "timestamptz"}, true},
		{"TIMESTAMPTZ", Type{Name: "timestamp"}, false},
		{"BIGINT", Type{Name: "int4"}, true},
		{"BIGINT", Type{Name: "numeric"}, false},
		{"NUMERIC(12,2)", Type{Name: "numeric"}, true},
		{"DOUBLE PRECISION", Type{Name: "float8"}, true},
		{"DOUBLE PRECISION", Type{Name: "int8"}, false},
		{"TEXT", Type{Name: "varchar"}, true},
		{"VARCHAR(80)", Type{Name: "text"}, true},
		{"CITEXT", Type{Name: "text"}, true},
		{"JSONB", Type{Name: "json"}, true},
		{"vector(3)", Type{Name: "vector"}, true},
		{"DATE", Type{Name: "date"}, true},
		{"BOOLEAN", Type{Name: "bool"}, true},
	} {
		if got := compatible(tc.declared, tc.described); got != tc.want {
			t.Errorf("compatible(%s, %+v) = %v, want %v", tc.declared, tc.described, got, tc.want)
		}
	}
}

// TestJudge: what Postgres describes is held to the declared types: a
// refused statement, a parameter of another type, a missing and an extra
// column, a column of another type, and a column that may be null under a
// field that is not Nullable, each name the query and the fix; what holds
// is the query's lock entry.
func TestJudge(t *testing.T) {
	_, queries := fixture(t)
	budgets := queries["budgets"]
	column := func(name, typ, typeName, table string, notNull bool) Column {
		return Column{Name: name, Type: Type{Type: typ, Name: typeName}, Table: table, Column: name, NotNull: notNull}
	}
	holds := Description{ID: "budgets/0", Params: []Type{{Type: "uuid", Name: "uuid"}}, Columns: []Column{
		column("id", "uuid", "uuid", "category", true),
		column("name", "character varying(80)", "varchar", "category", true),
		column("budget", "numeric(12,2)", "numeric", "category", false),
		column("budget_since", "date", "date", "category", false),
		column("parent_id", "uuid", "uuid", "category", false),
	}}
	lock, problems := Judge([]sqlutil.QueryModel{budgets}, &Result{Version: DescribeVersion, Statements: []Description{holds}})
	if len(problems) > 0 {
		t.Fatalf("problems of a query that holds: %v", problems)
	}
	if len(lock.Queries) != 1 || lock.Queries[0].Hash != Hash(budgets) || lock.Queries[0].Params[0] != "uuid" ||
		len(lock.Queries[0].Columns) != 5 || !lock.Queries[0].Columns[0].NotNull || lock.Queries[0].Columns[2].NotNull {
		t.Fatalf("lock = %+v", lock)
	}

	broken := holds
	broken.Params = []Type{{Type: "text", Name: "text"}}
	broken.Columns = []Column{
		column("id", "uuid", "uuid", "category", true),
		column("name", "integer", "int4", "category", true),
		column("budget_since", "date", "date", "category", false),
		{Name: "parent_id", Type: Type{Type: "uuid", Name: "uuid"}},
		{Name: "extra", Type: Type{Type: "text", Name: "text"}},
	}
	budgets.NotNull = map[string]bool{}
	_, problems = Judge([]sqlutil.QueryModel{budgets}, &Result{Version: DescribeVersion, Statements: []Description{broken}})
	text := strings.Join(problems, "\n")
	for _, want := range []string{
		"query budgets: :parentId is Identity.UUID in BudgetsParams, which binds as UUID, and Postgres reads $1 as text; cast it in the SQL (:parentId::uuid)",
		"query budgets: column name is integer, and BudgetsRow.name is Identity.Name, which a VARCHAR(80) column holds",
		"query budgets: it returns no column budget, which BudgetsRow.budget reads; select it, AS budget",
		"query budgets: it returns column extra, which no field of BudgetsRow reads",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("problems lack %q:\n%s", want, text)
		}
	}

	_, problems = Judge([]sqlutil.QueryModel{budgets}, &Result{Version: DescribeVersion, Statements: []Description{{ID: "budgets/0", Error: `relation "category" does not exist`}}})
	if len(problems) != 1 || problems[0] != `query budgets: Postgres refuses it: relation "category" does not exist` {
		t.Fatalf("a refused statement: %v", problems)
	}

	totals := queries["categoryTotals"]
	described := Description{ID: "categoryTotals/0",
		Params: []Type{{Type: "timestamp with time zone", Name: "timestamptz"}, {Type: "timestamp with time zone", Name: "timestamptz"}, {Type: "uuid[]", Name: "_uuid", Element: "uuid"}},
		Columns: []Column{
			{Name: "category_id", Type: Type{Type: "uuid", Name: "uuid"}},
			{Name: "spent", Type: Type{Type: "bigint", Name: "int8"}},
			column("earned", "bigint", "int8", "line_item", true),
		}}
	totals.NotNull = map[string]bool{"spent": true}
	_, problems = Judge([]sqlutil.QueryModel{totals}, &Result{Version: DescribeVersion, Statements: []Description{described}})
	if len(problems) != 0 {
		t.Fatalf("a column of a NOT NULL column of an inner table, a vouched one and a nullable field: %v", problems)
	}
	described.Columns[2] = column("earned", "bigint", "int8", "category", true)
	_, problems = Judge([]sqlutil.QueryModel{totals}, &Result{Version: DescribeVersion, Statements: []Description{described}})
	if len(problems) != 1 || !strings.Contains(problems[0], "column earned reads category.earned of a table an outer join may leave out, and CategoryTotalsRow.earned is not Nullable") {
		t.Fatalf("a NOT NULL column of an outer joined table: %v", problems)
	}
}

// TestHashCoversWhatAQueryReturnsAndTheProjectionsItReads: a query's hash
// changes with what it returns and with the projection whose view its SQL
// names, and a query of the many rows it returns by default hashes as one
// that says so; a query of the tables alone keeps its hash when the
// projection changes, and the lock names the query over the view alone
// (D95).
func TestHashCoversWhatAQueryReturnsAndTheProjectionsItReads(t *testing.T) {
	schema, queries := fixture(t)
	totals := queries["dashboardTotals"]
	many := totals
	many.Returns = ir.QueryReturnsMany
	optional := totals
	optional.Returns = ir.QueryReturnsOptional
	unset := queries["categoryTotals"]
	unset.Returns = ""
	if Hash(totals) == Hash(many) || Hash(totals) == Hash(optional) || Hash(many) == Hash(optional) {
		t.Fatal("returns changes no hash")
	}
	if Hash(unset) != Hash(queries["categoryTotals"]) {
		t.Fatal("a query of many rows hashes apart from one whose returns is unset, as a lock written before D95 holds it")
	}

	projection := schema.Types["LineListEntry"].Projection
	projection.Joins[0].Kind = "left"
	changed, err := sqlutil.QueriesOf(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range changed {
		switch moved := Hash(q) != Hash(queries[q.Name]); {
		case q.Name == "dashboardLines" && !moved:
			t.Error("a join of the projection dashboardLines reads leaves its hash")
		case q.Name != "dashboardLines" && moved:
			t.Errorf("a join of the projection moves the hash of %s, which does not read its view", q.Name)
		}
	}
	err = CheckLock(fixtureDir, schema)
	if err == nil || !strings.Contains(err.Error(), "query dashboardLines changed since it was verified: its SQL, its params' or row's types, what it returns, or a table or projection it names are not what queries.lock verified") ||
		strings.Contains(err.Error(), "query dashboardTotals") {
		t.Fatalf("a changed projection: %v", err)
	}
}

// TestJudgeAViewsColumns: a required field of a projection holds a column of
// its view that Postgres describes as no NOT NULL column, as every view's
// is; without the projection's word for it, the column may be null (D95).
func TestJudgeAViewsColumns(t *testing.T) {
	_, queries := fixture(t)
	lines := queries["dashboardLines"]
	view := func(name, typ, typeName string) Column {
		return Column{Name: name, Type: Type{Type: typ, Name: typeName}, Table: "line_list", Column: name}
	}
	timestamp := Type{Type: "timestamp with time zone", Name: "timestamptz"}
	described := Description{ID: "dashboardLines/0",
		Params: []Type{timestamp, timestamp, {Type: "uuid[]", Name: "_uuid", Element: "uuid"}, {Type: "uuid[]", Name: "_uuid", Element: "uuid"}, {Type: "text", Name: "text"}},
		Columns: []Column{
			view("id", "uuid", "uuid"), view("date", "timestamp with time zone", "timestamptz"), view("name", "text", "text"),
			view("account_name", "character varying(80)", "varchar"), view("category_name", "character varying(80)", "varchar"),
		}}
	lock, problems := Judge([]sqlutil.QueryModel{lines}, &Result{Version: DescribeVersion, Statements: []Description{described}})
	if len(problems) > 0 {
		t.Fatalf("problems of a query over a view: %v", problems)
	}
	if columns := lock.Queries[0].Columns; !columns[0].NotNull || !columns[3].NotNull || columns[4].NotNull {
		t.Fatalf("locked columns = %+v", columns)
	}
	lines.ViewNotNull = nil
	_, problems = Judge([]sqlutil.QueryModel{lines}, &Result{Version: DescribeVersion, Statements: []Description{described}})
	if len(problems) != 4 || !strings.Contains(problems[0], "column id reads line_list.id, which may be null, and DashboardLinesRow.id is not Nullable") {
		t.Fatalf("a view's columns with no projection's word: %v", problems)
	}
}

// TestFixtureLockIsCurrent: fixture-queries-db's committed queries.lock
// verified each of its queries as the schema declares it now. Write it
// again with TestVerifyFixtureOnPostgres -update.
func TestFixtureLockIsCurrent(t *testing.T) {
	schema, _ := fixture(t)
	if err := CheckLock(fixtureDir, schema); err != nil {
		t.Fatal(err)
	}
}

// TestCheckLock: a lock with no entry for a query, an entry for other SQL
// and an entry for a query no longer declared are each stale, naming the
// query; a service with no queries needs no lock.
func TestCheckLock(t *testing.T) {
	schema, queries := fixture(t)
	dir := t.TempDir()
	if err := CheckLock(dir, schema); err == nil || !strings.Contains(err.Error(), "query accountLines has not been verified: queries.lock holds no entry for it") {
		t.Fatalf("no lock: %v", err)
	}
	lock := &Lock{Version: LockVersion}
	for _, q := range queries {
		hash := Hash(q)
		if q.Name == "accountLines" {
			hash = "sha256:00"
		}
		lock.Queries = append(lock.Queries, LockedQuery{Name: q.Name, Hash: hash})
	}
	lock.Queries = append(lock.Queries, LockedQuery{Name: "gone", Hash: "sha256:01"})
	if err := WriteLock(filepath.Join(dir, LockFile), lock); err != nil {
		t.Fatal(err)
	}
	err := CheckLock(dir, schema)
	if err == nil || !strings.Contains(err.Error(), "query accountLines changed since it was verified") ||
		!strings.Contains(err.Error(), "queries.lock names query gone, which the schema no longer declares") ||
		strings.Contains(err.Error(), "query budgets") {
		t.Fatalf("a stale lock: %v", err)
	}
	if err := CheckLock(t.TempDir(), &ir.Schema{Name: "empty-db", Kind: ir.SchemaKindDB, Types: map[string]*ir.TypeDef{}}); err != nil {
		t.Fatalf("a service with no queries: %v", err)
	}
}

// TestVerifyFixtureOnPostgres verifies fixture-queries-db's queries on the
// Postgres at SUPERSCHEMATIC_MIGRATE_TEST_DATABASE_URL through the
// migration runner, built from runtime/migrate/go, and requires the lock
// the fixture commits; with -update it writes it.
func TestVerifyFixtureOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds the migration runner")
	}
	url := os.Getenv("SUPERSCHEMATIC_MIGRATE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set SUPERSCHEMATIC_MIGRATE_TEST_DATABASE_URL to a Postgres whose role may create a database")
	}
	runner := filepath.Join(t.TempDir(), "superschematic-migrate")
	build := exec.Command("go", "build", "-o", runner, "./cmd/superschematic-migrate")
	build.Dir = filepath.Join("..", "..", "runtime", "migrate", "go")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the migration runner: %v\n%s", err, out)
	}
	schema, _ := fixture(t)
	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: schema.Name})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := sqlgen.WriteDDL(ddl, dir); err != nil {
		t.Fatal(err)
	}
	createSQL, err := os.ReadFile(filepath.Join(dir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	lock, problems, err := Verify(context.Background(), Options{Schema: schema, CreateSQL: string(createSQL), DatabaseURL: url, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) > 0 {
		t.Fatalf("problems: %s", strings.Join(problems, "\n"))
	}
	path := filepath.Join(fixtureDir, LockFile)
	if *update {
		if err := WriteLock(path, lock); err != nil {
			t.Fatal(err)
		}
	}
	written := filepath.Join(t.TempDir(), LockFile)
	if err := WriteLock(written, lock); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(written)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is not what verify writes; run with -update:\n%s", path, got)
	}
}
