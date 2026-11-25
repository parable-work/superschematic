//! The Postgres adapter and its tokio-postgres client against Postgres.

mod support;

use serde_json::json;
use superschematic_versiongraph_engine::postgres::TokioPostgres;
use superschematic_versiongraph_engine::storage::{
    NewCommit, NewRef, Patch, Ref, RefUpdate, ReleasedWrite, RowWrite, Storage,
};
use superschematic_versiongraph_engine::Error;

/// A prune past every image's retention deletes the superseded image no
/// commit pins and keeps the pinned one, and a kind declared without
/// retentionDays has no prune function and prunes nothing.
#[tokio::test]
async fn prune_keeps_pinned_images() {
    let Some(dsn) = support::database("prune_keeps_pinned_images") else {
        return;
    };
    let schema = support::Schema::create(&dsn, "vg_rust_adapter").await;
    let store = schema.storage(&support::adapter()).await;
    let sql = schema.connect().await;
    sql.batch_execute(
        "INSERT INTO recipe (id, title, created_by) VALUES ('00000000-0000-0000-0000-000000000001', 'Bread', '00000000-0000-0000-0000-000000000002')",
    )
    .await
    .expect("insert the root");
    let actor = "2";
    let mut tx = store.begin().await.expect("begin");
    let r = tx
        .create_ref(NewRef {
            root: "1".to_owned(),
            name: "main".to_owned(),
            actor: actor.to_owned(),
            ..NewRef::default()
        })
        .await
        .expect("create a ref");
    let mut pinned = None;
    for instruction in ["Mix", "Mix well", "Mix gently"] {
        let row =
            json!({"entity_key": "Mix", "position": 1, "instruction": instruction, "timings": {}});
        let stored = tx
            .upsert_row(
                "step",
                RowWrite {
                    ref_id: r.id.clone(),
                    root: r.root.clone(),
                    row,
                    tombstone: false,
                    actor: actor.to_owned(),
                },
            )
            .await
            .expect("write a step");
        if instruction == "Mix well" {
            pinned = Some(stored);
        }
    }
    tx.upsert_row(
        "cover",
        RowWrite {
            ref_id: r.id.clone(),
            root: r.root.clone(),
            row: json!({"photo_url": "a.jpg"}),
            tombstone: false,
            actor: actor.to_owned(),
        },
    )
    .await
    .expect("write a cover");
    let pinned = pinned.expect("the pinned row");
    let commit = tx
        .insert_commit(NewCommit {
            root: r.root.clone(),
            ref_id: r.id.clone(),
            content_hash: "h".to_owned(),
            actor: actor.to_owned(),
            ..NewCommit::default()
        })
        .await
        .expect("write a commit");
    tx.insert_patches(
        &commit.id,
        &[Patch {
            kind: "step".to_owned(),
            entity_key: "Mix".to_owned(),
            entity_id: pinned["id"].as_str().expect("an id").to_owned(),
            entity_version: pinned["_version"].as_i64().expect("a version"),
            operation: "ADD".to_owned(),
            ..Patch::default()
        }],
    )
    .await
    .expect("write a patch");
    tx.commit().await.expect("commit");
    for table in ["step_history", "cover_history"] {
        sql.batch_execute(&format!(
            "UPDATE {table} SET recorded_at = now() - interval '400 days'"
        ))
        .await
        .expect("age the history");
    }
    let mut tx = store.begin().await.expect("begin");
    let steps = tx.prune("step", 365, 0).await.expect("prune steps");
    let covers = tx.prune("cover", 365, 0).await.expect("prune covers");
    tx.commit().await.expect("commit");
    assert_eq!(
        (steps, covers),
        (1, 0),
        "want the unpinned superseded step image alone pruned"
    );
    let kept: Vec<String> = sql
        .query(
            "SELECT data->>'instruction' FROM step_history ORDER BY _version",
            &[],
        )
        .await
        .expect("read the history")
        .iter()
        .map(|row| row.get(0))
        .collect();
    assert_eq!(
        kept,
        ["Mix well", "Mix gently"],
        "want the pinned image and the latest"
    );
}

