//! What the Postgres tests share: the database they run against, a schema of
//! their own that holds the fixture's DDL, and the fixture's descriptor; the
//! test hosts of the fixed layout's synchronous seam ([`hosts`]); and the
//! cases every adapter is held to: the writes of released rows
//! ([`released_cases`]) and the reads an entity's history is built from
//! ([`check_entity_reads`]).

#![allow(dead_code)]

pub mod hosts;

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use serde_json::json;
use superschematic_versiongraph_engine::postgres::{self, TokioPostgres};
use superschematic_versiongraph_engine::storage::{
    NewCommit, NewRef, Patch, Ref, ReleasedWrite, Storage, Tx,
};
use superschematic_versiongraph_engine::Error;
use tokio_postgres::{Client, Config, NoTls};

/// Names the Postgres the tests run against.
pub const DATABASE_VARIABLE: &str = "SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL";

/// The schema epoch the fixture's Recipe graph declares.
pub const FIXTURE_SCHEMA_EPOCH: i64 = 1;

/// The snapshot interval the fixture's Recipe graph declares.
pub const FIXTURE_SNAPSHOT_EVERY: usize = 3;

/// The actor of a step that names none: "Cook", a UUID in its canonical
/// form.
pub const DEFAULT_ACTOR: &str = "Cook";

/// `runtime/versiongraph/testdata`.
pub fn testdata() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../testdata")
}

/// The fixture's descriptor.
pub fn descriptor() -> String {
    std::fs::read_to_string(testdata().join("fixture/recipe.json"))
        .expect("read the fixture's descriptor")
}

/// The database the tests run against, or `None`, having said why, when
/// the variable is unset.
pub fn database(test: &str) -> Option<String> {
    match std::env::var(DATABASE_VARIABLE) {
        Ok(dsn) if !dsn.is_empty() => Some(dsn),
        _ => {
            eprintln!("{test}: skipped; set {DATABASE_VARIABLE} to run it against Postgres");
            None
        }
    }
}

/// Connects to `dsn` with `search_path` set to `schema` (and public), and
/// drives the connection on a task of its own.
pub async fn connect(dsn: &str, schema: Option<&str>) -> Client {
    let mut config: Config = dsn.parse().expect("parse the database URL");
    if let Some(schema) = schema {
        config.options(format!("-c search_path={schema},public"));
    }
    let (client, connection) = config.connect(NoTls).await.expect("connect to Postgres");
    tokio::spawn(async move {
        let _ = connection.await;
    });
    client
}

/// A schema of its own that holds the fixture's DDL, dropped with the value.
pub struct Schema {
    pub dsn: String,
    pub name: String,
    /// The server's URL and the name of the database the schema is in, when
    /// the database is the schema's own: the value drops the database.
    database: Option<(String, String)>,
}

/// A name, after `prefix`, that no other schema or database of the tests
/// has.
fn unique_name(prefix: &str) -> String {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("clock")
        .as_nanos();
    // Tests of one process run side by side, and a clock may tick in
    // microseconds, so a counter tells their names apart.
    static NEXT: AtomicU64 = AtomicU64::new(0);
    let n = NEXT.fetch_add(1, Ordering::Relaxed);
    format!("{prefix}_{nanos}_{}_{n}", std::process::id())
}

/// The URL `dsn`, a postgres:// URL, with the database `name` in place of
/// its own.
fn database_url(dsn: &str, name: &str) -> String {
    let (scheme, rest) = dsn
        .split_once("://")
        .expect("the database variable is a postgres:// URL");
    let authority = rest.find(['/', '?']).unwrap_or(rest.len());
    let query = rest.find('?').map_or("", |i| &rest[i..]);
    format!("{scheme}://{}/{name}{query}", &rest[..authority])
}

