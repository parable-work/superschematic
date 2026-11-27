package rustgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// releasedFixture is the fixture whose Part keeps its released rows
// (D139): the loader gives it PartReleased, which the Recipe graph's
// release writes.
const releasedFixture = "fixture-version-graph-db"

// repositoryReads are the methods of the runtime's typed repository that
// read, which the repository of a table that is only read keeps, and
// repositoryWrites every other one, which it leaves out: the writes, a
// behavior's operations, and untyped, the repository over JSON objects,
// which has them all.
var (
	repositoryReads  = []string{"find_many", "find_one", "find_page", "get_one"}
	repositoryWrites = []string{
		"create_many", "create_one", "delete_one", "hard_delete_one", "merge_into", "operation",
		"reencrypt", "table_operation", "untyped", "update_many", "update_one", "upsert_many", "upsert_one",
	}
)

// generateReleased is fixture-version-graph-db's Rust types with both
// dialects' repositories.
func generateReleased(t *testing.T) *ModuleOutput {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, releasedFixture))
	if err != nil {
		t.Fatalf("load %s: %v", releasedFixture, err)
	}
	if got := schema.Types["PartReleased"]; got == nil || got.ReleasedOf != "Part" {
		t.Fatalf("%s has no PartReleased that holds the released rows of Part: %+v", releasedFixture, got)
	}
	output, err := Generate(schema, Options{SchemaName: releasedFixture, SQLite: true, Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output.ORM == nil || output.ORM.SQLite == "" {
		t.Fatalf("%s's types carry no repositories on SQLite", releasedFixture)
	}
	return output
}

// publicMethods are the public methods of the impl block of source whose
// lines up to its brace the pattern opens matches, sorted. indent is the
// block's own.
func publicMethods(t *testing.T, source, opens, indent string) []string {
	t.Helper()
	start := regexp.MustCompile(`(?m)^` + indent + opens + `\{$`).FindStringIndex(source)
	if start == nil {
		t.Fatalf("no impl block opens with %s", opens)
	}
	body := source[start[1]:]
	end := strings.Index(body, "\n"+indent+"}\n")
	if end < 0 {
		t.Fatalf("the impl block that opens with %s does not close", opens)
	}
	var names []string
	for _, m := range regexp.MustCompile(`(?m)^`+indent+`    pub (?:async )?fn (\w+)`).FindAllStringSubmatch(body[:end], -1) {
		names = append(names, m[1])
	}
	slices.Sort(names)
	return names
}

// ormModule is the text of the module `pub mod name` of orm.rs.
func ormModule(t *testing.T, orm, name string) string {
	t.Helper()
	_, rest, found := strings.Cut(orm, "\npub mod "+name+" {\n")
	if !found {
		t.Fatalf("src/%s has no module %s", ORMFile, name)
	}
	body, _, found := strings.Cut(rest, "\n}\n")
	if !found {
		t.Fatalf("the module %s of src/%s does not close", name, ORMFile)
	}
	return body
}

// TestReleasedRowsRepositoryOnlyReads: the repository of a table that
// holds a graph member's released rows (D139) only reads. The table's info
// carries the flag from the IR's ReleasedOf, orm.rs writes no Create and no
// Update of it, and on each dialect its repository has every read of the
// runtime's typed repository, which an ordinary table's repository is, and
// no other method of it. Part, the member, keeps the runtime's repository,
// with its writes.
func TestReleasedRowsRepositoryOnlyReads(t *testing.T) {
	output := generateReleased(t)
	tables := map[string]ORMTable{}
	for _, table := range output.ORM.Tables {
		tables[table.Type] = table
		if want := table.Type == "PartReleased"; table.ReadOnly != want {
			t.Errorf("%s: ReadOnly = %v, want %v", table.Type, table.ReadOnly, want)
		}
	}
	released, part := tables["PartReleased"], tables["Part"]
	if released.ReleasedOf != "Part" || len(released.Row) == 0 || len(released.Filter) == 0 {
		t.Errorf("PartReleased: ReleasedOf = %q with %d row members and %d filter members, want Part's and some of each",
			released.ReleasedOf, len(released.Row), len(released.Filter))
	}
	if len(released.Create) != 0 || len(released.Update) != 0 {
		t.Errorf("PartReleased has %d create members and %d update members, want none", len(released.Create), len(released.Update))
	}
	if len(part.Create) == 0 || len(part.Update) == 0 {
		t.Errorf("Part has %d create members and %d update members, want some of each", len(part.Create), len(part.Update))
	}
	for _, refused := range output.ORM.SQLiteRefused {
		if refused.Type == "PartReleased" || refused.Type == "Part" {
			t.Fatalf("%s has no repository on SQLite: %s", refused.Type, refused.Reason)
		}
	}

	dir := t.TempDir()
	if err := WriteTypes(output, dir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "src", ORMFile))
	if err != nil {
		t.Fatal(err)
	}
	orm := string(data)
	for _, want := range []string{
		"//! `PartReleased` holds the released rows of `Part`, which the graph's\n//! release owns (D139): its repository reads them and has no write.\n",
		"pub struct PartReleasedRow {",
		"pub struct PartReleasedFilter {",
		"pub struct PartCreate {",
		"pub struct PartUpdate {",
	} {
		if !strings.Contains(orm, want) {
			t.Errorf("src/%s lacks %q", ORMFile, want)
		}
	}
	for _, unwanted := range []string{"PartReleasedCreate", "PartReleasedUpdate"} {
		if strings.Contains(orm, unwanted) {
			t.Errorf("src/%s names %s, and a released row has no write", ORMFile, unwanted)
		}
	}

	root := testpaths.RepoRoot(t)
	for _, dialect := range []struct {
		name, client, on string
	}{
		{"postgres", "<'a, C>", "a Postgres client"},
		{"sqlite", "<'a>", "a SQLite connection"},
	} {
		t.Run(dialect.name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join(root, "runtime", "http", "rust", "src", "orm", dialect.name+".rs"))
			if err != nil {
				t.Fatal(err)
			}
			// Every method of the runtime's repository is one this test
			// names a read or a write, so a read it gains is one the
			// released rows' repository must gain too.
			runtime := publicMethods(t, string(source), `impl<[^\n]*> Typed<[^\n]*>\nwhere\n(?:    [^\n]+\n)+`, "")
			named := slices.Concat(repositoryReads, repositoryWrites)
			slices.Sort(named)
			if !slices.Equal(runtime, named) {
				t.Fatalf("the runtime's typed repository has the methods %v, and this test names %v reads or writes", runtime, named)
			}

			mod := ormModule(t, orm, dialect.name)
			got := publicMethods(t, mod, `impl(?:<[^\n]*>)? PartReleasedRepository<[^\n]*> `, "    ")
			if !slices.Equal(got, repositoryReads) {
				t.Errorf("PartReleased's repository has the methods %v, want the reads %v alone", got, repositoryReads)
			}
			for _, want := range []string{
				"    /// The repository of `PartReleased` on " + dialect.on + ", which only reads:\n" +
					"    /// the table holds the released rows of `Part`, and the graph's\n" +
					"    /// release owns them (D139).\n" +
					"    pub struct PartReleasedRepository" + dialect.client + "(\n",
				"        /// The repository of `PartReleased`, which only reads: the graph's\n" +
					"        /// release owns the rows (D139).\n" +
					"        pub fn part_released(&self) -> Result<PartReleasedRepository<'_",
				"            self.0.typed(\"PartReleased\").map(PartReleasedRepository)\n",
				// Part's repository is the runtime's, whose methods are the
				// reads and the writes above.
				"    pub type PartRepository" + dialect.client + " =\n        orm::" + dialect.name + "::Typed<'a, ",
				"PartRow, PartCreate, PartUpdate, PartFilter, IdentityUUID>;\n",
				"            self.0.typed(\"Part\")\n",
			} {
				if !strings.Contains(mod, want) {
					t.Errorf("the module %s of src/%s lacks %q", dialect.name, ORMFile, want)
				}
			}
		})
	}
}

// TestReleasedRowsRepositoryCompiles holds fixture-version-graph-db's types
// crate, with both dialects' repositories, to cargo clippy with warnings
// refused: the released rows' repository (D139) leaves no type and no
// import of the crate unused.
//
// It needs cargo, which it skips without, and runs outside -short.
func TestReleasedRowsRepositoryCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the released rows' repository's cargo clippy in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the released rows' repository's cargo clippy")
	}
	paths := testpaths.Local(t)
	output := generateReleased(t)
	root := testpaths.TempDir(t)
	typesDir := filepath.Join(root, "types", "rust", releasedFixture)
	if err := SetLocalPaths(output, paths, typesDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, typesDir); err != nil {
		t.Fatal(err)
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(root, "target")
	}
	clippy := exec.Command(cargo, "clippy", "--quiet", "--features", "postgres,sqlite", "--", "-D", "warnings")
	clippy.Dir = typesDir
	clippy.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
	if out, err := clippy.CombinedOutput(); err != nil {
		t.Fatalf("cargo clippy of %s: %v\n%s", typesDir, err, out)
	}
}