/// While one transaction holds the graph's sweep lock, another does not get
/// it and does not wait; once the first ends, the lock is free.
#[tokio::test]
async fn sweep_lock_is_held_by_one_transaction() {
    let Some(dsn) = support::database("sweep_lock_is_held_by_one_transaction") else {
        return;
    };
    let schema = support::Schema::create(&dsn, "vg_rust_adapter").await;
    let adapter = support::adapter();
    let (holder, other) = (
        schema.storage(&adapter).await,
        schema.storage(&adapter).await,
    );
    let mut held = holder.begin().await.expect("begin");
    assert!(
        held.sweep_lock().await.expect("take the lock"),
        "the first transaction did not get a free sweep lock"
    );
    let waited =
        tokio::time::timeout(std::time::Duration::from_secs(5), take(other.as_ref())).await;
    assert_eq!(
        waited.ok(),
        Some(false),
        "a second transaction got the lock the first holds, or waited"
    );
    held.commit().await.expect("commit");
    assert!(
        take(other.as_ref()).await,
        "the sweep lock stayed held after its transaction ended"
    );
}

/// Takes the sweep lock in a transaction of its own and ends it.
async fn take(store: &dyn Storage) -> bool {
    let mut tx = store.begin().await.expect("begin");
    let locked = tx.sweep_lock().await.expect("take the lock");
    tx.commit().await.expect("commit");
    locked
}

/// An operation dropped before it ended leaves its transaction open on the
/// connection; the binding's next transaction rolls it back first, so what
/// the dropped one wrote is gone and the next one runs on its own.
#[tokio::test]
async fn a_dropped_transaction_is_rolled_back_by_the_next() {
    let Some(dsn) = support::database("a_dropped_transaction_is_rolled_back_by_the_next") else {
        return;
    };
    let schema = support::Schema::create(&dsn, "vg_rust_adapter").await;
    let store = schema.storage(&support::adapter()).await;
    let sql = schema.connect().await;
    sql.batch_execute(
        "INSERT INTO recipe (id, title, created_by) VALUES ('00000000-0000-0000-0000-000000000001', 'Bread', '00000000-0000-0000-0000-000000000002')",
    )
    .await
    .expect("insert the root");
    let new_ref = |name: &str| NewRef {
        root: "1".to_owned(),
        name: name.to_owned(),
        actor: "2".to_owned(),
        ..NewRef::default()
    };
    {
        let mut dropped = store.begin().await.expect("begin");
        dropped
            .create_ref(new_ref("dropped"))
            .await
            .expect("create a ref");
        // Neither committed nor rolled back.
    }
    let mut tx = store
        .begin()
        .await
        .expect("begin after a dropped transaction");
    tx.create_ref(new_ref("kept")).await.expect("create a ref");
    tx.commit().await.expect("commit");
    let names: Vec<String> = sql
        .query("SELECT name FROM recipe_ref ORDER BY name", &[])
        .await
        .expect("read the refs")
        .iter()
        .map(|row| row.get(0))
        .collect();
    assert_eq!(
        names,
        ["kept"],
        "the dropped transaction's ref must be rolled back"
    );
}

/// Writes the Bread root and a primary line of it, and returns the ref.
async fn new_ref(schema: &support::Schema, store: &dyn Storage) -> Ref {
    schema
        .connect()
        .await
        .batch_execute(
            "INSERT INTO recipe (id, title, created_by) VALUES ('00000000-0000-0000-0000-000000000001', 'Bread', '00000000-0000-0000-0000-000000000002')",
        )
        .await
        .expect("insert the root");
    let mut tx = store.begin().await.expect("begin");
    let r = tx
        .create_ref(NewRef {
            root: "1".to_owned(),
            name: "main".to_owned(),
            actor: "2".to_owned(),
            ..NewRef::default()
        })
        .await
        .expect("create a ref");
    tx.commit().await.expect("commit");
    r
}

