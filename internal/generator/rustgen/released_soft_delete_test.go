package rustgen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

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

// TestReleasedRowsOfASoftDeletableRootReadItsLiveRoots: the definition
// orm.rs carries of PartReleased, on each dialect, says softDelete when the
// graph's root is soft-deletable, with the root's live row as its
// statements' live condition, so the runtime holds every read of the
// released rows to it (D139), as it holds a soft-deletable table's to its
// own, and writes no soft delete of them; the fixture's own root, which has
// no deletedAt, leaves the definition as it was.
func TestReleasedRowsOfASoftDeletableRootReadItsLiveRoots(t *testing.T) {
	type definition struct {
		SoftDelete bool              `json:"softDelete"`
		Statements map[string]string `json:"statements"`
	}
	released := func(t *testing.T, literal string) definition {
		t.Helper()
		// The definition is a raw string literal, r#"..."#.
		document := literal[strings.Index(literal, `"`)+1 : strings.LastIndex(literal, `"`)]
		var orm struct {
			Tables []struct {
				Name string `json:"name"`
				definition
			} `json:"tables"`
		}
		if err := json.Unmarshal([]byte(document), &orm); err != nil {
			t.Fatalf("read the ORM's definition: %v", err)
		}
		for _, table := range orm.Tables {
			if table.Name == "PartReleased" {
				return table.definition
			}
		}
		t.Fatal("the ORM's definition has no PartReleased")
		return definition{}
	}
	clock := codegen.FixedClock(time.Unix(0, 0).UTC())

	plain := generateReleased(t)
	for name, document := range map[string]string{"postgres": plain.ORM.Postgres, "sqlite": plain.ORM.SQLite} {
		if got := released(t, document); got.SoftDelete || got.Statements["live"] != "" {
			t.Errorf("%s: PartReleased of a root without deletedAt = %+v, want no live condition", name, got)
		}
	}
	output, err := Generate(softDeletableRoot(t), Options{SchemaName: releasedFixture, SQLite: true, Clock: clock})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for _, table := range output.ORM.Tables {
		if want := table.Type == "PartReleased"; table.LiveRoot != want {
			t.Errorf("%s: LiveRoot = %v, want %v", table.Type, table.LiveRoot, want)
		}
	}
	for name, document := range map[string]string{"postgres": output.ORM.Postgres, "sqlite": output.ORM.SQLite} {
		got := released(t, document)
		if !got.SoftDelete || got.Statements["live"] != " AND "+releasedLiveRoot || got.Statements["liveWhere"] != " WHERE "+releasedLiveRoot ||
			got.Statements["liveCondition"] != releasedLiveRoot || got.Statements["softDelete"] != "" {
			t.Errorf("%s: PartReleased of a soft-deletable root = %+v, want softDelete and the root's live row", name, got)
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
	if want := "//! release owns (D139): its repository reads them and has no write. Its\n//! reads leave out the rows of a soft-deleted root, unless they include\n//! deleted rows, and a restore of the root sees them again.\n"; !strings.Contains(string(data), want) {
		t.Errorf("src/%s lacks %q", ORMFile, want)
	}
}

// TestReleasedRowsOfASoftDeletedRootOnPostgres builds the Rust types crate
// of fixture-version-graph-db with a soft-deletable root, with its postgres
// feature, and runs releasedSoftDeleteTest in it against the Postgres
// SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL names (D139): the
// repository of PartReleased leaves out the rows of a soft-deleted root,
// unless the read includes deleted rows, keeps another root's, and reads
// the same rows again once the root is restored. The test creates a
// database of its own there and drops it after.
//
// It needs cargo, which it skips without, and runs outside -short.
func TestReleasedRowsOfASoftDeletedRootOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the released rows of a soft-deleted root in -short mode")
	}
	dsn := os.Getenv("SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL to run the released rows of a soft-deleted root against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the released rows of a soft-deleted root")
	}
	schema := softDeletableRoot(t)
	output, err := Generate(schema, Options{SchemaName: releasedFixture, Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: releasedFixture})
	if err != nil {
		t.Fatalf("generate the DDL: %v", err)
	}
	// The manifest's path dependencies are relative to the crate, so the
	// crate's directory is the real one, not a symlink to it.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlgen.WriteDDL(ddl, filepath.Join(root, "sql")); err != nil {
		t.Fatalf("write the DDL: %v", err)
	}
	createSQL := filepath.Join(root, "sql", "create.sql")
	pgtest.WriteCreateSQL(t, createSQL, createSQL)
	outDir := filepath.Join(root, output.CrateName)
	if err := SetLocalPaths(output, testpaths.Local(t), outDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	manifest := filepath.Join(outDir, "Cargo.toml")
	cargoToml, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	cargoToml = append(cargoToml, []byte(`
[dev-dependencies]
tokio = { version = "1", features = ["macros", "rt-multi-thread"] }
tokio-postgres = "0.7.18"
`)...)
	if err := os.WriteFile(manifest, cargoToml, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outDir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	crate := strings.ReplaceAll(output.CrateName, "-", "_")
	if err := os.WriteFile(filepath.Join(outDir, "tests", "released.rs"), []byte(strings.ReplaceAll(releasedSoftDeleteTest, "CRATE", crate)), 0o644); err != nil {
		t.Fatal(err)
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(root, "target")
	}
	cmd := exec.Command(cargo, "test", "--test", "released", "--features", "postgres")
	cmd.Dir = outDir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target, "RELEASED_DDL="+createSQL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the generated crate's released rows test: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "test released_rows_of_a_soft_deleted_root ... ok") {
		t.Fatalf("the generated crate did not run released_rows_of_a_soft_deleted_root:\n%s", out)
	}
}