impl Schema {
    /// Creates a schema named after `prefix` and applies the fixture's DDL
    /// to it.
    pub async fn create(dsn: &str, prefix: &str) -> Schema {
        let admin = connect(dsn, None).await;
        // The fixture's DDL creates pgcrypto if it is missing. An extension's
        // name is unique in the database, so create it once in public, where
        // every schema's search path finds it, before tests running side by
        // side each try to create it in their own schema.
        if let Err(error) = admin
            .batch_execute("CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public")
            .await
        {
            let unique = error.code().is_some_and(|c| c.code() == "23505");
            assert!(unique, "create pgcrypto: {error}");
        }
        let name = unique_name(prefix);
        admin
            .batch_execute(&format!("CREATE SCHEMA {name}"))
            .await
            .expect("create the schema");
        let create_sql = std::fs::read_to_string(testdata().join("fixture/create.sql"))
            .expect("read the fixture's DDL");
        let client = connect(dsn, Some(&name)).await;
        client
            .batch_execute(&create_sql)
            .await
            .expect("apply the fixture's DDL");
        Schema {
            dsn: dsn.to_owned(),
            name,
            database: None,
        }
    }

    /// Creates a database of its own on the server `dsn` names, and in it a
    /// schema as [`Schema::create`] does. The value drops the database.
    ///
    /// The graph's sweep lock is an advisory lock, and Postgres keys an
    /// advisory lock to the database, not to a schema. The tests of one
    /// binary run side by side, so in a database they share, one test's
    /// held lock makes another's sweep skip, and one test's sweep makes
    /// another's take of the lock fail. A test that holds or takes the lock
    /// while another test of its binary does runs in a database of its own.
    pub async fn create_in_own_database(dsn: &str, prefix: &str) -> Schema {
        let database = unique_name(prefix);
        connect(dsn, None)
            .await
            .batch_execute(&format!("CREATE DATABASE {database}"))
            .await
            .expect("create the database");
        let mut schema = Schema::create(&database_url(dsn, &database), prefix).await;
        schema.database = Some((dsn.to_owned(), database));
        schema
    }

    /// A new connection whose search path is the schema.
    pub async fn connect(&self) -> Client {
        connect(&self.dsn, Some(&self.name)).await
    }

    /// The fixture's graph over a new connection to the schema.
    pub async fn storage(
        &self,
        adapter: &Arc<postgres::Adapter>,
    ) -> Arc<postgres::PostgresStorage<TokioPostgres>> {
        Arc::new(adapter.storage(TokioPostgres::new(self.connect().await)))
    }
}

/// Drops the schema and everything in it, or the schema's own database,
/// when the test ends and when it panics. It runs on a thread of its own,
/// since a destructor cannot await, and gives up on a lock it waits on past
/// a few seconds rather than hang. A database is dropped with the
/// connections to it still open.
impl Drop for Schema {
    fn drop(&mut self) {
        let (dsn, statement) = match &self.database {
            Some((server, database)) => (
                server.clone(),
                format!("DROP DATABASE IF EXISTS {database} WITH (FORCE)"),
            ),
            None => (
                self.dsn.clone(),
                format!(
                    "SET lock_timeout = '5s'; DROP SCHEMA IF EXISTS {} CASCADE",
                    self.name
                ),
            ),
        };
        let dropped = std::thread::spawn(move || {
            let runtime = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .expect("a runtime");
            runtime.block_on(async {
                let admin = connect(&dsn, None).await;
                let _ = admin.batch_execute(&statement).await;
            });
        })
        .join();
        if dropped.is_err() {
            eprintln!("could not drop schema {}", self.name);
        }
    }
}

/// The fixture's adapter.
pub fn adapter() -> Arc<postgres::Adapter> {
    Arc::new(
        postgres::Adapter::new(&descriptor(), postgres::Options::default())
            .expect("the fixture's adapter"),
    )
}

/// The hyphenated text Postgres reads of a UUID in its canonical form.
pub fn hyphenated(id: &str) -> String {
    superschematic_versiongraph_engine::canonical::uuid_hyphenated(id).expect("a UUID")
}