/// While one transaction holds a ref's lock, another transaction's
/// `lock_ref` of it waits (here past its `lock_timeout`, with
/// lock_not_available) while `read_ref` does not, and once the first ends
/// the lock is free.
#[tokio::test]
async fn lock_ref_waits_for_the_holder() {
    let Some(dsn) = support::database("lock_ref_waits_for_the_holder") else {
        return;
    };
    let schema = support::Schema::create(&dsn, "vg_rust_adapter").await;
    let adapter = support::adapter();
    let holder = schema.storage(&adapter).await;
    let r = new_ref(&schema, holder.as_ref()).await;
    let waiting = schema.connect().await;
    waiting
        .batch_execute("SET lock_timeout = '200ms'")
        .await
        .expect("set lock_timeout");
    let other = adapter.storage(TokioPostgres::new(waiting));
    let mut held = holder.begin().await.expect("begin");
    held.lock_ref(&r.id).await.expect("lock the ref");
    let mut tx = other.begin().await.expect("begin");
    let locked = tx.lock_ref(&r.id).await;
    assert!(
        matches!(&locked, Err(e) if e.to_string().contains("SQLSTATE 55P03")),
        "a second transaction's lock_ref = {locked:?}, want it to wait for the holder past its lock_timeout (55P03)"
    );
    tx.rollback().await.expect("roll back");
    let mut tx = other.begin().await.expect("begin");
    let read = tx
        .read_ref(&r.id)
        .await
        .expect("read the ref without waiting");
    assert_eq!(read.id, r.id);
    tx.rollback().await.expect("roll back");
    held.commit().await.expect("commit");
    let mut tx = other.begin().await.expect("begin");
    tx.lock_ref(&r.id)
        .await
        .expect("lock the ref once the holder ended");
    tx.commit().await.expect("commit");
}

/// `update_ref` at the ref's version moves it to the next version, and
/// `update_ref` at the version it had before is `VersionConflict` and
/// changes nothing.
#[tokio::test]
async fn update_ref_fences_its_version() {
    let Some(dsn) = support::database("update_ref_fences_its_version") else {
        return;
    };
    let schema = support::Schema::create(&dsn, "vg_rust_adapter").await;
    let store = schema.storage(&support::adapter()).await;
    let r = new_ref(&schema, store.as_ref()).await;
    let update = |seal| RefUpdate {
        id: r.id.clone(),
        version: r.version,
        head: None,
        base: None,
        seal,
        actor: "2".to_owned(),
    };
    let mut tx = store.begin().await.expect("begin");
    let moved = tx.update_ref(update(false)).await.expect("update the ref");
    assert_eq!(moved.version, r.version + 1);
    let stale = tx.update_ref(update(true)).await;
    assert!(
        matches!(stale, Err(Error::VersionConflict)),
        "update_ref at the stale version {} = {stale:?}, want VersionConflict",
        r.version
    );
    let now = tx.read_ref(&r.id).await.expect("read the ref");
    assert_eq!((now.version, now.sealed), (moved.version, false));
    tx.commit().await.expect("commit");
}

/// `discard_ref` of a ref already discarded, even at its current version,
/// is `VersionConflict` and keeps who discarded it first.
#[tokio::test]
async fn discard_ref_refuses_a_discarded_ref() {
    let Some(dsn) = support::database("discard_ref_refuses_a_discarded_ref") else {
        return;
    };
    let schema = support::Schema::create(&dsn, "vg_rust_adapter").await;
    let store = schema.storage(&support::adapter()).await;
    let r = new_ref(&schema, store.as_ref()).await;
    let mut tx = store.begin().await.expect("begin");
    tx.discard_ref(&r.id, r.version, "2")
        .await
        .expect("discard the ref");
    let discarded = tx.read_ref(&r.id).await.expect("read the ref");
    assert!(discarded.discarded, "discard_ref left the ref live");
    let again = tx.discard_ref(&r.id, discarded.version, "3").await;
    assert!(
        matches!(again, Err(Error::VersionConflict)),
        "discard_ref of a discarded ref at its version {} = {again:?}, want VersionConflict",
        discarded.version
    );
    tx.commit().await.expect("commit");
    let by: String = schema
        .connect()
        .await
        .query_one("SELECT deleted_by::text FROM recipe_ref", &[])
        .await
        .expect("read deleted_by")
        .get(0);
    assert_eq!(by, "00000000-0000-0000-0000-000000000002");
}