// releasedSoftDeleteTest is the generated crate's tests/released.rs; CRATE
// is the crate's name as Rust spells it. Two recipes, each with released
// parts written as a release writes them; one is soft-deleted, read, and
// restored.
const releasedSoftDeleteTest = `use std::str::FromStr;

use CRATE::*;

/// Drops the test's database when the test ends, and when it panics.
struct DropDatabase {
    dsn: String,
    name: String,
}

impl Drop for DropDatabase {
    fn drop(&mut self) {
        let (dsn, name) = (self.dsn.clone(), self.name.clone());
        let _ = std::thread::spawn(move || {
            let runtime = tokio::runtime::Builder::new_current_thread().enable_all().build().expect("a runtime");
            runtime.block_on(async {
                let (client, connection) = tokio_postgres::connect(&dsn, tokio_postgres::NoTls).await.expect("connect");
                tokio::spawn(connection);
                let _ = client.batch_execute(&format!("DROP DATABASE IF EXISTS {name} WITH (FORCE)")).await;
            });
        })
        .join();
    }
}

fn cook() -> String {
    "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90".to_owned()
}

/// A recipe, a tagged commit of it and the released parts a release of
/// that commit writes; the recipe's key.
async fn released(sql: &tokio_postgres::Client, title: &str, headings: &[&str]) -> IdentityUUID {
    let root: String = sql
        .query_one("INSERT INTO recipe (title, created_by) VALUES ($1, $2::text::uuid) RETURNING id::text", &[&title, &cook()])
        .await
        .expect("insert the recipe")
        .get(0);
    let reference: String = sql
        .query_one(
            "INSERT INTO recipe_ref (root_id, name, created_by, updated_by) VALUES ($1::text::uuid, 'main', $2::text::uuid, $2::text::uuid) RETURNING id::text",
            &[&root, &cook()],
        )
        .await
        .expect("insert the ref")
        .get(0);
    let commit: String = sql
        .query_one(
            "INSERT INTO recipe_commit (root_id, ref_id, schema_epoch, content_hash, \"sequence\", created_by) VALUES ($1::text::uuid, $2::text::uuid, 1, 'hash', 1, $3::text::uuid) RETURNING id::text",
            &[&root, &reference, &cook()],
        )
        .await
        .expect("insert the commit")
        .get(0);
    for (i, heading) in headings.iter().enumerate() {
        let rank = char::from(b'F' + i as u8).to_string();
        sql.execute(
            "INSERT INTO part_released (recipe_id, entity_key, rank, heading, commit_id, \"sequence\") VALUES ($1::text::uuid, gen_random_uuid(), $2, $3, $4::text::uuid, 1)",
            &[&root, &rank, heading, &commit],
        )
        .await
        .expect("insert a released part");
    }
    IdentityUUID::from_str(&root).expect("a UUID")
}

fn headings(rows: &[orm::PartReleasedRow]) -> Vec<String> {
    let mut out: Vec<String> = rows.iter().map(|row| row.heading.clone()).collect();
    out.sort();
    out
}

#[tokio::test]
async fn released_rows_of_a_soft_deleted_root() {
    let dsn = std::env::var("SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL").expect("the database");
    let ddl = std::fs::read_to_string(std::env::var("RELEASED_DDL").expect("the DDL")).expect("read the DDL");
    let config = |database: Option<&str>| {
        let mut config: tokio_postgres::Config = dsn.parse().expect("the database URL");
        if let Some(database) = database {
            config.dbname(database);
        }
        config
    };
    let (admin, connection) = config(None).connect(tokio_postgres::NoTls).await.expect("connect");
    tokio::spawn(connection);
    let nanos = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).expect("clock").as_nanos();
    let database = format!("released_soft_rust_{nanos}_{}", std::process::id());
    admin.batch_execute(&format!("CREATE DATABASE {database}")).await.expect("create the database");
    let _dropper = DropDatabase { dsn: dsn.clone(), name: database.clone() };
    let (sql, connection) = config(Some(&database)).connect(tokio_postgres::NoTls).await.expect("connect");
    tokio::spawn(connection);
    sql.batch_execute(&ddl).await.expect("apply the DDL");

    let bread = released(&sql, "Bread", &["Mix", "Knead", "Bake"]).await;
    released(&sql, "Soup", &["Boil"]).await;
    let manager = orm::postgres::deadpool_postgres::Manager::new(config(Some(&database)), tokio_postgres::NoTls);
    let pool = orm::postgres::deadpool_postgres::Pool::builder(manager).max_size(1).build().expect("the pool");
    let client = pool.get().await.expect("a connection of the pool");
    let repositories = orm::postgres::Repositories::new(&client).expect("the repositories");
    let parts = repositories.part_released().expect("the repository of PartReleased");
    let of_bread = orm::PartReleasedFilter { recipe_id: Some(orm::Equals::eq(bread.clone())), ..Default::default() };
    let reads = orm::FindOptions::default();
    let deleted_too = orm::FindOptions { include_deleted: true, ..Default::default() };

    let before = parts.find_many(Some(&of_bread), &reads).await.expect("find Bread's released parts");
    assert_eq!(before.total, Some(3));
    let part = before.rows[0].clone();

    // Bread is soft-deleted: the rows stay in the table, and every read
    // leaves them out; Soup's stay.
    let bread_text = bread.to_uuid().to_string();
    sql.execute("UPDATE recipe SET deleted_at = now(), deleted_by = $2::text::uuid WHERE id = $1::text::uuid", &[&bread_text, &cook()])
        .await
        .expect("soft-delete Bread");
    let kept: i64 = sql
        .query_one("SELECT count(*) FROM part_released WHERE recipe_id = $1::text::uuid", &[&bread_text])
        .await
        .expect("count Bread's rows")
        .get(0);
    assert_eq!(kept, 3, "the soft delete clears no row");
    let after = parts.find_many(None, &reads).await.expect("find every released part");
    assert_eq!((after.total, headings(&after.rows)), (Some(1), vec!["Boil".to_owned()]));
    assert_eq!(parts.find_many(Some(&of_bread), &reads).await.expect("find Bread's").total, Some(0));
    assert!(matches!(parts.get_one(&part.id).await, Err(orm::Error::NotFound(_))), "get_one of a soft-deleted root's part");
    assert!(parts.find_one(Some(&of_bread), &reads).await.expect("find one of Bread's").is_none());
    let page = parts.find_page(None, &orm::PageOptions::default()).await.expect("a page");
    assert_eq!(headings(&page.rows), vec!["Boil".to_owned()]);
    // A read that includes deleted rows reads them, as it reads a
    // soft-deleted row.
    let included = parts.find_many(Some(&of_bread), &deleted_too).await.expect("find Bread's, deleted included");
    assert_eq!(headings(&included.rows), vec!["Bake".to_owned(), "Knead".to_owned(), "Mix".to_owned()]);
    let page = parts
        .find_page(Some(&of_bread), &orm::PageOptions { include_deleted: true, ..Default::default() })
        .await
        .expect("a page, deleted included");
    assert_eq!(page.rows.len(), 3);

    // A restore of Bread reads the same rows again.
    sql.execute("UPDATE recipe SET deleted_at = NULL, deleted_by = NULL WHERE id = $1::text::uuid", &[&bread_text])
        .await
        .expect("restore Bread");
    let restored = parts.find_many(Some(&of_bread), &reads).await.expect("find Bread's after the restore");
    assert_eq!(headings(&restored.rows), vec!["Bake".to_owned(), "Knead".to_owned(), "Mix".to_owned()]);
    assert_eq!(parts.get_one(&part.id).await.expect("get Bread's part after the restore"), part);
    assert_eq!(parts.find_many(None, &reads).await.expect("find every released part").total, Some(4));
}
`