/// One row of a kind's released rows, as a test reads it back from either
/// adapter: ids in their canonical form.
#[derive(Debug, Clone, PartialEq, Eq, PartialOrd, Ord)]
pub struct ReleasedPart {
    pub root: String,
    pub key: String,
    pub heading: String,
    pub rank: String,
    pub parent: Option<String>,
    pub commit: String,
    pub sequence: i64,
}

/// One `write_released` an adapter is held to (D139): the write, what it is
/// refused with ("" when it is not), and every released part after it,
/// sorted.
pub struct ReleasedCase {
    pub what: &'static str,
    pub kind: &'static str,
    pub write: ReleasedWrite,
    pub refused: &'static str,
    pub rows: Vec<ReleasedPart>,
}

/// Two commits of the root Bread, tagged 1 and 2, and one of Soup, each on
/// a primary line of its root, for released rows to name. On Postgres the
/// roots' rows are there first.
pub async fn released_commits(storage: &dyn Storage) -> Result<([String; 2], String), Error> {
    let mut tx = storage.begin().await?;
    let mut ids = Vec::new();
    for (root, sequences) in [("Bread", &[1, 2][..]), ("Soup", &[1][..])] {
        let line = tx
            .create_ref(NewRef {
                root: root.to_owned(),
                name: "main".to_owned(),
                actor: DEFAULT_ACTOR.to_owned(),
                ..NewRef::default()
            })
            .await?;
        for sequence in sequences {
            let commit = tx
                .insert_commit(NewCommit {
                    root: root.to_owned(),
                    ref_id: line.id.clone(),
                    content_hash: "h".to_owned(),
                    sequence: Some(*sequence),
                    actor: DEFAULT_ACTOR.to_owned(),
                    ..NewCommit::default()
                })
                .await?;
            ids.push(commit.id);
        }
    }
    tx.commit().await?;
    let soup = ids.pop().expect("Soup's commit");
    let second = ids.pop().expect("Bread's second commit");
    let first = ids.pop().expect("Bread's first commit");
    Ok(([first, second], soup))
}

/// Runs one `write_released` in a transaction of its own, which it commits,
/// or rolls back when the write is refused.
pub async fn write_released(
    storage: &dyn Storage,
    kind: &str,
    write: ReleasedWrite,
) -> Result<(), Error> {
    let mut tx = storage.begin().await?;
    match tx.write_released(kind, write).await {
        Ok(()) => tx.commit().await,
        Err(error) => {
            // The write's error is the one to report.
            let _ = tx.rollback().await;
            Err(error)
        }
    }
}

