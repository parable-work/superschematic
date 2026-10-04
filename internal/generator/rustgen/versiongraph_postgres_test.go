package rustgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestVersionGraphFacadeOnPostgres generates fixture-version-graph-db's
// Rust types crate, whose src/versiongraph_recipe.rs is the Recipe graph's
// typed facade over the version graph's Rust engine, and runs two tests in
// it against the Postgres SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL
// names. The first saves a Tasting, whose columns hold a value of every
// class a descriptor names, commits it and reads it back from the save and
// from the commit: each field comes back as the typed value of its
// canonical form. The second merges, resolves a typed conflict, diffs,
// releases, rebases and sweeps through the facade.
func TestVersionGraphFacadeOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the compiled facade in -short mode")
	}
	dsn := os.Getenv("SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL to run the version graph facade against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the compiled facade")
	}
	const service = "fixture-version-graph-db"
	schema, err := loader.LoadService(filepath.Join(fixturesDir, service))
	if err != nil {
		t.Fatalf("load %s: %v", service, err)
	}
	output, err := Generate(schema, Options{SchemaName: service, Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: service})
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
	outDir := filepath.Join(root, output.CrateName)
	paths := testpaths.Local(t)
	if err := SetLocalPaths(output, paths, outDir); err != nil {
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
serde_json = "1.0"
tokio = { version = "1", features = ["macros", "rt-multi-thread"] }
tokio-postgres = "0.7.18"
`)...)
	if err := os.WriteFile(manifest, cargoToml, 0o644); err != nil {
		t.Fatal(err)
	}
	testsDir := filepath.Join(outDir, "tests")
	if err := os.Mkdir(testsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	crate := strings.ReplaceAll(output.CrateName, "-", "_")
	source := strings.ReplaceAll(versionGraphFacadeTest, "CRATE", crate)
	if err := os.WriteFile(filepath.Join(testsDir, "facade.rs"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(cargo, "test", "--test", "facade")
	cmd.Dir = outDir
	cmd.Env = append(os.Environ(),
		"CARGO_TARGET_DIR="+filepath.Join(root, "target"),
		"FACADE_DDL="+filepath.Join(root, "sql", "create.sql"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the generated facade's tests: %v\n%s", err, out)
	}
	for _, test := range []string{"facade_keeps_every_class", "facade_merges_releases_rebases_and_sweeps"} {
		if !strings.Contains(string(out), "test "+test+" ... ok") {
			t.Fatalf("the generated crate did not run %s:\n%s", test, out)
		}
	}
	t.Logf("the generated crate ran facade_keeps_every_class and facade_merges_releases_rebases_and_sweeps against Postgres")
}

// versionGraphFacadeTest is the generated crate's tests/facade.rs; CRATE is
// the crate's name as Rust spells it.
const versionGraphFacadeTest = `use std::str::FromStr;
use std::time::Duration;

use serde_json::{json, Value};
use superschematic_versiongraph_engine::postgres::TokioPostgres;
use superschematic_versiongraph_engine::{CommitOptions, SweepOptions, Take};
use CRATE::*;

fn uuid(text: &str) -> IdentityUUID {
    IdentityUUID::from_str(text).expect("a UUID")
}

/// Drops a test's schema when the test ends, and when it panics.
struct DropSchema {
    dsn: String,
    name: String,
}

impl Drop for DropSchema {
    fn drop(&mut self) {
        let (dsn, name) = (self.dsn.clone(), self.name.clone());
        let _ = std::thread::spawn(move || {
            let runtime = tokio::runtime::Builder::new_current_thread().enable_all().build().expect("a runtime");
            runtime.block_on(async {
                let (client, connection) = tokio_postgres::connect(&dsn, tokio_postgres::NoTls).await.expect("connect");
                tokio::spawn(connection);
                let _ = client.batch_execute(&format!("SET lock_timeout = '5s'; DROP SCHEMA IF EXISTS {name} CASCADE")).await;
            });
        })
        .join();
    }
}

/// A schema of its own holding the fixture's DDL and a Bread recipe, and the
/// Recipe graph over a connection to it.
async fn graph(name: &str) -> (RecipeGraph, tokio_postgres::Client, IdentityUUID, DropSchema) {
    let dsn = std::env::var("SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL").expect("the database");
    let ddl = std::fs::read_to_string(std::env::var("FACADE_DDL").expect("the DDL")).expect("read the DDL");
    let connect = |schema: Option<String>| {
        let dsn = dsn.clone();
        async move {
            let mut config: tokio_postgres::Config = dsn.parse().expect("the database URL");
            if let Some(schema) = schema {
                config.options(format!("-c search_path={schema},public"));
            }
            let (client, connection) = config.connect(tokio_postgres::NoTls).await.expect("connect");
            tokio::spawn(connection);
            client
        }
    };
    let admin = connect(None).await;
    let _ = admin.batch_execute("CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public").await;
    let nanos = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).expect("clock").as_nanos();
    let schema = format!("vg_rust_facade_{name}_{nanos}_{}", std::process::id());
    admin.batch_execute(&format!("DROP SCHEMA IF EXISTS {schema} CASCADE; CREATE SCHEMA {schema}")).await.expect("create the schema");
    let sql = connect(Some(schema.clone())).await;
    sql.batch_execute(&ddl).await.expect("apply the DDL");
    let root = IdentityUUID::new_v4();
    sql.execute(
        "INSERT INTO recipe (id, title, created_by) VALUES ($1::text::uuid, 'Bread', $2::text::uuid)",
        &[&root.to_uuid().to_string(), &cook().to_uuid().to_string()],
    )
    .await
    .expect("insert the recipe");
    let graph = RecipeGraph::postgres(TokioPostgres::new(connect(Some(schema.clone())).await)).expect("the graph");
    (graph, sql, root, DropSchema { dsn, name: schema })
}

