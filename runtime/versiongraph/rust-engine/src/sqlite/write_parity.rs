//! The shared SQLite vectors' script, written through this adapter: the
//! TypeScript tests' `writeDatabase` (typescript/test/sqlite-vectors.ts),
//! with its clock and its seeded ids, dumped as its `dumpDatabase` dumps a
//! database, must give testdata/sqlite/typescript.sql byte for byte. So a
//! file this adapter writes is the file the TypeScript adapter writes, down
//! to the order it draws ids and reads the clock in.
//!
//! It is a test of the crate itself, not of its public interface, since it
//! draws the adapter's ids from a seeded source, which only the crate's own
//! tests can give it ([`Adapter::with_ids`]).

use std::collections::BTreeMap;
use std::sync::atomic::{AtomicI64, Ordering};
use std::sync::{Arc, Mutex};

use rusqlite::types::ValueRef;
use serde_json::Value;

use super::{default_table_name, layout, Adapter, Clock, IdBits, Options, Rusqlite, TABLES};
use crate::{CommitOptions, Edits, Engine, KindEdits, MergeResult};

const COOK: &str = "Cook";
const ANN: &str = "Ann";
const BREAD: &str = "Bread";

/// The time of the script's first transaction: 2026-10-05T09:00:00Z, in
/// microseconds.
const FIRST_TIME: i64 = 1_791_190_800_000_000;

/// How far the clock moves at each read: 1.250005 s.
const CLOCK_STEP: i64 = 1_250_005;

/// The seed of the script's ids.
const ID_SEED: u64 = 0x5eed_d32a;

fn testdata(path: &str) -> String {
    let path = format!("{}/../testdata/{path}", env!("CARGO_MANIFEST_DIR"));
    std::fs::read_to_string(&path).unwrap_or_else(|e| panic!("read {path}: {e}"))
}

/// A splitmix64 generator seeded with `seed`, giving each id's 128 bits as
/// the TypeScript script's `seededUUIDs` does: the first draw the high 64
/// bits, the second the low.
fn seeded_ids(seed: u64) -> IdBits {
    let state = Mutex::new(seed);
    Arc::new(move || {
        let mut state = state.lock().expect("the generator");
        let mut next = || {
            *state = state.wrapping_add(0x9e37_79b9_7f4a_7c15);
            let mut z = *state;
            z = (z ^ (z >> 30)).wrapping_mul(0xbf58_476d_1ce4_e5b9);
            z = (z ^ (z >> 27)).wrapping_mul(0x94d0_49bb_1331_11eb);
            z ^ (z >> 31)
        };
        let (high, low) = (next(), next());
        let mut bytes = [0u8; 16];
        bytes[..8].copy_from_slice(&high.to_be_bytes());
        bytes[8..].copy_from_slice(&low.to_be_bytes());
        Ok(bytes)
    })
}

/// A clock that reads FIRST_TIME, then moves CLOCK_STEP at each read.
fn fixed_clock() -> Clock {
    let now = AtomicI64::new(FIRST_TIME);
    Arc::new(move || now.fetch_add(CLOCK_STEP, Ordering::SeqCst))
}

fn row(json: &str) -> Value {
    serde_json::from_str(json).expect("a row")
}

fn upsert(rows: &[&str]) -> KindEdits {
    KindEdits {
        upsert: rows.iter().map(|r| row(r)).collect(),
        ..KindEdits::default()
    }
}

fn edits(kinds: Vec<(&str, KindEdits)>) -> Edits {
    kinds
        .into_iter()
        .map(|(kind, edits)| (kind.to_owned(), edits))
        .collect::<BTreeMap<_, _>>()
}

fn message(text: &str, tag: bool) -> CommitOptions {
    CommitOptions {
        message: text.to_owned(),
        tag,
    }
}

/// A merge that wrote a commit, with no conflicts.
fn merged(result: MergeResult) -> MergeResult {
    assert!(
        result.conflicts.is_empty(),
        "the script's merges have no conflicts"
    );
    assert!(
        result.commit.is_some(),
        "the script's merges write a commit"
    );
    result
}

/// An engine over a graph's adapter, as the script runs it.
async fn engine(
    client: &Arc<Rusqlite>,
    descriptor: &str,
    graph: &str,
    clock: &Clock,
    ids: &IdBits,
) -> Engine {
    let options = Options {
        graph: graph.to_owned(),
        clock: Some(Arc::clone(clock)),
        ..Options::default()
    };
    let adapter = Arc::new(
        Adapter::new(descriptor, options)
            .expect("the adapter")
            .with_ids(Arc::clone(ids)),
    );
    if graph == "recipe" {
        adapter
            .create_tables(client)
            .await
            .expect("create the layout");
    }
    let storage = Arc::new(adapter.storage(Arc::clone(client)).await.expect("bind"));
    let engine_options = crate::Options {
        schema_epoch: 1,
        snapshot_every: 3,
        ..crate::Options::default()
    };
    Engine::new(descriptor, storage, engine_options).expect("the engine")
}