/// The writes of released parts every adapter takes the same way, over the
/// commits [`released_commits`] wrote, in order: each case starts from the
/// rows the one before it left.
pub fn released_cases(bread: &[String; 2], soup: &str) -> Vec<ReleasedCase> {
    let write = |root: &str, commit: &str, sequence: i64, replace: bool| ReleasedWrite {
        root: root.to_owned(),
        commit: commit.to_owned(),
        sequence,
        replace,
        ..ReleasedWrite::default()
    };
    let part = |root: &str, key: &str, heading: &str, rank: &str, commit: &str, sequence: i64| {
        ReleasedPart {
            root: root.to_owned(),
            key: key.to_owned(),
            heading: heading.to_owned(),
            rank: rank.to_owned(),
            parent: None,
            commit: commit.to_owned(),
            sequence,
        }
    };
    let (first, second) = (bread[0].as_str(), bread[1].as_str());
    let oven = ReleasedPart {
        parent: Some("Mix".to_owned()),
        ..part("Bread", "Oven", "Heat the oven", "V", first, 1)
    };
    let stock = part("Soup", "Mix", "Make the stock", "V", soup, 1);
    let after_the_first = vec![
        part("Bread", "Bake", "Bake", "k", first, 1),
        part("Bread", "Mix", "Mix", "F", first, 1),
        oven.clone(),
    ];
    let after_the_second = vec![
        part("Bread", "Mix", "Mix well", "F", second, 2),
        oven,
        part("Bread", "Rest", "Rest", "c", second, 2),
        stock.clone(),
    ];
    vec![
        ReleasedCase {
            what: "a root's first write: every row, a full row's role columns left out, an id \
                   hyphenated or canonical, a column the row lacks null",
            kind: "part",
            write: ReleasedWrite {
                upsert: vec![
                    json!({"entity_key": "Mix", "parent_key": null, "rank": "F", "heading": "Mix",
                        "id": "Row", "ref_id": "Ref", "recipe_id": "Soup", "deleted_on_ref": true, "_version": 7}),
                    json!({"entity_key": hyphenated("Oven"), "parent_key": hyphenated("Mix"), "rank": "V",
                        "heading": "Heat the oven"}),
                    json!({"entity_key": "Bake", "rank": "k", "heading": "Bake"}),
                ],
                ..write("Bread", first, 1, true)
            },
            refused: "",
            rows: after_the_first.clone(),
        },
        ReleasedCase {
            what: "another root's first write leaves the first root's rows",
            kind: "part",
            write: ReleasedWrite {
                upsert: vec![
                    json!({"entity_key": "Mix", "parent_key": null, "rank": "V", "heading": "Make the stock"}),
                ],
                ..write("Soup", soup, 1, true)
            },
            refused: "",
            rows: [after_the_first, vec![stock.clone()]].concat(),
        },
        ReleasedCase {
            what: "a later write deletes, changes and adds, each with its commit and sequence, \
                   and leaves the rest as they were written",
            kind: "part",
            write: ReleasedWrite {
                upsert: vec![
                    json!({"entity_key": "Mix", "parent_key": null, "rank": "F", "heading": "Mix well"}),
                    json!({"entity_key": "Rest", "parent_key": null, "rank": "c", "heading": "Rest"}),
                ],
                delete: vec!["Bake".to_owned(), "Gone".to_owned()],
                ..write("Bread", second, 2, false)
            },
            refused: "",
            rows: after_the_second.clone(),
        },
        ReleasedCase {
            what: "a write of nothing changes nothing",
            kind: "part",
            write: write("Bread", second, 2, false),
            refused: "",
            rows: after_the_second.clone(),
        },
        ReleasedCase {
            what: "a kind that keeps no released table is refused",
            kind: "step",
            write: ReleasedWrite {
                upsert: vec![
                    json!({"entity_key": "Mix", "position": 1, "instruction": "Mix", "timings": {}}),
                ],
                ..write("Bread", second, 2, true)
            },
            refused: "keeps no released table",
            rows: after_the_second.clone(),
        },
        ReleasedCase {
            what: "an unknown kind is refused",
            kind: "garnish",
            write: write("Bread", second, 2, true),
            refused: "unknown kind",
            rows: after_the_second.clone(),
        },
        ReleasedCase {
            what: "a row that is not an object is refused, and its transaction's replace undone",
            kind: "part",
            write: ReleasedWrite {
                upsert: vec![json!("Mix")],
                ..write("Bread", second, 2, true)
            },
            refused: "is a JSON object",
            rows: after_the_second,
        },
        ReleasedCase {
            what: "a replace rewrites its root's rows and no other's",
            kind: "part",
            write: ReleasedWrite {
                upsert: vec![
                    json!({"entity_key": "Mix", "parent_key": null, "rank": "F", "heading": "Mix"}),
                ],
                ..write("Bread", first, 1, true)
            },
            refused: "",
            rows: vec![part("Bread", "Mix", "Mix", "F", first, 1), stock],
        },
        ReleasedCase {
            what: "a replace with no rows clears its root",
            kind: "part",
            write: write("Soup", soup, 1, true),
            refused: "",
            rows: vec![part("Bread", "Mix", "Mix", "F", first, 1)],
        },
    ]
}