fn cook() -> IdentityUUID {
    uuid("5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90")
}

fn janitor() -> IdentityUUID {
    uuid("3c9a7e21-6b4d-4f8a-9e2c-5d1b7a3f6e08")
}

fn fields(tasting: &Tasting) -> serde_json::Map<String, Value> {
    match serde_json::to_value(tasting).expect("serialize") {
        Value::Object(fields) => fields,
        other => panic!("a tasting serializes as {other}"),
    }
}

#[tokio::test]
async fn facade_keeps_every_class() {
    let (graph, _sql, root, _schema) = graph("classes").await;
    let main = graph.create_primary(&cook(), &root, "main").await.expect("create_primary");
    let draft = graph.branch(&cook(), &uuid(&main.id), "tastings").await.expect("branch");
    let input = Tasting {
        id: None,
        recipe: None,
        taster: uuid("0e7d4b1a-3c2f-4a6e-8b9d-1f2e3d4c5b6a"),
        salty: true,
        score: 4.5,
        servings: 9007199254740993,
        tasted_on: "2026-09-01".to_owned(),
        tasted_at: serde_json::from_value(json!("2026-09-01T12:30:00.25+02:00")).expect("a date-time"),
        served_at: "18:30".to_owned(),
        rested: "1.5ms".to_owned(),
        verdict: Verdict::Tweak,
        remarks: json!({"crumb": "open", "crust": [1, 2.5]}),
        tags: vec!["sour".to_owned(), "a \"quoted\" tag".to_owned()],
        helpers: vec![cook()],
        bites: vec![vec![1, 2], vec![3]],
        entity_key: None,
        r#ref: None,
        deleted_on_ref: false,
        version: 0,
    };
    // Each field comes back as the typed value of its canonical form: an
    // instant in UTC, a time of day as HH:MM:SS, a duration in the scalar
    // core's form; the rest as it was.
    let mut want = input.clone();
    want.tasted_at = serde_json::from_value(json!("2026-09-01T10:30:00.25Z")).expect("a date-time");
    want.served_at = "18:30:00".to_owned();
    want.rested = "1500us".to_owned();
    want.version = 1;
    let want = fields(&want);
    let edits = RecipeEdits {
        tasting: RecipeKindEdits { upsert: vec![input.clone()], ..RecipeKindEdits::default() },
        ..RecipeEdits::default()
    };
    let saved = graph.save(&cook(), &uuid(&draft.id), draft.version, &edits).await.expect("save a tasting");
    let committed = graph
        .commit(&cook(), &uuid(&draft.id), saved.ref_.version, &CommitOptions::default())
        .await
        .expect("commit the tasting");
    let commit = committed.commit.expect("a commit");
    let tree = graph.materialize(&uuid(&commit.id)).await.expect("materialize");
    assert_eq!(tree.content_hash, commit.content_hash, "the tree hashes as its commit");
    for (what, got) in [("saved", &saved.saved.tasting), ("materialized", &tree.tasting)] {
        assert_eq!(got.len(), 1, "{what}: {got:?}");
        let got = fields(&got[0]);
        for field in [
            "taster", "salty", "score", "servings", "tastedOn", "tastedAt", "servedAt", "rested",
            "verdict", "remarks", "tags", "helpers", "bites", "deletedOnRef", "_version",
        ] {
            assert_eq!(got.get(field), want.get(field), "{what} tasting {field}");
        }
        assert!(got.get("entityKey").is_some_and(Value::is_string), "{what} tasting has no generated entity key: {got:?}");
        assert!(got.get("id").is_some_and(Value::is_string), "{what} tasting has no row id: {got:?}");
    }
}

