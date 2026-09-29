//! The Postgres adapter and its tokio-postgres client against Postgres.

mod support;

use serde_json::json;
use superschematic_versiongraph_engine::postgres::TokioPostgres;
use superschematic_versiongraph_engine::storage::{
    NewCommit, NewRef, Patch, Ref, RefUpdate, RowWrite, Storage,
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
