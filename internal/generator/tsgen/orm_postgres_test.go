package tsgen

import (
	"embed"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed testdata/orm/*.ts
var ormTests embed.FS

// repositoryCase is a generated package's repository test: the fixture,
// the test files of testdata/orm it runs, and the tests that must pass.
type repositoryCase struct {
	fixture string
	files   []string
	tests   []string
}

// repositoryCases are the TypeScript repositories' Postgres tests (D79),
// the counterparts of the Go ORM's: fixture-ledger-db's CRUD, filters,
// search, pages, upsert, acting user, soft delete, transaction with a
// queued message and raw rows through the codec; fixture-db's pages, the
// counterpart of page_postgres_test.go; fixture-queue-db's order by on a
// keyword table, order_by_postgres_test.go's, and its keyed queues and
// trigger under a transaction of the repositories (D80); fixture-db's JSON
// column through the codec; fixture-user-model-db's user model tables,
// user_model_postgres_test.go's; and fixture-ddl-db's defaults, checks,
// SET NULL, JSONB and expression and partial unique indexes (D83) under
// the repositories.
var repositoryCases = []repositoryCase{
	{fixture: "fixture-ledger-db", files: []string{"ledger.test.ts"}, tests: []string{
		"createOne and getOne read a row as the types hold it, its key base62 and its user stamped",
		"a relation loads in a batch through getManyByIds, keyed by base62",
		"a write that stamps a user needs one, as the Go ORM refuses with ErrNoUserInContext",
		"createMany writes one INSERT, DEFAULT for a column one row leaves unset that another sets",
		"the filters mirror the Go ORM: equality, in, ranges, null, a key, a relation, a reference and a search",
		"findMany counts every row the filter finds, and reads a limit after an offset, in order",
		"findPage reads keyset pages by a sort field, ties broken by the key, and refuses a key that names no row",
		"updateOne stamps updatedAt and updatedBy, sets a column null with null, and refuses a key no live row holds",
		"upsertOne inserts, then updates the row that holds its unique key, and an empty update leaves it as it is",
		"a soft delete stamps deletedAt and deletedBy, and reads leave the row out unless they include deleted rows",
		"a transaction writes rows and enqueues their message together, and rolls both back when it throws",
		"a nullable JSON column's null is SQL NULL, and a JSON string in it stays a string",
		"a field of a @jsonField type is a JSON object in its JSONB column, and reads back as its value",
		"a raw query on connectPostgres's pool decodes through decodeRow to wire-form ids and numeric amounts",
	}},
	{fixture: "fixture-db", files: []string{"page.test.ts", "json.test.ts"}, tests: []string{
		"pages by text, a number and a time, each way, ties broken by the key in the sort's direction",
		"a filter and a search sit beside the keyset, and a deleted member is out of every page unless included",
		"a key is the row's id alone, which places a page however the row changed since",
		"a page reads in a transaction",
		"a JSON column holds a string, a number, a list, an object and the JSON null as they are, with the pool's parsers on and off",
		"eq and in on a JSON column compare JSON values as JSONB does",
	}},
	{fixture: "fixture-queue-db", files: []string{"order_by.test.ts", "queue_key.test.ts"}, tests: []string{
		"findMany and findOne order a keyword-named table by a column, each way",
		"an enqueue in a transaction of the ORM keeps one waiting message per key: skip the first, replace the latest",
		"a line the repositories write enqueues its order's change through the trigger, under the queue's key with the code's",
		"a transaction that throws enqueues nothing, by its code or by its trigger",
	}},
	{fixture: "fixture-user-model-db", files: []string{"user_model.test.ts"}, tests: []string{
		"a login that differs only in case is the same login, and the identity tables keep their keys unique",
		"deleting a user deletes its sessions, credential and grants, and deleting a role its grants",
		"a principal's base62 subject matches its user row through uuidToDb",
	}},
	{fixture: "fixture-ddl-db", files: []string{"ddl.test.ts"}, tests: []string{
		"a create that leaves a defaulted field unset writes what the column's DEFAULT gives a raw insert",
		"a value a column's CHECK refuses fails the write, naming the constraint",
		"deleting a category clears the references SET NULL names, and keeps their rows",
		"a @jsonField value is a JSON object in its JSONB column, read back as the object",
		"an upsert by a unique @index over an expression, or with a where, infers the index",
	}},
}

// TestRepositoriesOnPostgres generates each repositoryCase's TypeScript
// types package, orm.ts among it, type-checks it (buildTSPackages), and
// runs its test with bun against the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, each in a schema of its own
// (D79). It skips without the database, and without bun unless
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestRepositoriesOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the TypeScript repositories against Postgres")
	}
	harness, err := ormTests.ReadFile("testdata/orm/harness.ts")
	if err != nil {
		t.Fatal(err)
	}
	var packages []tsPackageCase
	files := map[string]map[string]string{}
	for _, rc := range repositoryCases {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, rc.fixture))
		if err != nil {
			t.Fatalf("load %s: %v", rc.fixture, err)
		}
		packages = append(packages, tsPackageCase{name: rc.fixture, schema: schema})
		files[rc.fixture] = map[string]string{
			"harness.ts": string(harness),
			"create.sql": createSQLOf(t, schema, rc.fixture),
		}
		for _, file := range rc.files {
			source, err := ormTests.ReadFile("testdata/orm/" + file)
			if err != nil {
				t.Fatal(err)
			}
			files[rc.fixture][file] = string(source)
		}
	}
	typesRoot, bunPath := buildTSPackages(t, packages)
	// The tests are written once the packages type-check: bun runs them
	// without a check of its own, and tsc has no bun:test.
	for fixture, extra := range files {
		for name, text := range extra {
			if err := os.WriteFile(filepath.Join(typesRoot, fixture, name), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, rc := range repositoryCases {
		t.Run(rc.fixture, func(t *testing.T) {
			dir := filepath.Join(typesRoot, rc.fixture)
			report := filepath.Join(dir, "orm.junit.xml")
			// A test and a hook, which creates a database and runs its DDL,
			// may take longer than bun's 5s on a loaded machine.
			args := []string{"test"}
			for _, file := range rc.files {
				args = append(args, "./"+file)
			}
			run := exec.Command(bunPath, append(args, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)...)
			run.Dir = dir
			out, err := run.CombinedOutput()
			if err != nil {
				t.Fatalf("the repository test failed: %v\n%s", err, out)
			}
			junit, err := os.ReadFile(report)
			if err != nil {
				t.Fatalf("read bun's report: %v\n%s", err, out)
			}
			cases, err := junitTestcases(junit)
			if err != nil {
				t.Fatalf("parse bun's report: %v\n%s", err, junit)
			}
			for _, name := range rc.tests {
				tc, ok := cases[name]
				if !ok {
					t.Fatalf("bun did not run %q:\n%s", name, junit)
				}
				if tc.Failure != nil || tc.Skipped != nil {
					t.Fatalf("%q did not pass:\n%s", name, junit)
				}
			}
			if len(cases) != len(rc.tests) {
				t.Fatalf("bun ran %d tests of %s, and the case names %d:\n%s", len(cases), strings.Join(rc.files, ", "), len(rc.tests), junit)
			}
			t.Logf("bun ran the repository tests:\n%s", out)
		})
	}
}

// createSQLOf is the Postgres create.sql of schema, with its extensions
// created in public first (pgtest.CreateSQL), as the Go ORM's tests apply
// it.
func createSQLOf(t *testing.T, schema *ir.Schema, name string) string {
	t.Helper()
	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: name})
	if err != nil {
		t.Fatalf("generate %s's DDL: %v", name, err)
	}
	dir := t.TempDir()
	if err := sqlgen.WriteDDL(ddl, dir); err != nil {
		t.Fatalf("write %s's DDL: %v", name, err)
	}
	createSQL, err := os.ReadFile(filepath.Join(dir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	return string(pgtest.CreateSQL(createSQL))
}
