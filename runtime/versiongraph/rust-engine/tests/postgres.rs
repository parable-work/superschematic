//! The Postgres adapter and its tokio-postgres client against Postgres.

mod support;

use serde_json::json;
use superschematic_versiongraph_engine::storage::{NewCommit, NewRef, Patch, RowWrite, Storage};

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
