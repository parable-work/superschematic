package tsgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// releasedFixture is the fixture whose Part keeps its released rows (D139).
const releasedFixture = "fixture-version-graph-db"

// releasedLiveRoot is the live condition of part_released when the Recipe
// graph's root is soft-deletable: the row's root is a live row of recipe.
const releasedLiveRoot = "EXISTS (SELECT 1 FROM recipe AS live_root WHERE live_root.id = part_released.recipe_id AND live_root.deleted_at IS NULL)"

// softDeletableRoot is fixture-version-graph-db with a deletedAt and a
// deletedBy on its Recipe graph's root, as a project's root that is
// soft-deleted has (D139): the fixture's own root has none.
func softDeletableRoot(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, releasedFixture))
	if err != nil {
		t.Fatalf("load %s: %v", releasedFixture, err)
	}
	root := schema.Types["Recipe"]
	if root == nil || root.VersionGraph == nil {
		t.Fatalf("the fixture has no Recipe graph root: %+v", root)
	}
	root.Fields = append(root.Fields,
		&ir.FieldDef{Name: "deletedAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}},
		&ir.FieldDef{Name: "deletedBy", TypeRef: ir.TypeRef{Name: "Identity.UUID"}},
	)
	return schema
}

// TestAReleasedTableOfASoftDeletableRootReadsItsLiveRoots: orm.ts's
// definition of PartReleased is readOnly, so the ORM holds its reads alone
// (D139); with a soft-deletable root it says softDelete too, with the
// root's live row as its statements' live condition, so the runtime holds
// each read to it as it holds a soft-deletable table's reads to its own.
// The fixture's own root, which has no deletedAt, leaves the definition
// readOnly alone.
func TestAReleasedTableOfASoftDeletableRootReadsItsLiveRoots(t *testing.T) {
	definition := func(t *testing.T, schema *ir.Schema) (ORMTable, string) {
		t.Helper()
		output, err := Generate(schema, Options{SchemaName: releasedFixture})
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		var table ORMTable
		for _, candidate := range output.ORM.Tables {
			if candidate.Type == "PartReleased" {
				table = candidate
			}
		}
		dir := t.TempDir()
		if err := WriteTypes(output, dir); err != nil {
			t.Fatalf("write: %v", err)
		}
		text, err := os.ReadFile(filepath.Join(dir, ORMFile))
		if err != nil {
			t.Fatal(err)
		}
		_, after, _ := strings.Cut(string(text), "export const partReleasedTable: TableDefinition = {")
		body, _, _ := strings.Cut(after, "\n};\n")
		return table, body
	}

	plain, err := loader.LoadService(filepath.Join(fixturesDir, releasedFixture))
	if err != nil {
		t.Fatalf("load %s: %v", releasedFixture, err)
	}
	table, body := definition(t, plain)
	if !table.ReadOnly || table.LiveRoot || !strings.Contains(body, "\n  readOnly: true,\n") || strings.Contains(body, "softDelete: true") || strings.Contains(body, "live_root") {
		t.Errorf("PartReleased of a root without deletedAt: ReadOnly %v, LiveRoot %v, definition:\n%s", table.ReadOnly, table.LiveRoot, body)
	}

	table, body = definition(t, softDeletableRoot(t))
	if !table.ReadOnly || !table.LiveRoot || table.SoftDelete {
		t.Errorf("PartReleased of a soft-deletable root: ReadOnly %v, LiveRoot %v, SoftDelete %v", table.ReadOnly, table.LiveRoot, table.SoftDelete)
	}
	for _, want := range []string{
		"\n  softDelete: true,\n  readOnly: true,\n",
		`live: " AND ` + releasedLiveRoot + `",`,
		`liveWhere: " WHERE ` + releasedLiveRoot + `",`,
		`liveCondition: "` + releasedLiveRoot + `",`,
		`softDelete: "",`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("partReleasedTable lacks %q:\n%s", want, body)
		}
	}
}

// TestReleasedRowsOfASoftDeletedRootOnPostgres generates the TypeScript
// package of fixture-version-graph-db with a soft-deletable root,
// type-checks it and runs testdata/orm/released.test.ts in it with bun
// against the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (D139):
// the ORM holds PartReleased's reads alone, and they leave out the rows of
// a root its repository soft-deletes, unless they include deleted rows,
// and read them again once the root is restored. It skips without the
// database, and without bun unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says
// the gate must run.
func TestReleasedRowsOfASoftDeletedRootOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the released rows of a soft-deleted root against Postgres")
	}
	schema := softDeletableRoot(t)
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{name: releasedFixture, schema: schema}})
	dir := filepath.Join(typesRoot, releasedFixture)
	harness, err := ormTests.ReadFile("testdata/orm/harness.ts")
	if err != nil {
		t.Fatal(err)
	}
	source, err := ormTests.ReadFile("testdata/orm/released.test.ts")
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"harness.ts":       string(harness),
		"released.test.ts": string(source),
		"create.sql":       createSQLOf(t, schema, releasedFixture),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "released.junit.xml")
	run := exec.Command(bunPath, "test", "./released.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = dir
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("the released rows' test failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range []string{
		"the ORM holds a released table's reads alone, on the pool and in a transaction",
		"a soft-deleted root's released rows are out of every read unless it includes deleted rows, and a restore reads them again",
	} {
		tc, ok := cases[name]
		if !ok {
			t.Fatalf("bun did not run %q:\n%s", name, junit)
		}
		if tc.Failure != nil || tc.Skipped != nil {
			t.Fatalf("%q did not pass:\n%s", name, junit)
		}
	}
	t.Logf("bun ran the released rows' tests:\n%s", out)
}
