package generator

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// Per-record access, references and attachments in the Rust repositories
// on a SQLite file (D156, D154, D182, #611): the build's SQLite DDL applied
// to a file, and the cases the Postgres tests run (TestRustAccessEndToEnd,
// TestRustReferencesEndToEnd) held to the repositories over it. They need
// cargo, which they skip without, and no database server.

//go:embed testdata/access_rust_sqlite.rs
var accessRustSQLiteTest string

//go:embed testdata/references_rust_sqlite.rs
var referencesRustSQLiteTest string

// accessRustSQLiteCases are the cases testdata/access_rust_sqlite.rs runs.
var accessRustSQLiteCases = []string{
	"a private row never appears to another user in a list, a read or a count",
	"a named row is visible to its owner and to the principals it names",
	"a member of a nested team sees a row named for the outer team, and stops seeing it once removed",
	"a document inherits its folder's audience, and follows it when the folder's changes",
	"an agent run sees what whom it acts for sees, and writes no read-only row",
	"a party with a grant reaches only the rows its role's paths allow",
	"an expired or revoked grant is refused on the very next call",
	"a reveal opens the value for a holder of its permission and writes an access-log row",
}

// referencesRustSQLiteCases are the cases testdata/references_rust_sqlite.rs
// runs.
var referencesRustSQLiteCases = []string{
	"a file posted on one row is filed onto a second row, the same object, which lists the first in its backlinks",
	"a caller who cannot read a referenced row sees neither the reference nor a count, and its owner sees a count",
	"a target's delete clears the references to it and tells each row that held one",
	"an upload's bytes are held to what it declared, and an object is deleted once no file names it",
}

// TestRustAccessOnSQLite builds fixture-access-db's Rust types with their
// sqlite feature and runs testdata/access_rust_sqlite.rs over its
// repositories, the runtime's access::sqlite grants and access log, and a
// table operation's reveal, on a SQLite file: an audience private, named
// and inherited, an agent run's, a party's grant reaching through its
// role's paths, a grant revoked, expired or ended by its target's state
// refused at the next call, and a reveal's access-log row.
func TestRustAccessOnSQLite(t *testing.T) {
	runRustOnSQLite(t, "fixture-access-db", "access", accessRustSQLiteTest, accessRustSQLiteCases, func(cfg *schemaconfig.SchemaConfig) {
		sql, _ := cfg.Outputs["sql"].(map[string]any)
		if sql == nil {
			sql = map[string]any{}
			cfg.Outputs["sql"] = sql
		}
		sql["encryption"] = map[string]any{"key": "AccessKeys.ACCESS_FIELD_KEYS"}
	})
}

// TestRustReferencesOnSQLite builds fixture-references-db's Rust types with
// their sqlite feature and runs testdata/references_rust_sqlite.rs over
// its repositories' references() and attachments() on a SQLite file,
// through a bucket in memory: backlinks held to their reader, a target's
// delete clearing its references by trigger, a file filed elsewhere naming
// the same object, and an object released once no file names it.
func TestRustReferencesOnSQLite(t *testing.T) {
	runRustOnSQLite(t, "fixture-references-db", "references", referencesRustSQLiteTest, referencesRustSQLiteCases, nil)
}

func runRustOnSQLite(t *testing.T, db, name, source string, cases []string, configure func(*schemaconfig.SchemaConfig)) {
	if testing.Short() {
		t.Skip("skipping the Rust repositories on SQLite in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the Rust repositories on SQLite")
	}
	paths := testpaths.Local(t)
	outputRoot := testpaths.TempDir(t)
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, db))
	if err != nil {
		t.Fatalf("load %s: %v", db, err)
	}
	types, _ := cfg.Outputs["types"].(map[string]any)
	if types == nil {
		types = map[string]any{}
		cfg.Outputs["types"] = types
	}
	types["rust"] = map[string]any{"enabled": true}
	// The Go ORM, which a DB service always generates, imports its Go
	// types.
	types["go"] = map[string]any{"enabled": true}
	if configure != nil {
		configure(cfg)
	}
	if _, err := Run(schema, cfg, Options{
		OutputRoot:       outputRoot,
		Paths:            paths,
		Naming:           naming.Default(),
		Clock:            codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		DependencyConfig: func(string) (*schemaconfig.SchemaConfig, bool) { return nil, false },
	}); err != nil {
		t.Fatalf("run %s: %v", db, err)
	}
	ddl, err := os.ReadFile(filepath.Join(SQLDir(outputRoot, db), SQLiteSubdir, "create.sql"))
	if err != nil {
		t.Fatalf("the build wrote no SQLite DDL for %s: %v", db, err)
	}
	typesDir := TypesDir(outputRoot, "rust", db)
	crateDir := filepath.Join(outputRoot, name+"-rust-sqlite")
	for _, dir := range []string{"src", "tests"} {
		if err := os.MkdirAll(filepath.Join(crateDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	for file, content := range map[string]string{
		"Cargo.toml":            fmt.Sprintf(rustSQLiteManifest, name, naming.Default().RustTypesCrate(db), filepath.ToSlash(typesDir), filepath.ToSlash(paths.HTTPRuntimeRust)),
		"Cargo.lock":            string(lock),
		"create.sql":            string(ddl),
		"src/lib.rs":            "//! " + db + "'s Rust repositories on SQLite: see tests/.\n",
		"tests/" + name + ".rs": source,
	} {
		if err := os.WriteFile(filepath.Join(crateDir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(outputRoot, "target")
	}
	var out []byte
	for _, args := range [][]string{
		{"clippy", "--quiet", "--all-targets", "--", "-D", "warnings"},
		{"test", "--quiet", "--", "--nocapture"},
	} {
		cmd := exec.Command(cargo, args...)
		cmd.Dir = crateDir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", args[0], crateDir, err, out)
		}
	}
	t.Logf("tests/%s.rs:\n%s", name, out)
	for _, c := range cases {
		if !strings.Contains(string(out), "case: "+c+"\n") {
			t.Errorf("the case %q did not run:\n%s", c, out)
		}
	}
}

// rustSQLiteManifest is the test crate's Cargo.toml, given its name, the
// types crate's name and directory and the HTTP runtime's directory.
const rustSQLiteManifest = `# A DB's generated Rust repositories on a SQLite file (D182, #611).
[package]
name = "%s-rust-sqlite"
version = "0.0.0"
edition = "2021"
publish = false

[dev-dependencies]
async-trait = "0.1.89"
http = "1"
ring = "0.17.14"
serde_json = "1.0.149"
types = { package = %q, path = %q, features = ["sqlite"] }
runtime = { package = "superschematic-http-runtime", path = %q, features = ["sqlite"] }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