fn step(position: i64, instruction: &str, entity_key: Option<IdentityUUID>) -> Step {
    Step {
        id: None,
        recipe: None,
        position,
        instruction: instruction.to_owned(),
        timings: json!({}),
        scratch: None,
        created_at: serde_json::from_value(json!("2026-01-01T00:00:00Z")).expect("a date-time"),
        created_by: cook(),
        updated_at: serde_json::from_value(json!("2026-01-01T00:00:00Z")).expect("a date-time"),
        updated_by: cook(),
        entity_key,
        r#ref: None,
        deleted_on_ref: false,
        version: 0,
    }
}

fn steps(steps: Vec<Step>) -> RecipeEdits {
    RecipeEdits {
        step: RecipeKindEdits { upsert: steps, ..RecipeKindEdits::default() },
        ..RecipeEdits::default()
    }
}

/// Saves edits on a new change set of main, commits it and merges it into
/// main, returning the merge.
async fn land(graph: &RecipeGraph, main: &IdentityUUID, name: &str, edits: RecipeEdits, options: CommitOptions) -> RecipeMergeResult {
    let draft = graph.branch(&cook(), main, name).await.expect("branch");
    let saved = graph.save(&cook(), &uuid(&draft.id), draft.version, &edits).await.expect("save");
    graph.commit(&cook(), &uuid(&draft.id), saved.ref_.version, &CommitOptions::default()).await.expect("commit");
    let version = current_version(graph, main).await;
    graph.merge(&cook(), &uuid(&draft.id), main, version, &[], &options).await.expect("merge")
}

async fn current_version(graph: &RecipeGraph, id: &IdentityUUID) -> i64 {
    let mut tx = graph.engine().storage().begin().await.expect("begin");
    let r = tx.read_ref(&id.to_string()).await.expect("read the ref");
    tx.rollback().await.expect("roll back");
    r.version
}