/// Holds an adapter to one case: the write's result, and the released parts
/// read back after it, in any order.
pub fn check_released(case: &ReleasedCase, result: Result<(), Error>, mut rows: Vec<ReleasedPart>) {
    let what = case.what;
    match (case.refused, result) {
        ("", Ok(())) => {}
        ("", Err(error)) => panic!("{what}: {error}"),
        (refused, Ok(())) => panic!("{what}: written, want it refused with {refused:?}"),
        (refused, Err(error)) => assert!(
            error.to_string().contains(refused),
            "{what}: {error}, want {refused:?}"
        ),
    }
    rows.sort();
    assert_eq!(rows, case.rows, "{what}");
}

/// Writes a ref of `root` in a transaction of its own, so the refs a test
/// writes one after another were created at different times: a primary
/// line when `parent` is `None`.
async fn a_ref(storage: &dyn Storage, root: &str, parent: Option<&Ref>, name: &str) -> Ref {
    let mut tx = storage.begin().await.expect("begin");
    let written = tx
        .create_ref(NewRef {
            root: root.to_owned(),
            parent: parent.map(|parent| parent.id.clone()),
            name: name.to_owned(),
            actor: DEFAULT_ACTOR.to_owned(),
            ..NewRef::default()
        })
        .await
        .unwrap_or_else(|e| panic!("create the ref {name} of {root}: {e}"));
    tx.commit().await.expect("commit");
    written
}

/// What [`check_entity_reads`] writes: the live primary lines of Bread in
/// the order they were created, Soup's, and the commits, two of Bread's
/// first line, one of a change set of it and one of Soup's line.
struct EntityReads {
    lines: [String; 3],
    soup: String,
    first: String,
    second: String,
    drafted: String,
    stock: String,
}

/// Holds an adapter to the two reads an entity's history is built from
/// (D138), over the roots Bread and Soup, whose rows are there first on
/// Postgres.
///
/// `primary_refs` gives a root's refs with no parent that are not
/// discarded, in the order they were created: not a change set, not a
/// discarded line, not another root's. `entity_patches` gives the patches
/// of one entity among the commits it is given, whatever ref wrote them: not
/// a patch of another commit, of another entity, or of another kind's
/// entity with the same key. No commits read as no patches, and an unknown
/// kind is refused.
pub async fn check_entity_reads(storage: &dyn Storage) {
    let written = entity_reads(storage).await;
    let mut tx = storage.begin().await.expect("begin");
    check_primary_refs(&mut *tx, &written).await;
    check_entity_patches(&mut *tx, &written).await;
    tx.commit().await.expect("commit");
}

/// Writes the lines, the commits and the patches the reads are held to: a
/// discarded primary line and a change set beside Bread's live lines, and
/// patches of two kinds' entities with one key.
async fn entity_reads(storage: &dyn Storage) -> EntityReads {
    let main = a_ref(storage, "Bread", None, "main").await;
    let old = a_ref(storage, "Bread", None, "old").await;
    let later = a_ref(storage, "Bread", None, "later").await;
    let last = a_ref(storage, "Bread", None, "last").await;
    let draft = a_ref(storage, "Bread", Some(&main), "draft").await;
    let soup = a_ref(storage, "Soup", None, "main").await;

    let mut tx = storage.begin().await.expect("begin");
    let commit = |line: &Ref, parent: Option<&str>| NewCommit {
        root: line.root.clone(),
        ref_id: line.id.clone(),
        parent: parent.map(str::to_owned),
        content_hash: "h".to_owned(),
        actor: DEFAULT_ACTOR.to_owned(),
        ..NewCommit::default()
    };
    let first = tx
        .insert_commit(commit(&main, None))
        .await
        .expect("the line's first commit")
        .id;
    let second = tx
        .insert_commit(commit(&main, Some(&first)))
        .await
        .expect("the line's second commit")
        .id;
    let drafted = tx
        .insert_commit(commit(&draft, Some(&second)))
        .await
        .expect("the change set's commit")
        .id;
    let stock = tx
        .insert_commit(commit(&soup, None))
        .await
        .expect("another root's commit")
        .id;
    let patch = |kind: &str, key: &str, operation: &str| Patch {
        kind: kind.to_owned(),
        entity_key: key.to_owned(),
        entity_id: format!("Row{kind}{key}"),
        entity_version: 1,
        operation: operation.to_owned(),
        ..Patch::default()
    };
    for (commit, patches) in [
        (
            &first,
            vec![
                patch("part", "Mix", "ADD"),
                patch("part", "Bake", "ADD"),
                patch("step", "Mix", "ADD"),
            ],
        ),
        (&second, vec![patch("part", "Mix", "UPDATE")]),
        (&drafted, vec![patch("part", "Mix", "DELETE")]),
        (&stock, vec![patch("part", "Mix", "ADD")]),
    ] {
        tx.insert_patches(commit, &patches)
            .await
            .expect("write the patches");
    }
    tx.discard_ref(&old.id, old.version, DEFAULT_ACTOR)
        .await
        .expect("discard a primary line");
    tx.commit().await.expect("commit");
    EntityReads {
        lines: [main.id, later.id, last.id],
        soup: soup.id,
        first,
        second,
        drafted,
        stock,
    }
}