/// Gives the roots Bread and Soup the row a root has, created by the
/// default actor.
async fn seed_roots(sql: &tokio_postgres::Client) {
    sql.batch_execute(&format!(
        "INSERT INTO recipe (id, title, created_by) VALUES ('{}', 'Bread', '{actor}'), ('{}', 'Soup', '{actor}')",
        support::hyphenated("Bread"),
        support::hyphenated("Soup"),
        actor = support::hyphenated(support::DEFAULT_ACTOR),
    ))
    .await
    .expect("insert the roots");
}

/// Every row of the part kind's released table, ids in their canonical form.
async fn released_parts(sql: &tokio_postgres::Client) -> Vec<support::ReleasedPart> {
    let canonical =
        |text: String| superschematic_versiongraph_engine::canonical::uuid(&text).expect("a UUID");
    sql.query(
        "SELECT recipe_id::text, entity_key::text, heading, rank, parent_key::text, commit_id::text, \"sequence\" \
         FROM part_released",
        &[],
    )
    .await
    .expect("read the released rows")
    .iter()
    .map(|row| support::ReleasedPart {
        root: canonical(row.get(0)),
        key: canonical(row.get(1)),
        heading: row.get(2),
        rank: row.get(3),
        parent: row.get::<_, Option<String>>(4).map(canonical),
        commit: canonical(row.get(5)),
        sequence: row.get(6),
    })
    .collect()
}

/// `write_released` rewrites one root's rows of a kind in the kind's
/// released table (D139): a replace deletes the root's rows first, a delete
/// removes an entity's, and an upsert writes a row by its root and entity
/// key with the release's commit and sequence, from the kind's content
/// columns alone. Another root's rows stay, a row the write does not name
/// keeps the commit and sequence it was written with, and a kind that keeps
/// no released table is refused.
#[tokio::test]
async fn write_released_rewrites_one_roots_rows_of_a_kind() {
    let Some(dsn) = support::database("write_released_rewrites_one_roots_rows_of_a_kind") else {
        return;
    };
    let schema = support::Schema::create(&dsn, "vg_rust_adapter").await;
    let store = schema.storage(&support::adapter()).await;
    let sql = schema.connect().await;
    seed_roots(&sql).await;
    let (bread, soup) = support::released_commits(store.as_ref())
        .await
        .expect("the commits");
    for case in support::released_cases(&bread, &soup) {
        let result = support::write_released(store.as_ref(), case.kind, case.write.clone()).await;
        support::check_released(&case, result, released_parts(&sql).await);
    }
}