// The tastings: a value of every class the descriptor names, the first in
// canonical form and the second in the schema runtime's other forms.
const FIRST_TASTING: &str = concat!(
    r#"{"entity_key":"First","taster":"Ann","salty":true,"score":4.5,"servings":9007199254740993,"#,
    r#""tasted_on":"2026-09-01","tasted_at":"2026-09-01T10:00:00.12Z","served_at":"18:30:00","rested":"1h30m0s","#,
    r#""verdict":"again","remarks":{"crust":[1,2.50],"crumb":"open"},"tags":["sour","a \"quoted\" tag","crème brûlée 🍞"],"#,
    r#""helpers":["Bob","Cy"],"bites":[[1,2],[3]]}"#
);
const SECOND_TASTING: &str = concat!(
    r#"{"entity_key":"00000000-0000-0000-0000-000000000002","taster":"00000000-0000-0000-0000-00000000000a","salty":false,"#,
    r#""score":1e21,"servings":-3,"tasted_on":"2026-02-28","tasted_at":"2026-09-01T12:30:00+02:30","served_at":"2:30 pm","#,
    r#""rested":"-1m30.5s","verdict":"never","remarks":null,"tags":[],"helpers":["00000000-0000-0000-0000-00000000003d"],"bites":[]}"#
);