#[tokio::test]
async fn facade_merges_releases_rebases_and_sweeps() {
    let (graph, sql, root, _schema) = graph("flow").await;
    let main = graph.create_primary(&cook(), &root, "main").await.expect("create_primary");
    let main_id = uuid(&main.id);
    let tagged = CommitOptions { message: "v1".to_owned(), tag: true };
    let one = land(&graph, &main_id, "one", steps(vec![step(1, "Mix", None)]), tagged).await;
    let v1 = one.commit.expect("a tagged merge commit");
    assert_eq!((v1.sequence, v1.message.as_str()), (Some(1), "v1"));
    let mix = graph.compose(&main_id).await.expect("compose").step[0].entity_key.expect("a key");

    // Two change sets edit one step: the second merge conflicts, as a typed
    // conflict, until a typed resolution settles it.
    let a = graph.branch(&cook(), &main_id, "a").await.expect("branch a");
    let b = graph.branch(&cook(), &main_id, "b").await.expect("branch b");
    for (r, instruction) in [(&a, "Mix well"), (&b, "Mix fast")] {
        let saved = graph.save(&cook(), &uuid(&r.id), r.version, &steps(vec![step(1, instruction, Some(mix))])).await.expect("save");
        graph.commit(&cook(), &uuid(&r.id), saved.ref_.version, &CommitOptions::default()).await.expect("commit");
    }
    graph.merge(&cook(), &uuid(&a.id), &main_id, current_version(&graph, &main_id).await, &[], &CommitOptions::default()).await.expect("merge a");
    let conflicted = graph
        .merge(&cook(), &uuid(&b.id), &main_id, current_version(&graph, &main_id).await, &[], &CommitOptions::default())
        .await
        .expect("merge b");
    assert_eq!(conflicted.conflicts.len(), 1, "{conflicted:?}");
    let conflict = &conflicted.conflicts[0];
    assert_eq!((conflict.kind, conflict.entity_key, conflict.path.as_str()), (RecipeEntityKind::Step, mix, "/instruction"));
    assert_eq!((conflict.ours.clone(), conflict.theirs.clone()), (Some(json!("Mix well")), Some(json!("Mix fast"))));
    let resolution = RecipeResolution { kind: RecipeEntityKind::Step, entity_key: mix, path: "/instruction".to_owned(), take: Some(Take::Theirs), value: None };
    let settled = graph
        .merge(&cook(), &uuid(&b.id), &main_id, current_version(&graph, &main_id).await, &[resolution], &CommitOptions { message: "v2".to_owned(), tag: true })
        .await
        .expect("merge b with a resolution");
    let v2 = settled.commit.expect("a merge commit");
    let changes = graph.diff(&uuid(&v1.id), &uuid(&v2.id)).await.expect("diff");
    assert_eq!(changes.len(), 1, "{changes:?}");
    assert_eq!((changes[0].kind, changes[0].entity_key, changes[0].operation), (RecipeEntityKind::Step, mix, RecipePatchOperation::Update));

    // The release pointer names a tagged commit; a rollback moves it back.
    let released = graph.release(&cook(), &root, &uuid(&v2.id), 0).await.expect("release v2");
    let read = graph.released(&root).await.expect("released");
    assert_eq!((read.release.version, read.tree.step[0].instruction.as_str()), (1, "Mix fast"));
    graph.release(&cook(), &root, &uuid(&v1.id), released.version).await.expect("roll back to v1");
    assert_eq!(graph.released(&root).await.expect("released").tree.content_hash, v1.content_hash);
    let untagged = land(&graph, &main_id, "rest", steps(vec![step(2, "Rest", None)]), CommitOptions::default()).await;
    let refused = graph.release(&cook(), &root, &uuid(&untagged.commit.expect("a commit").id), 2).await;
    assert_eq!(refused.expect_err("an untagged release").code(), Some("not_tagged"));

    // A rebase moves a change set onto its parent's newer head.
    let c = graph.branch(&cook(), &main_id, "c").await.expect("branch c");
    let saved = graph.save(&cook(), &uuid(&c.id), c.version, &steps(vec![step(3, "Bake", None)])).await.expect("save c");
    land(&graph, &main_id, "cool", steps(vec![step(4, "Cool", None)]), CommitOptions::default()).await;
    let rebased = graph.rebase(&cook(), &uuid(&c.id), saved.ref_.version, &[]).await.expect("rebase c");
    assert!(rebased.conflicts.is_empty() && rebased.commit.is_some(), "{rebased:?}");
    let instructions: Vec<String> = graph.compose(&uuid(&c.id)).await.expect("compose c").step.into_iter().map(|s| s.instruction).collect();
    assert_eq!(instructions, ["Mix fast", "Rest", "Bake", "Cool"]);
    let refused = graph.rebase(&cook(), &main_id, current_version(&graph, &main_id).await, &[]).await;
    assert_eq!(refused.expect_err("rebase main").code(), Some("no_parent"));

    // A sweep writes as its actor: the rows it collects record it.
    let x = graph.branch(&cook(), &main_id, "dropped").await.expect("branch x");
    let x_saved = graph.save(&cook(), &uuid(&x.id), x.version, &steps(vec![step(9, "Garnish", None)])).await.expect("save x");
    // The adapter writes the audit columns, whatever the typed value holds.
    let placeholder: TemporalDateTime = serde_json::from_value(json!("2026-01-01T00:00:00Z")).expect("a date-time");
    let garnish = &x_saved.saved.step[0];
    assert!(garnish.created_at != placeholder && garnish.updated_at != placeholder, "{garnish:?}");
    graph.discard(&cook(), &uuid(&x.id), x_saved.ref_.version).await.expect("discard x");
    sql.execute("UPDATE recipe_ref SET deleted_at = now() - interval '8 days' WHERE id = $1::text::uuid", &[&uuid(&x.id).to_uuid().to_string()])
        .await
        .expect("age the discard");
    let report = graph
        .sweep(&SweepOptions { actor: janitor().to_string(), discard_grace: Duration::ZERO, ..SweepOptions::default() })
        .await
        .expect("sweep");
    assert_eq!((report.skipped, report.collected_refs, report.collected_rows.get("step").copied()), (false, 1, Some(1)), "{report:?}");
    let row = sql
        .query_one("SELECT operation, data->>'updated_by' FROM step_history WHERE id = $1::text::uuid ORDER BY _version DESC LIMIT 1",
            &[&x_saved.saved.step[0].id.expect("a row id").to_uuid().to_string()])
        .await
        .expect("read the collected row's history");
    let (operation, actor): (String, String) = (row.get(0), row.get(1));
    assert_eq!((operation.as_str(), actor), ("DELETE", janitor().to_uuid().to_string()));
}
`