/// A released row's values are written as `upsert_row` writes a member
/// row's, whatever their class: a released table of the tasting kind, which
/// has a column of every class, holds what the kind's own table does. A
/// JSON null in a JSONB column is the value null, not SQL NULL, and a role
/// column the released table happens to have is not written.
#[tokio::test]
async fn write_released_writes_each_value_as_a_member_rows_is_written() {
    let test = "write_released_writes_each_value_as_a_member_rows_is_written";
    let Some(dsn) = support::database(test) else {
        return;
    };
    let schema = support::Schema::create(&dsn, "vg_rust_adapter").await;
    let sql = schema.connect().await;
    // The tasting table's columns, none required, and a released table's own.
    sql.batch_execute(
        "CREATE TABLE tasting_released AS SELECT * FROM tasting WITH NO DATA; \
         ALTER TABLE tasting_released ADD COLUMN commit_id UUID NOT NULL, ADD COLUMN \"sequence\" BIGINT NOT NULL; \
         CREATE UNIQUE INDEX uq_tasting_released ON tasting_released (recipe_id, entity_key)",
    )
    .await
    .expect("create the released table");
    let mut descriptor: serde_json::Value =
        serde_json::from_str(&support::descriptor()).expect("the descriptor");
    for kind in descriptor["kinds"].as_array_mut().expect("kinds") {
        if kind["kind"] == "tasting" {
            kind["releasedTable"] = "tasting_released".into();
        }
    }
    let adapter = std::sync::Arc::new(
        superschematic_versiongraph_engine::postgres::Adapter::new(
            &descriptor.to_string(),
            Default::default(),
        )
        .expect("the adapter"),
    );
    let store = schema.storage(&adapter).await;
    seed_roots(&sql).await;
    let (bread, _) = support::released_commits(store.as_ref())
        .await
        .expect("the commits");
    let mut tx = store.begin().await.expect("begin");
    let line = tx
        .create_ref(NewRef {
            root: "Bread".to_owned(),
            name: "tastings".to_owned(),
            actor: support::DEFAULT_ACTOR.to_owned(),
            ..NewRef::default()
        })
        .await
        .expect("create a ref");
    let mut stored = Vec::new();
    for row in [
        json!({"entity_key": "First", "taster": "Ann", "salty": true, "score": 4.5, "servings": 9_007_199_254_740_993_i64,
            "tasted_on": "2026-09-01", "tasted_at": "2026-09-01T10:00:00.12Z", "served_at": "18:30:00",
            "rested": "1h30m0s", "verdict": "again", "remarks": {"crust": [1, 2.5], "crumb": "open"},
            "tags": ["sour", "a \"quoted\" tag"], "helpers": ["Bob", "Cy"], "bites": [[1, 2], [3]]}),
        json!({"entity_key": "Second", "taster": "Bob", "salty": false, "score": 1, "servings": -3,
            "tasted_on": "2026-02-28", "tasted_at": "2026-09-01T12:30:00+02:30", "served_at": "14:30:00",
            "rested": "-1m30.5s", "verdict": "never", "remarks": null, "tags": [], "helpers": [], "bites": []}),
    ] {
        let write = RowWrite {
            ref_id: line.id.clone(),
            root: line.root.clone(),
            row,
            tombstone: false,
            actor: support::DEFAULT_ACTOR.to_owned(),
        };
        stored.push(
            tx.upsert_row("tasting", write)
                .await
                .expect("write a tasting"),
        );
    }
    // The rows as the engine hands them over: canonical, role columns and
    // all.
    tx.write_released(
        "tasting",
        ReleasedWrite {
            root: "Bread".to_owned(),
            commit: bread[0].clone(),
            sequence: 1,
            replace: true,
            upsert: stored,
            ..ReleasedWrite::default()
        },
    )
    .await
    .expect("write the released tastings");
    tx.commit().await.expect("commit");
    let count = |condition: &'static str| {
        let sql = &sql;
        async move {
            let statement = format!(
                "SELECT count(*) FROM tasting_released AS r JOIN tasting AS t ON t.entity_key = r.entity_key WHERE {condition}"
            );
            let n: i64 = sql
                .query_one(&statement, &[])
                .await
                .expect("count the released tastings")
                .get(0);
            n
        }
    };
    assert_eq!(count("true").await, 2);
    assert_eq!(
        count(
            "r.recipe_id = t.recipe_id AND r.taster = t.taster AND r.salty = t.salty AND r.score = t.score \
             AND r.servings = t.servings AND r.tasted_on = t.tasted_on AND r.tasted_at = t.tasted_at \
             AND r.served_at = t.served_at AND r.rested = t.rested AND r.verdict = t.verdict \
             AND r.remarks = t.remarks AND r.tags = t.tags AND r.helpers = t.helpers AND r.bites = t.bites"
        )
        .await,
        2,
        "a released row holds what its member row does"
    );
    assert_eq!(
        count("r.remarks = 'null'::jsonb").await,
        1,
        "a JSON null is the value null"
    );
    assert_eq!(
        count(
            "r.id IS NULL AND r.ref_id IS NULL AND r._version IS NULL AND r.deleted_on_ref IS NULL \
             AND r.\"sequence\" = 1"
        )
        .await,
        2,
        "the adapter writes the root, the key, the content columns, the commit and the sequence"
    );
}