/// The script, as `writeDatabase` writes it: in graph recipe, a primary
/// line; a change set with a row of every kind, committed, merged as a
/// tagged commit, released and sealed; a second change set that updates,
/// tombstones, adds, commits, updates with a partial row and unsets as
/// another actor, commits without a message and merges as a second tagged
/// commit, which the release moves to, then saves work it does not commit;
/// and a draft that saves and is discarded. In graph menu, first under a
/// descriptor whose utensil lacks its name, a primary line, a change set, a
/// tagged merge and a release; then under the fixture's, a second change set
/// and a second tagged merge.
async fn write_database(client: &Arc<Rusqlite>) {
    let descriptor = testdata("fixture/recipe.json");
    let mut d: Value = serde_json::from_str(&descriptor).expect("the descriptor");
    for k in d["kinds"].as_array_mut().expect("kinds") {
        if k["kind"] == "utensil" {
            let columns = k["columns"].as_object_mut().expect("columns");
            columns.remove("name").expect("utensil declares name");
        }
    }
    let before_gain = d.to_string();
    let (clock, ids) = (fixed_clock(), seeded_ids(ID_SEED));

    let g = engine(client, &descriptor, "recipe", &clock, &ids).await;
    let (main, release) = recipe_first(&g).await;
    let main = recipe_second(&g, main, &release).await;
    recipe_scrap(&g, &main).await;
    // Before the gain: rows, images and commits without the utensil's name.
    let h = engine(client, &before_gain, "menu", &clock, &ids).await;
    let mut lunch = h.create_primary(COOK, BREAD, "main").await.expect("main");
    let mut today = h.branch(COOK, &lunch.id, "today").await.expect("today");
    let sliced = edits(vec![
        (
            "step",
            upsert(&[r#"{"entity_key":"Knead","position":1,"instruction":"Slice","timings":{}}"#]),
        ),
        ("utensil", upsert(&[r#"{"entity_key":"Knife"}"#])),
    ]);
    today = h
        .save(COOK, &today.id, today.version, &sliced)
        .await
        .expect("save")
        .ref_;
    today = h
        .commit(COOK, &today.id, today.version, &message("today", false))
        .await
        .expect("commit")
        .ref_;
    let served = merged(
        h.merge(
            COOK,
            &today.id,
            &lunch.id,
            lunch.version,
            &[],
            &message("lunch", true),
        )
        .await
        .expect("merge"),
    );
    lunch = served.ref_;
    h.release(COOK, BREAD, &served.commit.expect("a commit").id, 0)
        .await
        .expect("release");

    // After it: the fixture's descriptor, which declares the name.
    let i = engine(client, &descriptor, "menu", &clock, &ids).await;
    let mut dinner = i.branch(ANN, &lunch.id, "dinner").await.expect("dinner");
    let named = edits(vec![(
        "utensil",
        upsert(&[
            r#"{"entity_key":"Knife","name":"Bread knife"}"#,
            r#"{"entity_key":"Board","name":"Bread board"}"#,
        ]),
    )]);
    dinner = i
        .save(ANN, &dinner.id, dinner.version, &named)
        .await
        .expect("save")
        .ref_;
    dinner = i
        .commit(ANN, &dinner.id, dinner.version, &message("dinner", false))
        .await
        .expect("commit")
        .ref_;
    merged(
        i.merge(
            ANN,
            &dinner.id,
            &lunch.id,
            lunch.version,
            &[],
            &message("supper", true),
        )
        .await
        .expect("merge"),
    );
}

/// Recipe's primary line, and its first change set: a row of every kind,
/// committed, merged as a tagged commit, released and sealed.
async fn recipe_first(g: &Engine) -> (crate::Ref, crate::Release) {
    let mut main = g.create_primary(COOK, BREAD, "main").await.expect("main");
    let mut first = g.branch(COOK, &main.id, "first").await.expect("first");
    let every_kind = edits(vec![
        (
            "cover",
            upsert(&[r#"{"entity_key":"Cover","photo_url":"https://example.com/bread.jpg"}"#]),
        ),
        (
            "ingredient",
            upsert(&[
                r#"{"entity_key":"Flour","step_key":"Knead","quantity":"500 g","substitutes":[{"name":"spelt","ratio":1}]}"#,
                r#"{"entity_key":"Salt","step_key":"Knead","quantity":"10 g","substitutes":null}"#,
            ]),
        ),
        (
            "note",
            upsert(&[
                r#"{"entity_key":"Note","body":"Proof overnight\tif there's time"}"#,
                r#"{"entity_key":"Reply","body":"Agreed","reply_to":"Note"}"#,
            ]),
        ),
        (
            "step",
            upsert(&[
                r#"{"entity_key":"Knead","position":1,"instruction":"Knead for ten minutes","timings":{"knead":"10m"},"scratch":"floury"}"#,
                r#"{"entity_key":"Bake","position":2,"instruction":"Bake at 230 C","timings":{"bake":"35m","preheat":"30m"}}"#,
            ]),
        ),
        ("tasting", upsert(&[FIRST_TASTING, SECOND_TASTING])),
        ("utensil", upsert(&[r#"{"name":"Bowl"}"#])),
    ]);
    first = g
        .save(COOK, &first.id, first.version, &every_kind)
        .await
        .expect("save")
        .ref_;
    first = g
        .commit(
            COOK,
            &first.id,
            first.version,
            &message("first draft", false),
        )
        .await
        .expect("commit")
        .ref_;
    let m1 = merged(
        g.merge(
            COOK,
            &first.id,
            &main.id,
            main.version,
            &[],
            &message("first", true),
        )
        .await
        .expect("merge"),
    );
    main = m1.ref_;
    let release = g
        .release(COOK, BREAD, &m1.commit.expect("a commit").id, 0)
        .await
        .expect("release");
    g.seal(COOK, &first.id, first.version).await.expect("seal");

    (main, release)
}

/// Recipe's second change set, merged as a second tagged commit, which the
/// release moves to, and then work it does not commit.
async fn recipe_second(g: &Engine, mut main: crate::Ref, release: &crate::Release) -> crate::Ref {
    let mut second = g.branch(ANN, &main.id, "second").await.expect("second");
    let changes = edits(vec![
        (
            "ingredient",
            KindEdits {
                delete: vec!["Salt".to_owned()],
                ..KindEdits::default()
            },
        ),
        (
            "step",
            upsert(&[
                r#"{"entity_key":"Knead","position":1,"instruction":"Knead for twelve minutes","timings":{"knead":"12m","rest":"5m"},"scratch":"sticky"}"#,
                r#"{"entity_key":"Proof","position":3,"instruction":"Proof for an hour","timings":{"proof":"1h"}}"#,
            ]),
        ),
    ]);
    second = g
        .save(ANN, &second.id, second.version, &changes)
        .await
        .expect("save")
        .ref_;
    second = g
        .commit(
            ANN,
            &second.id,
            second.version,
            &message("second draft", false),
        )
        .await
        .expect("commit")
        .ref_;
    // A partial row keeps the columns it lacks; the unset removes the change
    // set's own Proof row, as Cook, which the DELETE image names.
    let partial = edits(vec![(
        "step",
        KindEdits {
            unset: vec!["Proof".to_owned()],
            ..upsert(&[r#"{"entity_key":"Knead","instruction":"Knead until smooth"}"#])
        },
    )]);
    second = g
        .save(COOK, &second.id, second.version, &partial)
        .await
        .expect("save")
        .ref_;
    second = g
        .commit(COOK, &second.id, second.version, &CommitOptions::default())
        .await
        .expect("commit")
        .ref_;
    let m2 = merged(
        g.merge(
            ANN,
            &second.id,
            &main.id,
            main.version,
            &[],
            &message("second", true),
        )
        .await
        .expect("merge"),
    );
    main = m2.ref_;
    g.release(
        ANN,
        BREAD,
        &m2.commit.expect("a commit").id,
        release.version,
    )
    .await
    .expect("move the release");
    // Work the change set does not commit: a partial row on insert stores
    // null for each column it lacks.
    let uncommitted = edits(vec![(
        "tasting",
        upsert(&[r#"{"entity_key":"First","score":5}"#]),
    )]);
    g.save(ANN, &second.id, second.version, &uncommitted)
        .await
        .expect("save");

    main
}

/// Recipe's draft that saves and is discarded.
async fn recipe_scrap(g: &Engine, main: &crate::Ref) {
    let mut scrap = g.branch(COOK, &main.id, "scrap").await.expect("scrap");
    let scrapped = edits(vec![
        (
            "cover",
            KindEdits {
                delete: vec!["Cover".to_owned()],
                ..KindEdits::default()
            },
        ),
        (
            "utensil",
            upsert(&[r#"{"entity_key":"Whisk","name":"Whisk"}"#]),
        ),
    ]);
    scrap = g
        .save(COOK, &scrap.id, scrap.version, &scrapped)
        .await
        .expect("save")
        .ref_;
    g.discard(COOK, &scrap.id, scrap.version)
        .await
        .expect("discard");
}

fn quote(name: &str) -> String {
    format!("\"{}\"", name.replace('"', "\"\""))
}

/// A value as `dumpDatabase` writes it: text quoted with `'` doubled, an
/// integer as written, `NULL`.
fn literal(value: ValueRef<'_>, place: &str) -> String {
    match value {
        ValueRef::Null => "NULL".to_owned(),
        ValueRef::Integer(n) => n.to_string(),
        ValueRef::Text(bytes) => {
            let text = std::str::from_utf8(bytes).expect("UTF-8 text");
            assert!(
                !text.chars().any(|c| c < ' '
                    || c == '\u{7f}'
                    || c == '\u{85}'
                    || c == '\u{2028}'
                    || c == '\u{2029}'),
                "{place} holds no line break"
            );
            format!("'{}'", text.replace('\'', "''"))
        }
        other => panic!("{place}: the layout holds text, integers and NULL only, not {other:?}"),
    }
}

/// The database as `dumpDatabase` writes it: the layout's statements, then
/// one INSERT per row, the tables in the layout's order and each table's
/// rows by primary key in byte order, one statement per line.
fn dump(connection: &rusqlite::Connection) -> String {
    let mut lines: Vec<String> = layout(&default_table_name)
        .expect("the layout")
        .into_iter()
        .map(|statement| statement + ";")
        .collect();
    for local in TABLES {
        let name = default_table_name(local);
        let columns: Vec<String> = connection
            .prepare("SELECT name FROM pragma_table_info(?1) ORDER BY cid")
            .expect("prepare")
            .query_map([&name], |r| r.get::<_, String>(0))
            .expect("columns")
            .collect::<Result<_, _>>()
            .expect("columns");
        let key = if local.ends_with("_history") {
            "history_id"
        } else {
            "id"
        };
        let list = columns.join(", ");
        let sql = format!("SELECT {list} FROM {} ORDER BY {key}", quote(&name));
        let mut statement = connection.prepare(&sql).expect("prepare");
        let mut rows = statement.query([]).expect("rows");
        while let Some(r) = rows.next().expect("a row") {
            let values: Vec<String> = columns
                .iter()
                .enumerate()
                .map(|(i, column)| {
                    literal(r.get_ref(i).expect("a value"), &format!("{name}.{column}"))
                })
                .collect();
            lines.push(format!(
                "INSERT INTO {} ({list}) VALUES ({});",
                quote(&name),
                values.join(", ")
            ));
        }
    }
    lines.join("\n") + "\n"
}

/// The script through this adapter writes the file the TypeScript adapter
/// wrote, byte for byte.
#[tokio::test]
async fn the_vectors_script_writes_the_typescript_adapters_file() {
    let client = Arc::new(Rusqlite::new(
        rusqlite::Connection::open_in_memory().expect("open an in-memory database"),
    ));
    write_database(&client).await;
    let got = dump(&*client.connection().await);
    let want = testdata("sqlite/typescript.sql");
    if got != want {
        let (got_lines, want_lines): (Vec<&str>, Vec<&str>) =
            (got.lines().collect(), want.lines().collect());
        let first = got_lines
            .iter()
            .zip(&want_lines)
            .position(|(g, w)| g != w)
            .unwrap_or(got_lines.len().min(want_lines.len()));
        panic!(
            "the file differs from typescript.sql at line {} ({} lines, want {}):\n  got  {}\n  want {}",
            first + 1,
            got_lines.len(),
            want_lines.len(),
            got_lines.get(first).unwrap_or(&"(none)"),
            want_lines.get(first).unwrap_or(&"(none)")
        );
    }
}