async fn check_primary_refs(tx: &mut dyn Tx, written: &EntityReads) {
    for (what, root, want) in [
        (
            "a root's primary lines, in the order they were created, less a discarded one \
             and a change set",
            "Bread",
            written.lines.iter().collect::<Vec<_>>(),
        ),
        ("another root's primary line", "Soup", vec![&written.soup]),
        ("a root with no ref", "Stew", vec![]),
    ] {
        let lines = tx.primary_refs(root).await.expect("read the primary lines");
        let got: Vec<&String> = lines.iter().map(|line| &line.id).collect();
        assert_eq!(got, want, "{what}");
        assert!(
            lines
                .iter()
                .all(|line| line.root == root && line.parent.is_none() && !line.discarded),
            "{what}: {lines:?}"
        );
    }
}

async fn check_entity_patches(tx: &mut dyn Tx, written: &EntityReads) {
    let EntityReads {
        first,
        second,
        drafted,
        stock,
        ..
    } = written;
    let every = vec![
        first.clone(),
        second.clone(),
        drafted.clone(),
        stock.clone(),
    ];
    let line = vec![first.clone(), second.clone()];
    for (what, commits, kind, key, want) in [
        (
            "an entity's patches among every commit, whatever ref or root wrote them",
            &every,
            "part",
            "Mix",
            vec![
                (first, "ADD"),
                (second, "UPDATE"),
                (drafted, "DELETE"),
                (stock, "ADD"),
            ],
        ),
        (
            "an entity's patches among a line's commits alone",
            &line,
            "part",
            "Mix",
            vec![(first, "ADD"), (second, "UPDATE")],
        ),
        (
            "another kind's entity with the same key",
            &line,
            "step",
            "Mix",
            vec![(first, "ADD")],
        ),
        (
            "another entity of the kind",
            &line,
            "part",
            "Bake",
            vec![(first, "ADD")],
        ),
        ("an entity no commit changed", &line, "part", "Rest", vec![]),
        ("no commits", &Vec::new(), "part", "Mix", vec![]),
    ] {
        let patches = tx
            .entity_patches(commits, kind, key)
            .await
            .unwrap_or_else(|e| panic!("{what}: {e}"));
        assert!(
            patches.iter().all(|p| p.kind == kind
                && p.entity_key == key
                && p.entity_id == format!("Row{kind}{key}")
                && p.entity_version == 1),
            "{what}: {patches:?}"
        );
        // In no particular order: by the order the commits were given.
        let mut got: Vec<(&String, &str)> = patches
            .iter()
            .map(|p| (&p.commit, p.operation.as_str()))
            .collect();
        got.sort_by_key(|(commit, _)| commits.iter().position(|id| id == *commit));
        assert_eq!(got, want, "{what}");
    }
    match tx.entity_patches(&line, "garnish", "Mix").await {
        Err(error) => assert!(error.to_string().contains("unknown kind"), "{error}"),
        Ok(patches) => panic!("an unknown kind's patches read as {patches:?}"),
    }
}
